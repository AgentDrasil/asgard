package exec

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/AgentDrasil/asgard/backend/lib/bwrap"
	"github.com/AgentDrasil/asgard/backend/lib/worktree"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

func gitIn(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := osexec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append([]string{
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	}, env...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

type wtEnv struct {
	repo, root string
	agent      *agentspec.Agent
}

func newWTEnv(t *testing.T) wtEnv {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("ASGARD_ROOT", filepath.Join(base, "asgard"))
	t.Setenv("ASGARD_SESSION_DATA", filepath.Join(base, "session-data"))
	repo := filepath.Join(base, "src", "repo")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	gitIn(t, repo, nil, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644))
	gitIn(t, repo, nil, "add", ".")
	gitIn(t, repo, nil, "commit", "-m", "init")

	agent := testAgent("a", "h1", "")
	agent.Config.RunDirs = []string{filepath.Join(base, "src")}
	agent.Config.Worktree = agentspec.WorktreeConfig{Enabled: true, Root: filepath.Join(base, "wt")}
	return wtEnv{repo: repo, root: filepath.Join(base, "wt"), agent: agent}
}

func TestPrepareWorktree(t *testing.T) {
	e := newWTEnv(t)

	eff, plan, err := prepareWorktree(t.Context(), e.agent, "c1", e.repo, false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(e.root, "repo-c1"), plan.Path)
	assert.Equal(t, []string{plan.Path}, eff.Config.RunDirs, "the main checkout is not a run dir of the worktree run")
	require.NotNil(t, eff.Config.WorktreeRun)
	assert.Equal(t, plan.ObjectsDir, eff.Config.WorktreeRun.ObjectsDir)
	assert.DirExists(t, plan.Path)
	// The caller's agent is untouched.
	assert.Nil(t, e.agent.Config.WorktreeRun)
	assert.Equal(t, []string{filepath.Dir(e.repo)}, e.agent.Config.RunDirs)
}

func TestPrepareWorktree_Refusals(t *testing.T) {
	t.Run("agent does not offer worktrees", func(t *testing.T) {
		e := newWTEnv(t)
		e.agent.Config.Worktree = agentspec.WorktreeConfig{}
		_, _, err := prepareWorktree(t.Context(), e.agent, "c1", e.repo, false)
		require.ErrorContains(t, err, "does not offer worktrees")
	})
	t.Run("dir outside the run dirs", func(t *testing.T) {
		e := newWTEnv(t)
		other := t.TempDir()
		_, _, err := prepareWorktree(t.Context(), e.agent, "c1", other, false)
		require.ErrorContains(t, err, "not allowed")
	})
	t.Run("not a git repo", func(t *testing.T) {
		e := newWTEnv(t)
		plain := filepath.Join(filepath.Dir(e.repo), "plain")
		require.NoError(t, os.MkdirAll(plain, 0o755))
		_, _, err := prepareWorktree(t.Context(), e.agent, "c1", plain, false)
		require.ErrorIs(t, err, worktree.ErrNotGitRepo)
	})
	t.Run("rw mount over the checkout", func(t *testing.T) {
		e := newWTEnv(t)
		e.agent.Config.MountDirs.ReadWrite = []string{filepath.Dir(e.repo)}
		_, _, err := prepareWorktree(t.Context(), e.agent, "c1", e.repo, false)
		require.ErrorContains(t, err, "expose the main checkout")
	})
	t.Run("symlinked run dir", func(t *testing.T) {
		e := newWTEnv(t)
		link := filepath.Join(filepath.Dir(e.repo), "link")
		require.NoError(t, os.Symlink(e.repo, link))
		_, _, err := prepareWorktree(t.Context(), e.agent, "c1", link, false)
		require.ErrorContains(t, err, "real path")
	})
	t.Run("bad chat id", func(t *testing.T) {
		e := newWTEnv(t)
		_, _, err := prepareWorktree(t.Context(), e.agent, "../x", e.repo, false)
		require.ErrorContains(t, err, "invalid chat id")
	})
}

// holdStream is a HoldMergeLock stream: it delivers grant (if set) and then
// blocks until the call context ends or the test breaks it.
type holdStream struct {
	grpc.ClientStream
	ctx    context.Context
	grant  bool
	sent   bool
	broken chan struct{}
}

func (h *holdStream) Recv() (*runnerpb.HoldMergeLockResponse, error) {
	if !h.sent {
		h.sent = true
		return &runnerpb.HoldMergeLockResponse{Granted: h.grant}, nil
	}
	select {
	case <-h.ctx.Done():
		return nil, status.FromContextError(h.ctx.Err()).Err()
	case <-h.broken:
		return nil, status.Error(codes.Unavailable, "connection lost")
	}
}

// mergeRig is a callback rig whose statusServer has a real worktree.
type mergeRig struct {
	*callbackRig
	env  wtEnv
	plan worktree.Plan

	mu       sync.Mutex
	holds    []string // repo dirs locked
	released chan struct{}
}

func newMergeRig(t *testing.T) *mergeRig {
	t.Helper()
	e := newWTEnv(t)
	_, plan, err := prepareWorktree(t.Context(), e.agent, "chat-1", e.repo, false)
	require.NoError(t, err)

	m := &mergeRig{env: e, plan: plan, released: make(chan struct{}, 1)}
	fake := &fakeCallbacks{holdHook: func(ctx context.Context, req *runnerpb.HoldMergeLockRequest) (grpc.ServerStreamingClient[runnerpb.HoldMergeLockResponse], error) {
		m.mu.Lock()
		m.holds = append(m.holds, req.GetRepoDir())
		m.mu.Unlock()
		go func() {
			<-ctx.Done()
			m.released <- struct{}{}
		}()
		return &holdStream{ctx: ctx, grant: true, broken: make(chan struct{})}, nil
	}}
	m.callbackRig = newCallbackRig(t, context.Background(), fake, nil)
	m.srv.worktree = &plan
	return m
}

func (m *mergeRig) commit(t *testing.T, name string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(m.plan.Path, name), []byte(name), 0o644))
	env := []string{"GIT_OBJECT_DIRECTORY=" + m.plan.ObjectsDir, "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(m.plan.GitDir(), "objects")}
	gitIn(t, m.plan.Path, env, "add", ".")
	gitIn(t, m.plan.Path, env, "commit", "-m", name)
}

func (m *mergeRig) merge(t *testing.T) (int, sandboxsock.MergeResponse) {
	t.Helper()
	code, body := m.post(t, sandboxsock.PathMerge, struct{}{})
	var resp sandboxsock.MergeResponse
	require.NoError(t, json.Unmarshal(body, &resp), string(body))
	return code, resp
}

func TestMergeEndpoint_MergesUnderTheLock(t *testing.T) {
	m := newMergeRig(t)
	m.commit(t, "b.txt")

	code, resp := m.merge(t)
	require.Equal(t, http.StatusOK, code)
	assert.True(t, resp.OK)
	assert.Equal(t, "merged", resp.Status)
	assert.Contains(t, resp.Message, "Merged into main")
	assert.FileExists(t, filepath.Join(m.env.repo, "b.txt"))

	m.mu.Lock()
	assert.Equal(t, []string{m.plan.RepoDir}, m.holds, "the lock is taken on the main checkout")
	m.mu.Unlock()
	<-m.released // the stream is cancelled once the merge is done
}

func TestMergeEndpoint_GuidanceForTheAgent(t *testing.T) {
	m := newMergeRig(t)
	m.commit(t, "b.txt")
	require.NoError(t, os.WriteFile(filepath.Join(m.env.repo, "o.txt"), []byte("o"), 0o644))
	gitIn(t, m.env.repo, nil, "add", ".")
	gitIn(t, m.env.repo, nil, "commit", "-m", "other")

	code, resp := m.merge(t)
	require.Equal(t, http.StatusOK, code)
	assert.False(t, resp.OK)
	assert.Equal(t, "main_moved", resp.Status)
	assert.Contains(t, resp.Message, "git rebase main")
	<-m.released
}

func TestMergeEndpoint_NoWorktree(t *testing.T) {
	rig := newCallbackRig(t, context.Background(), &fakeCallbacks{}, nil)
	code, body := rig.post(t, sandboxsock.PathMerge, struct{}{})
	require.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, string(body), "no worktree")
}

func TestMergeEndpoint_LockNotGranted(t *testing.T) {
	m := newMergeRig(t)
	m.fake.holdHook = func(ctx context.Context, _ *runnerpb.HoldMergeLockRequest) (grpc.ServerStreamingClient[runnerpb.HoldMergeLockResponse], error) {
		return nil, status.Error(codes.Unavailable, "backend down")
	}
	m.commit(t, "b.txt")

	code, resp := m.merge(t)
	require.Equal(t, http.StatusBadGateway, code)
	assert.False(t, resp.OK)
	assert.Contains(t, resp.Message, "Could not take the lock")
	assert.NoFileExists(t, filepath.Join(m.env.repo, "b.txt"), "no merge without the lock")
}

// A subagent shares the worktree of the chat that owns it: the path comes from
// the owner's chat id, and a read-only run gets it without the owner's
// writable git paths.
func TestPrepareWorktree_SharedAndReadonly(t *testing.T) {
	e := newWTEnv(t)
	_, ownerPlan, err := prepareWorktree(t.Context(), e.agent, "owner", e.repo, false)
	require.NoError(t, err)

	t.Run("a writable child reuses the owner's worktree", func(t *testing.T) {
		eff, plan, err := prepareWorktree(t.Context(), e.agent, "owner", e.repo, false)
		require.NoError(t, err)
		assert.Equal(t, ownerPlan.Path, plan.Path)
		require.NotNil(t, eff.Config.WorktreeRun)
		assert.Equal(t, []string{ownerPlan.Path}, eff.Config.RunDirs)
	})
	t.Run("read-only gets no writable git paths", func(t *testing.T) {
		eff, plan, err := prepareWorktree(t.Context(), e.agent, "owner", e.repo, true)
		require.NoError(t, err)
		assert.Equal(t, ownerPlan.Path, plan.Path)
		assert.Nil(t, eff.Config.WorktreeRun)
		assert.Contains(t, eff.Config.MountDirs.ReadOnly, plan.GitDir())
		assert.Equal(t, []string{ownerPlan.Path}, eff.Config.RunDirs)
	})
}

// A read-only StartRun turns the agent into a lock_mode: readonly one, so the
// worktree or checkout is bound read-only in the command sandbox too.
func TestApplyOverlay_ReadonlyRun(t *testing.T) {
	e := newWTEnv(t)
	got := applyOverlay(e.agent, &runnerpb.StartRun{Readonly: true, MountDirsReadwrite: []string{"/rw"}})
	assert.True(t, got.Config.IsReadOnly())
	assert.Empty(t, got.Config.MountDirs.ReadWrite)
	assert.False(t, e.agent.Config.IsReadOnly(), "the shared agent definition is left alone")

	cmd, err := bwrap.CommandForAgentCommandExec(&got.Config, e.repo, "", "sock", "chat-ro", "", false)
	require.NoError(t, err)
	var roBound, rwBound bool
	for i := 0; i+2 < len(cmd.Args); i++ {
		if cmd.Args[i+1] == e.repo && cmd.Args[i+2] == e.repo {
			roBound = roBound || cmd.Args[i] == "--ro-bind"
			rwBound = rwBound || cmd.Args[i] == "--bind"
		}
	}
	assert.True(t, roBound, "checkout is bound read-only")
	assert.False(t, rwBound, "checkout is never bound writable")

	rw := applyOverlay(e.agent, &runnerpb.StartRun{})
	assert.False(t, rw.Config.IsReadOnly())
}
