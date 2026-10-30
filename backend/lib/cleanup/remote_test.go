package cleanup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/paths"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

type fakeRunner struct {
	runnerpb.RunnerServiceClient
	mu    sync.Mutex
	calls []*runnerpb.CleanupRequest
	// fail maps a chat id to the failure the runner reports for it.
	fail map[string]string
}

func (f *fakeRunner) Cleanup(_ context.Context, in *runnerpb.CleanupRequest, _ ...grpc.CallOption) (*runnerpb.CleanupResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	resp := &runnerpb.CleanupResponse{}
	for _, id := range in.GetChatIds() {
		if msg, ok := f.fail[id]; ok {
			resp.Failures = append(resp.Failures, &runnerpb.CleanupFailure{ChatId: id, Error: msg})
		}
	}
	return resp, nil
}

type fakeHub struct {
	runners map[string]*fakeRunner
	offline map[string]bool
}

func (h *fakeHub) Client(node string) (runnerpb.RunnerServiceClient, error) {
	if h.offline[node] {
		return nil, runnerhub.ErrRunnerOffline
	}
	r, ok := h.runners[node]
	if !ok {
		return nil, runnerhub.ErrRunnerOffline
	}
	return r, nil
}

func (h *fakeHub) Nodes() []runnerhub.NodeInfo {
	var out []runnerhub.NodeInfo
	for id := range h.runners {
		out = append(out, runnerhub.NodeInfo{NodeID: id, Online: !h.offline[id]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

func TestSchedulerCleansThroughRunners(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbConn := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(dbConn))
	repo := dbmodels.NewSessionRepository(dbConn)

	old := time.Now().AddDate(0, -2, 0)
	mk := func(id, node string, running bool) {
		sess := &dbmodels.Session{ChatID: id, RunnerNodeID: node}
		if running {
			sess.Agents = dbmodels.Agents{dbmodels.Agent{Name: "a", Status: dbmodels.AgentStatusRunning}}
		}
		require.NoError(t, dbConn.Create(sess).Error)
	}
	for _, s := range []struct {
		id, node string
		running  bool
	}{
		{"expOnA", "A", false}, {"expOnA2", "A", false}, {"expFailA", "A", false},
		{"expOnB", "B", false}, {"expUnbound", "", false}, {"runningOnA", "A", true},
		{"recentOnA", "A", false}, {"bad id!", "A", false},
	} {
		mk(s.id, s.node, s.running)
	}
	for _, id := range []string{"expOnA", "expOnA2", "expFailA", "expOnB", "expUnbound", "runningOnA", "bad id!"} {
		require.NoError(t, dbConn.Exec("UPDATE sessions SET updated_at = ? WHERE chat_id = ?", old, id).Error)
	}
	require.NoError(t, dbConn.Create(&dbmodels.WorkflowRun{RunID: "r1", SessionID: "recentOnA"}).Error)
	require.NoError(t, dbConn.Create(&dbmodels.WorkflowRun{RunID: "r2", SessionID: "expOnB"}).Error)

	// Legacy (pre-split) dirs on the backend host: reclaimed for deleted
	// records, kept for kept records and fresh orphans, old orphans swept.
	tmpBase, sessBase := t.TempDir(), t.TempDir()
	mkLocal := func(base, id string) string {
		d := filepath.Join(base, id)
		require.NoError(t, os.MkdirAll(d, 0o755))
		return d
	}
	gone := []string{mkLocal(tmpBase, "expOnA"), mkLocal(sessBase, "expOnA"), mkLocal(sessBase, "expUnbound"),
		mkLocal(filepath.Join(tmpBase, paths.CABundleDirName), "expOnA"), mkLocal(sessBase, "oldOrphan")}
	stay := []string{mkLocal(sessBase, "expOnB"), mkLocal(sessBase, "newOrphan"), mkLocal(tmpBase, "recentOnA")}
	require.NoError(t, os.Chtimes(gone[4], old, old))
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "f"), nil, 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(sessBase, "expOnA2")))

	a := &fakeRunner{fail: map[string]string{"expFailA": "disk on fire"}}
	b := &fakeRunner{}
	hub := &fakeHub{runners: map[string]*fakeRunner{"A": a, "B": b}, offline: map[string]bool{"B": true}}

	s, err := NewScheduler(repo, WithRunners(hub), WithTmpBase(tmpBase), WithSessionBase(sessBase))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Shutdown() })
	s.CleanExpiredSessions()

	exists := func(id string) bool {
		sess, err := repo.GetSessionMeta(id)
		require.NoError(t, err)
		return sess != nil
	}
	assert.False(t, exists("expOnA"))
	assert.False(t, exists("expOnA2"))
	assert.True(t, exists("expFailA"), "a failed chat keeps its record for the next run")
	assert.True(t, exists("expOnB"), "an offline runner keeps the record")
	assert.False(t, exists("expUnbound"))
	assert.True(t, exists("runningOnA"))
	assert.True(t, exists("recentOnA"))
	assert.False(t, exists("bad id!"), "an invalid id cannot name a runner dir; only the record goes")

	for _, d := range gone {
		assert.NoDirExists(t, d)
	}
	for _, d := range stay {
		assert.DirExists(t, d)
	}
	assert.FileExists(t, filepath.Join(outside, "f"), "symlinks are never followed")

	// A missing local root is never created.
	missing := filepath.Join(t.TempDir(), "never-created")
	s2 := &Scheduler{tmpBase: missing, sessionBase: missing}
	s2.reclaimLocal("x")
	s2.localRoots().Cleanup(runnerfs.CleanupSpec{Cutoff: time.Now()})
	assert.NoDirExists(t, missing)

	assert.Empty(t, b.calls, "offline runner is not called")
	require.Len(t, a.calls, 2)
	assert.ElementsMatch(t, []string{"expOnA", "expOnA2", "expFailA"}, a.calls[0].GetChatIds())
	assert.Zero(t, a.calls[0].GetCutoffUnixNano())
	assert.NotEmpty(t, a.calls[0].GetRequestId())

	sweep := a.calls[1]
	assert.Empty(t, sweep.GetChatIds())
	assert.NotEqual(t, a.calls[0].GetRequestId(), sweep.GetRequestId())
	assert.Greater(t, sweep.GetCutoffUnixNano(), int64(0))
	assert.WithinDuration(t, time.Now().AddDate(0, -1, 0), time.Unix(0, sweep.GetCutoffUnixNano()), time.Minute)
	// keep = every chat id still in the DB (any runner's), none of the deleted.
	assert.ElementsMatch(t, []string{"expFailA", "expOnB", "runningOnA", "recentOnA"}, sweep.GetKeepChatIds())
	require.Len(t, sweep.GetOrphanTmpSweeps(), 1)
	assert.Equal(t, "recentOnA", sweep.GetOrphanTmpSweeps()[0].GetChatId())
	assert.Equal(t, runnerpb.FileScope_FILE_SCOPE_SESSION, sweep.GetOrphanTmpSweeps()[0].GetDir().GetScope())
	assert.Equal(t, "workflows", sweep.GetOrphanTmpSweeps()[0].GetDir().GetRelOrPath())

	// The runner comes back: the next run cleans the retried chats.
	hub.offline["B"] = false
	a.fail = nil
	s.CleanExpiredSessions()
	assert.False(t, exists("expOnB"))
	assert.False(t, exists("expFailA"))
	require.NotEmpty(t, b.calls)
	assert.Equal(t, []string{"expOnB"}, b.calls[0].GetChatIds())
}

func TestSchedulerRunnerRPCErrorKeepsRecord(t *testing.T) {
	// The scheduler also sweeps the backend host's legacy dirs: keep that
	// inside temp dirs.
	t.Setenv("HOME", t.TempDir())
	dbConn := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(dbConn))
	repo := dbmodels.NewSessionRepository(dbConn)
	require.NoError(t, dbConn.Create(&dbmodels.Session{ChatID: "c1", RunnerNodeID: "A"}).Error)
	require.NoError(t, dbConn.Exec("UPDATE sessions SET updated_at = ?", time.Now().AddDate(0, -3, 0)).Error)

	hub := &errHub{err: errors.New("rpc broke")}
	s, err := NewScheduler(repo, WithRunners(hub), WithTmpBase(t.TempDir()), WithSessionBase(t.TempDir()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Shutdown() })
	s.CleanExpiredSessions()
	sess, err := repo.GetSessionMeta("c1")
	require.NoError(t, err)
	assert.NotNil(t, sess)
}

type errClient struct {
	runnerpb.RunnerServiceClient
	err error
}

func (c errClient) Cleanup(context.Context, *runnerpb.CleanupRequest, ...grpc.CallOption) (*runnerpb.CleanupResponse, error) {
	return nil, c.err
}

type errHub struct{ err error }

func (h *errHub) Client(string) (runnerpb.RunnerServiceClient, error) {
	return errClient{err: h.err}, nil
}
func (h *errHub) Nodes() []runnerhub.NodeInfo {
	return []runnerhub.NodeInfo{{NodeID: "A", Online: true}}
}

func TestSchedulerDropsSessionsOfUnknownRunnerAfterGrace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dbConn := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(dbConn))
	repo := dbmodels.NewSessionRepository(dbConn)
	for _, id := range []string{"longGone", "recentlyExpired", "knownOffline"} {
		node := "Z" // never known to the hub
		if id == "knownOffline" {
			node = "B"
		}
		require.NoError(t, dbConn.Create(&dbmodels.Session{ChatID: id, RunnerNodeID: node}).Error)
	}
	set := func(id string, at time.Time) {
		require.NoError(t, dbConn.Exec("UPDATE sessions SET updated_at = ? WHERE chat_id = ?", at, id).Error)
	}
	set("longGone", time.Now().AddDate(0, 0, -120))
	set("recentlyExpired", time.Now().AddDate(0, 0, -40))
	set("knownOffline", time.Now().AddDate(0, 0, -120))
	require.NoError(t, dbConn.Create(&dbmodels.FinalizedRun{RunID: "old", FinalizedAt: time.Now().AddDate(0, 0, -120)}).Error)
	require.NoError(t, dbConn.Create(&dbmodels.FinalizedRun{RunID: "new", FinalizedAt: time.Now()}).Error)

	hub := &fakeHub{runners: map[string]*fakeRunner{"B": {}}, offline: map[string]bool{"B": true}}
	s, err := NewScheduler(repo, WithRunners(hub), WithTmpBase(t.TempDir()), WithSessionBase(t.TempDir()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Shutdown() })
	s.CleanExpiredSessions()

	exists := func(id string) bool {
		sess, err := repo.GetSessionMeta(id)
		require.NoError(t, err)
		return sess != nil
	}
	assert.False(t, exists("longGone"))
	assert.True(t, exists("recentlyExpired"))
	assert.True(t, exists("knownOffline"), "a known offline runner may come back")

	fr := dbmodels.NewFinalizedRunRepository(dbConn)
	done, err := fr.IsFinalized(t.Context(), "old")
	require.NoError(t, err)
	assert.False(t, done, "old finalized ids are pruned")
	done, err = fr.IsFinalized(t.Context(), "new")
	require.NoError(t, err)
	assert.True(t, done)
}
