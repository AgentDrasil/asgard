package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// shortSockDir returns a directory whose socket paths fit sun_path.
func shortSockDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "st")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func unixClient(sock string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
}

func postStatus(t *testing.T, c *http.Client, body any) int {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := c.Post("http://asgard/agent-status", "application/json", bytes.NewReader(b))
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestStatusSocketEmitsRunEvents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	runDir := t.TempDir()
	sr := baseStartRun()
	sink := newRecordingSink()
	srv := newStatusServer(context.Background(), sr, testAgent("a", "h1", ""), runDir, sink)
	dir := shortSockDir(t)
	stop, err := srv.serve(dir)
	require.NoError(t, err)
	t.Cleanup(stop)
	client := unixClient(filepath.Join(dir, SocketName))

	// The sandbox claims other ids; the runner stamps the run's own.
	code := postStatus(t, client, map[string]any{
		"chat_id": "evil-chat", "node_id": "evil-node", "run_token": "evil-token",
		"step_index": 4, "source": "aw", "entry_type": "tool_call", "content": "did a thing",
		"metadata": map[string]any{
			"model":        "m1",
			"target_files": []any{"/tmp/plan/a.md", "/session/notes.md", ".tmp/b.txt", "src/main.go", 7},
		},
	})
	require.Equal(t, http.StatusNoContent, code)
	require.Equal(t, http.StatusNoContent, postStatus(t, client, map[string]any{"step_index": 5, "source": "aw", "entry_type": "thinking", "content": "hm"}))

	evs := sink.eventList()
	require.Len(t, evs, 2)
	first := evs[0]
	assert.Equal(t, uint64(1), first.GetEventIndex())
	assert.Equal(t, "run-1", first.GetRunId())
	assert.Equal(t, "chat-1", first.GetChatId())
	assert.Equal(t, "node-7", first.GetNodeId())
	assert.Equal(t, "tok", first.GetRunToken())
	assert.Equal(t, int32(4), first.GetStepIndex())
	assert.Equal(t, "tool_call", first.GetEntryType())
	assert.Equal(t, "did a thing", first.GetContent())
	var meta map[string]any
	require.NoError(t, json.Unmarshal(first.GetMetadataJson(), &meta))
	assert.Equal(t, "m1", meta["model"])

	tmp, sess := runnerpaths.SessionTmpDir("chat-1"), runnerpaths.SessionDir("chat-1")
	require.Len(t, first.GetArtifacts(), 3)
	assert.Equal(t, "/tmp/plan/a.md", first.GetArtifacts()[0].GetViewerPath())
	assert.Equal(t, filepath.Join(tmp, "plan/a.md"), first.GetArtifacts()[0].GetAbsPath())
	assert.Equal(t, "/session/notes.md", first.GetArtifacts()[1].GetViewerPath())
	assert.Equal(t, filepath.Join(sess, "notes.md"), first.GetArtifacts()[1].GetAbsPath())
	assert.Equal(t, "/tmp/b.txt", first.GetArtifacts()[2].GetViewerPath())
	assert.Equal(t, filepath.Join(tmp, "b.txt"), first.GetArtifacts()[2].GetAbsPath())

	assert.Equal(t, uint64(2), evs[1].GetEventIndex())
	assert.Equal(t, "hm", evs[1].GetContent())
	assert.Empty(t, evs[1].GetArtifacts())
	assert.Empty(t, evs[1].GetMetadataJson())
}

func TestStatusArtifactsGitIgnore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		out, err := execGit(ws, args...)
		require.NoError(t, err, out)
	}
	runGit("init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(ws, ".gitignore"), []byte("build/\n"), 0o644))
	srv := newStatusServer(context.Background(), baseStartRun(), testAgent("a", "h1", ""), ws, newRecordingSink())

	got := srv.artifacts(map[string]any{"target_files": []any{"build/out.bin", "src/main.go", filepath.Join(ws, "build/abs.bin")}})

	require.Len(t, got, 2)
	assert.Equal(t, "build/out.bin", got[0].GetViewerPath())
	assert.Equal(t, filepath.Join(ws, "build/out.bin"), got[0].GetAbsPath())
	assert.Equal(t, filepath.Join(ws, "build/abs.bin"), got[1].GetViewerPath())
}

func TestStatusSplitsLargeArtifactLists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sink := newRecordingSink()
	srv := newStatusServer(context.Background(), baseStartRun(), testAgent("a", "h1", ""), t.TempDir(), sink)

	files := make([]any, 2500)
	for i := range files {
		files[i] = fmt.Sprintf("/tmp/f%d.txt", i)
	}
	require.NoError(t, srv.emit(context.Background(), statusUpdate{Content: "c", EntryType: "tool_call", Metadata: map[string]any{"target_files": files}}))

	evs := sink.eventList()
	require.Len(t, evs, 3)
	for i, ev := range evs {
		assert.Equal(t, uint64(i+1), ev.GetEventIndex())
		assert.LessOrEqual(t, len(ev.GetArtifacts()), maxEventArtifact)
	}
	assert.Equal(t, "c", evs[0].GetContent())
	assert.Empty(t, evs[1].GetContent())
	assert.Len(t, evs[2].GetArtifacts(), 500)
}

func TestStatusRejectsBadBody(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sink := newRecordingSink()
	srv := newStatusServer(context.Background(), baseStartRun(), testAgent("a", "h1", ""), t.TempDir(), sink)
	dir := shortSockDir(t)
	stop, err := srv.serve(dir)
	require.NoError(t, err)
	t.Cleanup(stop)

	resp, err := unixClient(filepath.Join(dir, SocketName)).Post("http://asgard/agent-status", "application/json", bytes.NewReader([]byte("{nope")))
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Empty(t, sink.eventList())
}

func TestListenUnixLongPath(t *testing.T) {
	t.Parallel()

	long := filepath.Join(shortSockDir(t), strings.Repeat("d", 120))
	require.NoError(t, os.MkdirAll(long, 0o755))
	require.Greater(t, len(filepath.Join(long, SocketName)), 108)
	ln, closeDir, err := listenUnix(long)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close(); closeDir() })

	// The socket is reachable through the same short fd path form.
	c, err := net.Dial("unix", fmt.Sprintf("/proc/self/fd/%d/%s", openDirFD(t, long), SocketName))
	require.NoError(t, err)
	_ = c.Close()
}

func openDirFD(t *testing.T, dir string) uintptr {
	t.Helper()
	f, err := os.Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	return f.Fd()
}

func TestTruncateUTF8(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "abc", truncateUTF8("abc", 10))
	// "é" is 2 bytes: cutting at 3 would split the second one.
	assert.Equal(t, "éa", truncateUTF8("éaéb", 3))
	assert.Equal(t, "", truncateUTF8("日本", 2))
	got, cut := truncateError(strings.Repeat("日", maxErrorSize))
	assert.True(t, cut)
	assert.True(t, utf8.ValidString(got))
	got, cut = truncateError("bad \xff byte")
	assert.False(t, cut)
	assert.True(t, utf8.ValidString(got))
}

func TestStatusEventsAreValidUTF8(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sink := newRecordingSink()
	srv := newStatusServer(context.Background(), baseStartRun(), testAgent("a", "h1", ""), t.TempDir(), sink)

	// Multi-byte content straddling the 4 MiB cap, plus invalid sandbox strings.
	content := strings.Repeat("日", maxEventContent/3+10)
	require.NoError(t, srv.emit(context.Background(), statusUpdate{Content: content, Source: "a\xffb", EntryType: "t\xc3"}))
	require.NoError(t, srv.emit(context.Background(), statusUpdate{Content: "x\xfe", Metadata: map[string]any{"target_files": []any{"/tmp/b\xffad.md"}}}))

	for _, ev := range sink.eventList() {
		_, err := proto.Marshal(ev)
		require.NoError(t, err)
	}
	evs := sink.eventList()
	assert.LessOrEqual(t, len(evs[0].GetContent()), maxEventContent)
	assert.Equal(t, "a�b", evs[0].GetSource())
}

// blockingSink holds Event until released.
type blockingSink struct {
	*recordingSink
	entered chan struct{}
	release chan struct{}
}

func (b *blockingSink) Event(ctx context.Context, ev *runnerpb.RunEvent) error {
	close(b.entered)
	<-b.release
	return b.recordingSink.Event(ctx, ev)
}

func TestStatusStopWaitsForInFlightUpdates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sink := &blockingSink{recordingSink: newRecordingSink(), entered: make(chan struct{}), release: make(chan struct{})}
	srv := newStatusServer(context.Background(), baseStartRun(), testAgent("a", "h1", ""), t.TempDir(), sink)
	dir := shortSockDir(t)
	stop, err := srv.serve(dir)
	require.NoError(t, err)

	posted := make(chan int, 1)
	go func() {
		posted <- postStatus(t, unixClient(filepath.Join(dir, SocketName)), map[string]any{"content": "last"})
	}()
	<-sink.entered

	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
		require.FailNow(t, "stop returned while an update was in flight")
	case <-time.After(150 * time.Millisecond):
	}
	close(sink.release)
	<-stopped
	assert.Equal(t, http.StatusNoContent, <-posted)
	require.Len(t, sink.eventList(), 1, "the final update is queued before stop returns")
}
