package service

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

const chat = "chat1"

// startService serves a Service on a loopback port with HOME pointing at a
// temp dir and returns a client plus the workspace dir.
func startService(t *testing.T, opts Options) (runnerpb.RunnerServiceClient, string) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv(runnerpaths.EnvDataDir, filepath.Join(h, "session-data"))
	ws := filepath.Join(t.TempDir(), "ws")
	require.NoError(t, os.MkdirAll(ws, 0o755))
	require.NoError(t, os.MkdirAll(runnerpaths.SessionTmpDir(chat), 0o755))
	require.NoError(t, os.MkdirAll(runnerpaths.SessionDir(chat), 0o755))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	if opts.Agents == nil {
		opts.Agents = func() ([]*agentspec.Agent, error) {
			return []*agentspec.Agent{{Config: agentspec.AgentConfig{RunDirs: []string{ws}}}}, nil
		}
	}
	runnerpb.RegisterRunnerServiceServer(srv, New(opts))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	cc, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = cc.Close() })
	return runnerpb.NewRunnerServiceClient(cc), ws
}

func readAll(t *testing.T, st grpc.ServerStreamingClient[runnerpb.ReadFileChunk]) (*runnerpb.FileInfo, []byte, error) {
	t.Helper()
	var info *runnerpb.FileInfo
	var data []byte
	for {
		c, err := st.Recv()
		if err == io.EOF {
			return info, data, nil
		}
		if err != nil {
			return info, data, err
		}
		if c.GetInfo() != nil {
			info = c.GetInfo()
		}
		data = append(data, c.GetData()...)
	}
}

func TestReadFileRangesScopeAndTraversal(t *testing.T) {
	cl, ws := startService(t, Options{})
	require.NoError(t, os.WriteFile(filepath.Join(ws, "a.txt"), []byte("0123456789"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(runnerpaths.SessionTmpDir(chat), "t.txt"), []byte("tmp"), 0o644))
	secret := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("s"), 0o644))
	require.NoError(t, os.Symlink(secret, filepath.Join(ws, "link.txt")))

	abs := &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, RelOrPath: ws}
	tests := []struct {
		name     string
		req      *runnerpb.ReadFileRequest
		want     string
		wantPath string
		code     codes.Code
	}{
		{"whole", &runnerpb.ReadFileRequest{SessionId: chat, Path: "a.txt", RunDir: abs}, "0123456789", "a.txt", codes.OK},
		{"range", &runnerpb.ReadFileRequest{SessionId: chat, Path: "a.txt", RunDir: abs, Offset: 2, Length: 3}, "234", "a.txt", codes.OK},
		{"to end", &runnerpb.ReadFileRequest{SessionId: chat, Path: "a.txt", RunDir: abs, Offset: 8}, "89", "a.txt", codes.OK},
		{"tmp namespace", &runnerpb.ReadFileRequest{SessionId: chat, Path: "/tmp/t.txt", RunDir: abs}, "tmp", "/tmp/t.txt", codes.OK},
		{"offset past end", &runnerpb.ReadFileRequest{SessionId: chat, Path: "a.txt", RunDir: abs, Offset: 11}, "", "", codes.OutOfRange},
		{"dotdot", &runnerpb.ReadFileRequest{SessionId: chat, Path: "../x", RunDir: abs}, "", "", codes.PermissionDenied},
		{"symlink escape", &runnerpb.ReadFileRequest{SessionId: chat, Path: "link.txt", RunDir: abs}, "", "", codes.PermissionDenied},
		{"missing", &runnerpb.ReadFileRequest{SessionId: chat, Path: "nope", RunDir: abs}, "", "", codes.NotFound},
		{"directory", &runnerpb.ReadFileRequest{SessionId: chat, Path: ".", RunDir: abs}, "", "", codes.FailedPrecondition},
		{"bad chat", &runnerpb.ReadFileRequest{SessionId: "../x", Path: "a.txt", RunDir: abs}, "", "", codes.InvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := cl.ReadFile(t.Context(), tt.req)
			require.NoError(t, err)
			info, data, err := readAll(t, st)
			assert.Equal(t, tt.code, status.Code(err))
			if tt.code == codes.OK {
				assert.Equal(t, tt.want, string(data))
				assert.Equal(t, tt.wantPath, info.GetPath())
			}
		})
	}
}

func TestWriteAppendStatRemove(t *testing.T) {
	cl, ws := startService(t, Options{})
	ref := func(p string, scope runnerpb.FileScope) *runnerpb.PathRef {
		return &runnerpb.PathRef{SessionId: chat, Path: p, Scope: scope, RunDir: &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, RelOrPath: ws}}
	}
	write := func(r *runnerpb.PathRef, h func(*runnerpb.WriteFileHeader), data ...string) (*runnerpb.WriteFileResponse, error) {
		st, err := cl.WriteFile(t.Context())
		require.NoError(t, err)
		hd := &runnerpb.WriteFileHeader{Ref: r}
		h(hd)
		require.NoError(t, st.Send(&runnerpb.WriteFileChunk{Payload: &runnerpb.WriteFileChunk_Header{Header: hd}}))
		for _, d := range data {
			require.NoError(t, st.Send(&runnerpb.WriteFileChunk{Payload: &runnerpb.WriteFileChunk_Data{Data: []byte(d)}}))
		}
		return st.CloseAndRecv()
	}

	// mkdir + atomic + sync, then overwrite.
	r := ref("sub/dir/f.txt", runnerpb.FileScope_FILE_SCOPE_WORKSPACE)
	resp, err := write(r, func(h *runnerpb.WriteFileHeader) { h.Mkdir, h.Atomic, h.Sync = true, true, true }, "hel", "lo")
	require.NoError(t, err)
	assert.EqualValues(t, 5, resp.GetSize())
	got, err := os.ReadFile(filepath.Join(ws, "sub/dir/f.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello", string(got))
	entries, _ := os.ReadDir(filepath.Join(ws, "sub/dir"))
	assert.Len(t, entries, 1, "no temp file left behind")

	// Without mkdir the parent must exist.
	_, err = write(ref("missing/f.txt", runnerpb.FileScope_FILE_SCOPE_WORKSPACE), func(*runnerpb.WriteFileHeader) {}, "x")
	assert.Equal(t, codes.NotFound, status.Code(err))

	// skip_if_unchanged keeps the mtime.
	before, _ := os.Stat(filepath.Join(ws, "sub/dir/f.txt"))
	time.Sleep(10 * time.Millisecond)
	_, err = write(r, func(h *runnerpb.WriteFileHeader) { h.SkipIfUnchanged = true }, "hello")
	require.NoError(t, err)
	after, _ := os.Stat(filepath.Join(ws, "sub/dir/f.txt"))
	assert.Equal(t, before.ModTime(), after.ModTime())

	// Escapes are denied.
	_, err = write(ref("../evil", runnerpb.FileScope_FILE_SCOPE_WORKSPACE), func(*runnerpb.WriteFileHeader) {}, "x")
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = write(ref("/etc/evil", runnerpb.FileScope_FILE_SCOPE_UNSPECIFIED), func(*runnerpb.WriteFileHeader) {}, "x")
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	// Append to a session-scope file, idempotent per request_id.
	sref := ref("/session/messages.jsonl", runnerpb.FileScope_FILE_SCOPE_SESSION)
	for _, id := range []string{"r1", "r1", "r2", ""} {
		_, err = cl.AppendFile(t.Context(), &runnerpb.AppendFileRequest{Ref: sref, Mkdir: true, Data: []byte("x\n"), Sync: true, RequestId: id})
		require.NoError(t, err)
	}
	got, err = os.ReadFile(filepath.Join(runnerpaths.SessionDir(chat), "messages.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, "x\nx\nx\n", string(got), "r1 retry is not applied twice")

	st, err := cl.Stat(t.Context(), &runnerpb.StatRequest{Ref: sref})
	require.NoError(t, err)
	assert.True(t, st.GetExists())
	assert.EqualValues(t, 6, st.GetSize())
	st, err = cl.Stat(t.Context(), &runnerpb.StatRequest{Ref: ref("nope", runnerpb.FileScope_FILE_SCOPE_WORKSPACE)})
	require.NoError(t, err)
	assert.False(t, st.GetExists())

	_, err = cl.Remove(t.Context(), &runnerpb.RemoveRequest{Ref: ref("sub", runnerpb.FileScope_FILE_SCOPE_WORKSPACE), Recursive: true})
	require.NoError(t, err)
	assert.NoDirExists(t, filepath.Join(ws, "sub"))
	_, err = cl.Remove(t.Context(), &runnerpb.RemoveRequest{Ref: ref("sub", runnerpb.FileScope_FILE_SCOPE_WORKSPACE)})
	require.NoError(t, err, "removing a missing file succeeds")
}

func TestAttachmentsRoundTripRangesAndCaps(t *testing.T) {
	cl, _ := startService(t, Options{})
	fs := runnerfs.NewRemote(cl, chat)

	put := func(id, name string, r io.Reader) (runnerfs.PutResult, error) {
		return fs.PutAttachment(t.Context(), id, name, r)
	}
	r1, err := put("a", "n.txt", strings.NewReader("0123456789"))
	require.NoError(t, err)
	assert.Equal(t, runnerfs.PutResult{Name: "n.txt", Size: 10}, r1)
	r2, err := put("b", "n.txt", strings.NewReader("other"))
	require.NoError(t, err)
	assert.Equal(t, "n-1.txt", r2.Name, "name collision is deduplicated")
	r3, err := put("a", "n.txt", strings.NewReader("0123456789"))
	require.NoError(t, err)
	assert.Equal(t, r1, r3, "retry with the same request_id returns the original result")
	entries, _ := os.ReadDir(runnerpaths.AttachmentsDir(chat))
	assert.Len(t, entries, 2)

	_, err = put("big", "big.bin", bytes.NewReader(make([]byte, runnerfs.MaxAttachmentSize+1)))
	assert.Equal(t, runnerfs.CodeTooLarge, runnerfs.CodeOf(err))
	assert.NoFileExists(t, filepath.Join(runnerpaths.AttachmentsDir(chat), "big.bin"))

	f, err := fs.OpenAttachment(t.Context(), "n.txt")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	assert.EqualValues(t, 10, f.Size)
	// Seek skips the open stream and fetches a range lazily.
	_, err = f.Seek(4, io.SeekStart)
	require.NoError(t, err)
	b, err := io.ReadAll(io.LimitReader(f, 3))
	require.NoError(t, err)
	assert.Equal(t, "456", string(b))
	_, err = f.Seek(-2, io.SeekEnd)
	require.NoError(t, err)
	b, err = io.ReadAll(f)
	require.NoError(t, err)
	assert.Equal(t, "89", string(b))

	_, err = fs.OpenAttachment(t.Context(), "../x")
	assert.Equal(t, runnerfs.CodeBadRequest, runnerfs.CodeOf(err))
	_, err = fs.OpenAttachment(t.Context(), "missing")
	assert.Equal(t, runnerfs.CodeNotFound, runnerfs.CodeOf(err))
}

func TestIdemCache(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	c := newIdemCache(time.Minute, 2)
	c.now = func() time.Time { return now }

	var calls atomic.Int32
	fn := func() (any, error) { calls.Add(1); return "v", nil }

	// In-flight attach: concurrent callers share one execution.
	release := make(chan struct{})
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.do("m", "id", func() (any, error) { calls.Add(1); <-release; return "v", nil })
			assert.NoError(t, err)
			assert.Equal(t, "v", v)
		}()
	}
	require.Eventually(t, func() bool { return calls.Load() == 1 }, time.Second, time.Millisecond)
	close(release)
	wg.Wait()
	assert.EqualValues(t, 1, calls.Load())

	// Cached until the TTL passes, then re-executed.
	_, _ = c.do("m", "id", fn)
	assert.EqualValues(t, 1, calls.Load())
	now = now.Add(2 * time.Minute)
	_, _ = c.do("m", "id", fn)
	assert.EqualValues(t, 2, calls.Load())

	// Methods are separate keys; empty ids are never cached; the cache is bounded.
	_, _ = c.do("other", "id", fn)
	_, _ = c.do("", "", fn)
	_, _ = c.do("", "", fn)
	assert.EqualValues(t, 5, calls.Load())
	_, _ = c.do("m", "id2", fn)
	_, _ = c.do("m", "id3", fn)
	c.mu.Lock()
	assert.LessOrEqual(t, len(c.entries), 2)
	c.mu.Unlock()

	// Errors are cached too.
	e1 := context.Canceled
	_, err := c.do("m", "err", func() (any, error) { return nil, e1 })
	assert.ErrorIs(t, err, e1)
	_, err = c.do("m", "err", fn)
	assert.ErrorIs(t, err, e1)
}

func TestRunDirIsConfined(t *testing.T) {
	cl, ws := startService(t, Options{})
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "x.txt"), []byte("secret"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(ws, "out")))

	for _, tc := range []struct {
		name, dir string
		code      codes.Code
	}{
		{"root", "/", codes.PermissionDenied},
		{"outside", outside, codes.PermissionDenied},
		{"parent of allowed", filepath.Dir(ws), codes.PermissionDenied},
		{"relative", "ws", codes.InvalidArgument},
		{"empty", "", codes.InvalidArgument},
		{"symlink out of allowed", filepath.Join(ws, "out"), codes.PermissionDenied},
		{"dotdot", ws + "/../" + filepath.Base(outside), codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, RelOrPath: tc.dir}
			st, err := cl.ReadFile(t.Context(), &runnerpb.ReadFileRequest{SessionId: chat, Path: "x.txt", RunDir: dir})
			require.NoError(t, err)
			_, _, err = readAll(t, st)
			assert.Equal(t, tc.code, status.Code(err))

			ref := &runnerpb.PathRef{SessionId: chat, Path: "x.txt", RunDir: dir, Scope: runnerpb.FileScope_FILE_SCOPE_WORKSPACE}
			_, err = cl.Remove(t.Context(), &runnerpb.RemoveRequest{Ref: ref})
			assert.Equal(t, tc.code, status.Code(err))
			_, err = cl.AppendFile(t.Context(), &runnerpb.AppendFileRequest{Ref: ref, Data: []byte("x")})
			assert.Equal(t, tc.code, status.Code(err))
		})
	}
	assert.FileExists(t, filepath.Join(outside, "x.txt"))
	got, _ := os.ReadFile(filepath.Join(outside, "x.txt"))
	assert.Equal(t, "secret", string(got))
}

func TestPutAttachmentValidatesFilename(t *testing.T) {
	cl, _ := startService(t, Options{})
	fs := runnerfs.NewRemote(cl, chat)
	for _, name := range []string{"../evil", "a/b", "..", "", `a\b`} {
		_, err := fs.PutAttachment(t.Context(), "", name, strings.NewReader("x"))
		assert.Equal(t, runnerfs.CodeBadRequest, runnerfs.CodeOf(err), name)
	}
}

func TestWriteFileSizeCap(t *testing.T) {
	cl, ws := startService(t, Options{})
	fs := runnerfs.NewRemote(cl, chat)
	big := bytes.NewReader(make([]byte, runnerfs.MaxWriteSize+1))
	for _, atomic := range []bool{false, true} {
		_, err := fs.WriteFile(t.Context(), runnerfs.WriteRequest{
			Ref: runnerfs.PathRef{RunDir: ws, Path: "big", Scope: "workspace"}, Atomic: atomic,
		}, big)
		assert.Equal(t, runnerfs.CodeTooLarge, runnerfs.CodeOf(err), "atomic=%v", atomic)
		big.Reset(make([]byte, runnerfs.MaxWriteSize+1))
		entries, _ := os.ReadDir(ws)
		assert.Empty(t, entries, "no partial or temp file left")
	}
}

func TestReadRefusesFIFO(t *testing.T) {
	cl, ws := startService(t, Options{})
	require.NoError(t, syscall.Mkfifo(filepath.Join(ws, "pipe"), 0o644))
	st, err := cl.ReadFile(t.Context(), &runnerpb.ReadFileRequest{
		SessionId: chat, Path: "pipe", RunDir: &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, RelOrPath: ws},
	})
	require.NoError(t, err)
	_, _, err = readAll(t, st)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestIdemInFlightNotEvictedAndPanicRecovered(t *testing.T) {
	t.Parallel()
	c := newIdemCache(time.Minute, 1)
	release := make(chan struct{})
	started := make(chan struct{})
	done := make(chan any)
	go func() {
		v, _ := c.do("m", "slow", func() (any, error) { close(started); <-release; return "slow", nil })
		done <- v
	}()
	<-started
	for _, id := range []string{"a", "b", "c"} {
		_, _ = c.do("m", id, func() (any, error) { return id, nil })
	}
	// A retry of the in-flight call must still attach, not re-execute.
	var reran atomic.Bool
	got := make(chan any)
	go func() {
		v, _ := c.do("m", "slow", func() (any, error) { reran.Store(true); return "again", nil })
		got <- v
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	assert.Equal(t, "slow", <-got)
	assert.Equal(t, "slow", <-done)
	assert.False(t, reran.Load())

	_, err := c.do("m", "boom", func() (any, error) { panic("kaboom") })
	assert.Equal(t, codes.Internal, status.Code(err))
	_, err = c.do("m", "boom", func() (any, error) { return nil, nil })
	assert.Equal(t, codes.Internal, status.Code(err), "panic result is cached and waiters are released")
}

func TestMkdirIsConfinedAndIdempotent(t *testing.T) {
	cl, ws := startService(t, Options{})
	outside := t.TempDir()
	ref := func(rd, p string) *runnerpb.PathRef {
		return &runnerpb.PathRef{SessionId: chat, Path: p, Scope: runnerpb.FileScope_FILE_SCOPE_WORKSPACE, RunDir: &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, RelOrPath: rd}}
	}
	for range 2 {
		_, err := cl.Mkdir(t.Context(), &runnerpb.MkdirRequest{Ref: ref(ws, "d/e"), RequestId: "m1"})
		require.NoError(t, err)
	}
	assert.DirExists(t, filepath.Join(ws, "d", "e"))

	_, err := cl.Mkdir(t.Context(), &runnerpb.MkdirRequest{Ref: ref(ws, "../esc")})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = cl.Mkdir(t.Context(), &runnerpb.MkdirRequest{Ref: ref(outside, "new")})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.NoDirExists(t, filepath.Join(outside, "new"))
	assert.NoDirExists(t, filepath.Join(filepath.Dir(ws), "esc"))
}
