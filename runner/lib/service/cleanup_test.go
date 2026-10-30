package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/llms"
	runnerpaths "github.com/AgentDrasil/asgard/pkg/paths/runner"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// mkChatDirs creates the three per-chat dirs of id, each holding a file.
func mkChatDirs(t *testing.T, id string) []string {
	t.Helper()
	dirs := []string{runnerpaths.SessionTmpDir(id), runnerpaths.SessionDir(id), runnerpaths.CABundleDir(id)}
	for _, d := range dirs {
		put(t, filepath.Join(d, "f.txt"), "x")
	}
	return dirs
}

func age(t *testing.T, dir string, when time.Time) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
		require.NoError(t, err)
		return os.Chtimes(p, when, when)
	}))
}

func TestCleanupRemovesChatDirsOnly(t *testing.T) {
	cl, _ := startService(t, Options{})
	a := mkChatDirs(t, "chatA")
	b := mkChatDirs(t, "chatB")

	resp, err := cl.Cleanup(context.Background(), &runnerpb.CleanupRequest{ChatIds: []string{"chatA", "nochat"}})
	require.NoError(t, err)
	assert.Empty(t, resp.GetFailures())
	assert.EqualValues(t, 3, resp.GetRemoved())
	for _, d := range a {
		assert.False(t, exists(d), d)
	}
	for _, d := range b {
		assert.True(t, exists(d), d)
	}
}

func TestCleanupRejectsBadChatIDs(t *testing.T) {
	cl, _ := startService(t, Options{})
	victim := filepath.Join(runnerpaths.DataDir(), "victim")
	put(t, filepath.Join(victim, "keep.txt"), "x")
	put(t, filepath.Join(runnerpaths.TmpDir(), "..", "victim2", "keep.txt"), "x")
	mkChatDirs(t, "good")

	bad := []string{"../victim", "..", ".", "", "a/b", "/etc", strings.Repeat("a", 65), "../../victim2"}
	resp, err := cl.Cleanup(context.Background(), &runnerpb.CleanupRequest{ChatIds: append(bad, "good")})
	require.NoError(t, err)
	failed := map[string]bool{}
	for _, f := range resp.GetFailures() {
		failed[f.GetChatId()] = true
	}
	for _, b := range bad {
		assert.True(t, failed[b], "bad id %q must be reported", b)
	}
	assert.False(t, failed["good"])
	assert.True(t, exists(filepath.Join(victim, "keep.txt")))
	assert.True(t, exists(filepath.Join(runnerpaths.DataDir(), "victim2", "keep.txt")))
	assert.False(t, exists(runnerpaths.SessionDir("good")))
}

func TestCleanupDoesNotFollowSymlinks(t *testing.T) {
	cl, _ := startService(t, Options{})
	outside := t.TempDir()
	put(t, filepath.Join(outside, "secret.txt"), "x")

	// The chat dir itself is a symlink out; another one hides a link inside.
	require.NoError(t, os.MkdirAll(runnerpaths.TmpDir(), 0o755))
	require.NoError(t, os.Symlink(outside, runnerpaths.SessionTmpDir("linked")))
	require.NoError(t, os.MkdirAll(runnerpaths.SessionDir("inner"), 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(runnerpaths.SessionDir("inner"), "escape")))

	resp, err := cl.Cleanup(context.Background(), &runnerpb.CleanupRequest{ChatIds: []string{"linked", "inner"}})
	require.NoError(t, err)
	assert.Empty(t, resp.GetFailures())
	assert.False(t, exists(runnerpaths.SessionTmpDir("linked")))
	assert.False(t, exists(runnerpaths.SessionDir("inner")))
	assert.True(t, exists(filepath.Join(outside, "secret.txt")), "symlink target must survive")
}

func TestCleanupIdempotentByRequestID(t *testing.T) {
	cl, _ := startService(t, Options{})
	mkChatDirs(t, "c1")
	req := &runnerpb.CleanupRequest{ChatIds: []string{"c1"}, RequestId: "req-1"}
	first, err := cl.Cleanup(context.Background(), req)
	require.NoError(t, err)
	assert.EqualValues(t, 3, first.GetRemoved())

	// The chat is recreated; a retry of the same request returns the cached
	// result without touching it, a new request id acts again.
	mkChatDirs(t, "c1")
	retry, err := cl.Cleanup(context.Background(), req)
	require.NoError(t, err)
	assert.EqualValues(t, 3, retry.GetRemoved())
	assert.True(t, exists(runnerpaths.SessionDir("c1")))

	req2 := &runnerpb.CleanupRequest{ChatIds: []string{"c1"}, RequestId: "req-2"}
	_, err = cl.Cleanup(context.Background(), req2)
	require.NoError(t, err)
	assert.False(t, exists(runnerpaths.SessionDir("c1")))
}

func TestCleanupOrphanSweepHonorsKeepAndCutoff(t *testing.T) {
	cl, _ := startService(t, Options{})
	old := time.Now().Add(-48 * time.Hour)
	cutoff := time.Now().Add(-24 * time.Hour)

	for _, id := range []string{"oldKept", "oldOrphan", "newOrphan", "oldWithNewFile"} {
		mkChatDirs(t, id)
	}
	for _, id := range []string{"oldKept", "oldOrphan"} {
		for _, d := range []string{runnerpaths.SessionTmpDir(id), runnerpaths.SessionDir(id), runnerpaths.CABundleDir(id)} {
			age(t, d, old)
		}
	}
	// A recent file inside an old dir keeps the whole dir.
	for _, d := range []string{runnerpaths.SessionTmpDir("oldWithNewFile"), runnerpaths.SessionDir("oldWithNewFile"), runnerpaths.CABundleDir("oldWithNewFile")} {
		age(t, d, old)
		now := time.Now()
		require.NoError(t, os.Chtimes(filepath.Join(d, "f.txt"), now, now))
	}
	// A plain file in the roots and a symlink to an old dir are not swept.
	outside := t.TempDir()
	put(t, filepath.Join(outside, "f.txt"), "x")
	age(t, outside, old)
	require.NoError(t, os.Symlink(outside, filepath.Join(runnerpaths.TmpDir(), "linkToOutside")))
	put(t, filepath.Join(runnerpaths.TmpDir(), "plain.txt"), "x")

	resp, err := cl.Cleanup(context.Background(), &runnerpb.CleanupRequest{
		CutoffUnixNano: cutoff.UnixNano(), KeepChatIds: []string{"oldKept"},
	})
	require.NoError(t, err)
	assert.Empty(t, resp.GetFailures())
	assert.EqualValues(t, 3, resp.GetRemoved())

	assert.False(t, exists(runnerpaths.SessionTmpDir("oldOrphan")))
	assert.False(t, exists(runnerpaths.SessionDir("oldOrphan")))
	assert.False(t, exists(runnerpaths.CABundleDir("oldOrphan")))
	for _, id := range []string{"oldKept", "newOrphan", "oldWithNewFile"} {
		assert.True(t, exists(runnerpaths.SessionTmpDir(id)), id)
		assert.True(t, exists(runnerpaths.SessionDir(id)), id)
		assert.True(t, exists(runnerpaths.CABundleDir(id)), id)
	}
	// The CA container itself is never swept as an orphan of tmp.
	assert.True(t, exists(runnerpaths.CABundleBaseDir()))
	assert.True(t, exists(filepath.Join(outside, "f.txt")))
	assert.True(t, exists(filepath.Join(runnerpaths.TmpDir(), "plain.txt")))
}

func TestCleanupNoSweepWithoutCutoff(t *testing.T) {
	cl, _ := startService(t, Options{})
	mkChatDirs(t, "old")
	age(t, runnerpaths.SessionDir("old"), time.Now().Add(-1000*time.Hour))
	resp, err := cl.Cleanup(context.Background(), &runnerpb.CleanupRequest{})
	require.NoError(t, err)
	assert.Zero(t, resp.GetRemoved())
	assert.True(t, exists(runnerpaths.SessionDir("old")))
}

func TestCleanupReportsFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	cl, _ := startService(t, Options{})
	mkChatDirs(t, "stuck")
	mkChatDirs(t, "fine")
	locked := filepath.Join(runnerpaths.SessionDir("stuck"), "locked")
	put(t, filepath.Join(locked, "f.txt"), "x")
	require.NoError(t, os.Chmod(locked, 0o500))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	resp, err := cl.Cleanup(context.Background(), &runnerpb.CleanupRequest{ChatIds: []string{"stuck", "fine"}})
	require.NoError(t, err)
	require.Len(t, resp.GetFailures(), 1)
	assert.Equal(t, "stuck", resp.GetFailures()[0].GetChatId())
	assert.NotEmpty(t, resp.GetFailures()[0].GetError())
	assert.False(t, exists(runnerpaths.SessionDir("fine")))
}

func TestCleanupOrphanTmpSweeps(t *testing.T) {
	cl, _ := startService(t, Options{})
	wf := filepath.Join(runnerpaths.SessionDir("c1"), "workflows", "run1")
	put(t, filepath.Join(wf, ".dag_spec.yaml.123.tmp"), "x")
	put(t, filepath.Join(wf, "nodes", ".n1.log.9.tmp"), "x")
	put(t, filepath.Join(wf, "dag_spec.yaml"), "x")
	put(t, filepath.Join(wf, "other.tmp"), "x") // no leading dot: kept
	put(t, filepath.Join(runnerpaths.SessionDir("c1"), ".top.tmp"), "x")
	outside := t.TempDir()
	put(t, filepath.Join(outside, ".x.tmp"), "x")
	require.NoError(t, os.Symlink(outside, filepath.Join(wf, "link")))

	sw := func(chat string, scope runnerpb.FileScope, rel string) *runnerpb.OrphanTmpSweep {
		return &runnerpb.OrphanTmpSweep{ChatId: chat, Dir: &runnerpb.DirRef{Scope: scope, RelOrPath: rel}}
	}
	resp, err := cl.Cleanup(context.Background(), &runnerpb.CleanupRequest{OrphanTmpSweeps: []*runnerpb.OrphanTmpSweep{
		sw("c1", runnerpb.FileScope_FILE_SCOPE_SESSION, "workflows"),
		sw("missing", runnerpb.FileScope_FILE_SCOPE_SESSION, "workflows"),
		sw("c1", runnerpb.FileScope_FILE_SCOPE_SESSION, "workflows/link"), // symlinked dir: not entered
		sw("c1", runnerpb.FileScope_FILE_SCOPE_SESSION, "../c2"),
		sw("../c1", runnerpb.FileScope_FILE_SCOPE_SESSION, "workflows"),
	}})
	require.NoError(t, err)
	failed := map[string]int{}
	for _, f := range resp.GetFailures() {
		failed[f.GetChatId()]++
	}
	assert.Equal(t, 1, failed["../c1"])
	assert.GreaterOrEqual(t, failed["c1"], 1) // the ../c2 escape (a symlinked dir may add one)
	assert.False(t, exists(filepath.Join(wf, ".dag_spec.yaml.123.tmp")))
	assert.False(t, exists(filepath.Join(wf, "nodes", ".n1.log.9.tmp")))
	assert.True(t, exists(filepath.Join(wf, "dag_spec.yaml")))
	assert.True(t, exists(filepath.Join(wf, "other.tmp")))
	assert.True(t, exists(filepath.Join(runnerpaths.SessionDir("c1"), ".top.tmp")), "outside the swept dir")
	assert.True(t, exists(filepath.Join(outside, ".x.tmp")), "symlink target must survive")

	// Absolute refs are refused outright.
	_, err = cl.Cleanup(context.Background(), &runnerpb.CleanupRequest{OrphanTmpSweeps: []*runnerpb.OrphanTmpSweep{
		sw("c1", runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, outside),
	}})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.True(t, exists(filepath.Join(outside, ".x.tmp")))
}

func TestCleanupRejectsNegativeCutoff(t *testing.T) {
	cl, _ := startService(t, Options{})
	_, err := cl.Cleanup(context.Background(), &runnerpb.CleanupRequest{CutoffUnixNano: -1})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestQueryQuotaMapping(t *testing.T) {
	in := map[string][]llms.ModelUsage{
		"opencode": {{
			Model: "zai/glm-5.3", Remaining: 0.4, RefreshDate: 1234,
			Limits: []llms.QuotaLimit{{Name: "5h", Remaining: 0.4, RefreshDate: 99}, {Name: "weekly", Remaining: 0.9}},
		}},
		"simplest": {{
			Model: "deepseek/chat", Remaining: 1,
			Balance: &llms.AccountBalance{IsAvailable: true, BalanceInfos: []llms.BalanceInfo{{Currency: "CNY", TotalBalance: "10.5", GrantedBalance: "1", ToppedUpBalance: "9.5"}}},
		}},
		"empty": {},
	}
	cl, _ := startService(t, Options{Quota: func(context.Context) (map[string][]llms.ModelUsage, error) { return in, nil }})

	resp, err := cl.QueryQuota(context.Background(), &runnerpb.QueryQuotaRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetUsages(), 2)
	oc := resp.GetUsages()[0]
	assert.Equal(t, "opencode", oc.GetCli())
	assert.Equal(t, "zai/glm-5.3", oc.GetModel())
	assert.InDelta(t, 0.4, oc.GetRemaining(), 1e-9)
	assert.EqualValues(t, 1234, oc.GetRefreshDate())
	assert.True(t, oc.GetEnabled())
	require.Len(t, oc.GetLimits(), 2)
	assert.Equal(t, "5h", oc.GetLimits()[0].GetName())
	assert.EqualValues(t, 99, oc.GetLimits()[0].GetRefreshDate())
	assert.Nil(t, oc.GetBalance())
	bal := resp.GetUsages()[1].GetBalance()
	require.NotNil(t, bal)
	assert.True(t, bal.GetIsAvailable())
	assert.Equal(t, "10.5", bal.GetBalanceInfos()[0].GetTotalBalance())
	assert.Equal(t, "9.5", bal.GetBalanceInfos()[0].GetToppedUpBalance())

	// Round trip back into the backend's shape.
	back := runnerfs.QuotaFromProto(resp.GetUsages())
	assert.Equal(t, in["opencode"], back["opencode"])
	assert.Equal(t, in["simplest"][0].Balance, back["simplest"][0].Balance)
	assert.NotContains(t, back, "empty")

	// Target filter.
	resp, err = cl.QueryQuota(context.Background(), &runnerpb.QueryQuotaRequest{Targets: []*runnerpb.Target{{Cli: "simplest"}}})
	require.NoError(t, err)
	require.Len(t, resp.GetUsages(), 1)
	assert.Equal(t, "simplest", resp.GetUsages()[0].GetCli())
	resp, err = cl.QueryQuota(context.Background(), &runnerpb.QueryQuotaRequest{Targets: []*runnerpb.Target{{Cli: "opencode", Model: "nope"}}})
	require.NoError(t, err)
	assert.Empty(t, resp.GetUsages())
}

func TestQueryQuotaError(t *testing.T) {
	cl, _ := startService(t, Options{Quota: func(context.Context) (map[string][]llms.ModelUsage, error) { return nil, errors.New("boom") }})
	_, err := cl.QueryQuota(context.Background(), &runnerpb.QueryQuotaRequest{})
	assert.Equal(t, codes.Internal, status.Code(err))
}
