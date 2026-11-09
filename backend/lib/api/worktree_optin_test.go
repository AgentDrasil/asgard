package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moznion/go-optional"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/agents/dispatch"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/dirlock"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

func worktreeAgent() *agentspec.Agent {
	return &agentspec.Agent{
		Config: agentspec.AgentConfig{
			ID: "wta", Name: "WT Agent", Description: "d",
			CLI:      []agentspec.CLITarget{{CLI: "opencode", Model: "m1"}},
			RunDirs:  []string{"/src"},
			Worktree: agentspec.WorktreeConfig{Enabled: true, Root: "/wt"},
		},
		Hash: "h",
	}
}

func plainAgent() *agentspec.Agent {
	a := worktreeAgent()
	a.Config.ID, a.Config.Name = "plain", "Plain Agent"
	a.Config.Worktree = agentspec.WorktreeConfig{}
	return a
}

func TestWorktreeOptInError(t *testing.T) {
	t.Parallel()

	assert.Empty(t, worktreeOptInError(worktreeAgent()))
	assert.Contains(t, worktreeOptInError(plainAgent()), "does not offer")
	assert.Contains(t, worktreeOptInError(nil), "not found")
	wf := worktreeAgent()
	wf.Config.Type = "workflow"
	assert.Contains(t, worktreeOptInError(wf), "workflow")
}

func newWorktreeAPIServer(t *testing.T) (*Server, *dbmodels.SessionRepository) {
	t.Helper()
	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	repo := dbmodels.NewSessionRepository(testDB)
	dir := t.TempDir()
	repo.SetSessionDirFunc(func(chatID string) string { return filepath.Join(dir, chatID) })
	server := &Server{conf: &config.Config{Host: "http://localhost:8080"}, repo: repo, agents: []*agentspec.Agent{worktreeAgent(), plainAgent()}}
	server.mux = server.buildMuxLocked()
	return server, repo
}

func createSessionRequest(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	return rr
}

func TestCreateSession_WorktreeOptIn(t *testing.T) {
	server, repo := newWorktreeAPIServer(t)

	t.Run("agent that offers worktrees", func(t *testing.T) {
		rr := createSessionRequest(t, server, `{"currentAgent":"wta","runDir":"/src/repo","worktree":true}`)
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		var created ChatSession
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
		assert.True(t, created.Worktree)

		sess, err := repo.GetSessionMeta(created.ChatID)
		require.NoError(t, err)
		assert.True(t, sess.Worktree, "the choice is stored with the chat")

		get := httptest.NewRecorder()
		server.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ChatID, nil))
		var fetched ChatSession
		require.NoError(t, json.Unmarshal(get.Body.Bytes(), &fetched))
		assert.True(t, fetched.Worktree)
	})

	t.Run("not requested stays off", func(t *testing.T) {
		rr := createSessionRequest(t, server, `{"currentAgent":"wta","runDir":"/src/repo"}`)
		require.Equal(t, http.StatusCreated, rr.Code)
		var created ChatSession
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
		assert.False(t, created.Worktree)
	})

	t.Run("agent without worktree support is refused", func(t *testing.T) {
		rr := createSessionRequest(t, server, `{"currentAgent":"plain","runDir":"/src/repo","worktree":true}`)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "does not offer")
	})

	t.Run("unknown agent is refused", func(t *testing.T) {
		rr := createSessionRequest(t, server, `{"currentAgent":"ghost","runDir":"/src/repo","worktree":true}`)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})
}

func TestUpdateAgentSession_WorktreeOnlyBeforeFirstMessage(t *testing.T) {
	_, repo := newWorktreeAPIServer(t)

	require.NoError(t, repo.UpdateAgentSession("chat-a", "wta", "", "", optional.None[string](), dbmodels.SessionWorktreeOption(true)))
	sess, err := repo.GetSessionMeta("chat-a")
	require.NoError(t, err)
	assert.True(t, sess.Worktree)

	// A chat that already has history is never switched over afterwards.
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: "chat-b", CurrentAgent: "wta", MessageCount: 3}))
	require.NoError(t, repo.UpdateAgentSession("chat-b", "wta", "", "", optional.None[string](), dbmodels.SessionWorktreeOption(true)))
	sess, err = repo.GetSessionMeta("chat-b")
	require.NoError(t, err)
	assert.False(t, sess.Worktree)

	// Nor is the opt-in ever withdrawn by a later request.
	require.NoError(t, repo.UpdateAgentSession("chat-a", "wta", "", "", optional.None[string](), dbmodels.SessionWorktreeOption(false)))
	sess, err = repo.GetSessionMeta("chat-a")
	require.NoError(t, err)
	assert.True(t, sess.Worktree)
}

func TestAgentsListAdvertisesWorktrees(t *testing.T) {
	server, _ := newWorktreeAPIServer(t)
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	var infos []AgentInfo
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &infos))
	got := map[string]bool{}
	for _, i := range infos {
		got[i.ID] = i.Worktree
	}
	assert.Equal(t, map[string]bool{"wta": true, "plain": false}, got)
}

// worktreeRunFixture runs one chat through Execute with a stub dispatcher and
// reports the request and the directories locked while it ran.
func worktreeRunFixture(t *testing.T, agent *agentspec.Agent, sessionWorktree bool) (dispatch.Request, []string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repo := newQuotaTestRepo(t)
	server := newQuotaTestServer(t, repo)
	server.dirLocks = dirlock.NewManager(nil)
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: "chat-wt", CurrentAgent: agent.Config.ID, Worktree: sessionWorktree}))

	var locked []string
	server.dispatcher = &stubDispatcher{step: func(_ int, req dispatch.Request) (dispatch.Result, error) {
		locked = server.dirLocks.LockedDirsForChat("chat-wt")
		return dispatch.Result{Output: []byte(`{"last_content":"ok"}`), Target: agent.Config.CLI[0]}, nil
	}}
	exec := NewSingleAgentExecutor(agent, server.conf, repo, server, nil)
	_, err := exec.Execute(context.Background(), SingleAgentRunParams{ChatID: "chat-wt", Prompt: "p", RunDir: "/src/repo"})
	require.NoError(t, err)
	d := server.dispatcher.(*stubDispatcher)
	require.Len(t, d.requests, 1)
	return d.requests[0], locked
}

func TestExecute_WorktreeChatLocksTheWorktreeNotTheCheckout(t *testing.T) {
	req, locked := worktreeRunFixture(t, worktreeAgent(), true)

	assert.True(t, req.Worktree)
	assert.Equal(t, "/src/repo", req.RunDir.Unwrap(), "the runner gets the checkout and derives the worktree itself")
	assert.Equal(t, []string{"/wt/repo-chat-wt"}, locked)
}

func TestExecute_WorktreeChatDoesNotBlockTheCheckout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := newQuotaTestRepo(t)
	server := newQuotaTestServer(t, repo)
	server.dirLocks = dirlock.NewManager(nil)
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: "chat-wt", CurrentAgent: "wta", Worktree: true}))

	// Another chat works in the main checkout the whole time.
	rel, err := server.dirLocks.Acquire(context.Background(), dirlock.AcquireRequest{ChatID: "plain-chat", Dirs: []string{"/src/repo"}, Mode: dirlock.ModeExclusive})
	require.NoError(t, err)
	defer rel(false)

	server.dispatcher = &stubDispatcher{step: func(int, dispatch.Request) (dispatch.Result, error) {
		return dispatch.Result{Output: []byte(`{"last_content":"ok"}`), Target: worktreeAgent().Config.CLI[0]}, nil
	}}
	exec := NewSingleAgentExecutor(worktreeAgent(), server.conf, repo, server, nil)
	_, err = exec.Execute(context.Background(), SingleAgentRunParams{ChatID: "chat-wt", Prompt: "p", RunDir: "/src/repo"})
	require.NoError(t, err, "a worktree chat runs next to a chat holding the main checkout")
}

func TestExecute_PlainChatKeepsLockingTheCheckout(t *testing.T) {
	req, locked := worktreeRunFixture(t, worktreeAgent(), false)
	assert.False(t, req.Worktree)
	assert.Equal(t, []string{"/src/repo"}, locked)
}

func TestExecute_AgentWithoutWorktreeSupportIgnoresTheFlag(t *testing.T) {
	// e.g. a peer of a worktree chat that does not offer worktrees.
	req, locked := worktreeRunFixture(t, plainAgent(), true)
	assert.False(t, req.Worktree)
	assert.Equal(t, []string{"/src/repo"}, locked)
}
