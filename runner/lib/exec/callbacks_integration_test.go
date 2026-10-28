package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/api"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths"
)

type staticAgents []*agentspec.Agent

func (s staticAgents) LoadAll() ([]*agentspec.Agent, error) { return s, nil }

// callbackStack is a real backend API server on top of the in-process hub and
// a runner executing in the fake sandbox.
type callbackStack struct {
	hub    *runnerhub.Hub
	server *api.Server
}

func newCallbackStack(t *testing.T) *callbackStack {
	t.Helper()
	setupFakeSandbox(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required by the fake sandbox")
	}
	require.NoError(t, os.MkdirAll(paths.ConfigDir(), 0o755))
	require.NoError(t, os.WriteFile(paths.ConfigFile(), []byte("secret"), 0o600))

	peer := testAgent("peer", "h2", "")
	peer.Config.RunDirs = []string{t.TempDir()}
	hub := startStack(t, newIntegrationExecutor(t, peer))

	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	caller := testAgent("a", "h1", "")
	caller.Config.RunDirs = []string{t.TempDir()}
	server, err := api.New(&config.Config{AgentDir: t.TempDir()}, testDB,
		api.WithAgentSource(staticAgents{caller, peer, testAgent("agent_father", "h3", "")}),
		api.WithRunnerHub(hub),
		api.WithCompressionMetrics(api.NewCompressionMetricsStore(t.TempDir()+"/metrics.json")),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	repo := dbmodels.NewSessionRepository(testDB)
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: "chat-int", CurrentAgent: "a"}))
	_, err = repo.BindRunner("chat-int", testNode)
	require.NoError(t, err)
	return &callbackStack{hub: hub, server: server}
}

// TestSandboxAskUserThroughRunnerAndBackend drives ask-user from the fake
// sandbox: sandbox -> runner socket -> hub -> backend, and resolves it with the
// WebUI's reply endpoint.
func TestSandboxAskUserThroughRunnerAndBackend(t *testing.T) {
	st := newCallbackStack(t)
	sub, _, cancel := st.server.EventHub().Subscribe("chat-int", 0)
	t.Cleanup(cancel)

	ctx, cancelCtx := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCtx()
	run, err := st.hub.Dispatch(ctx, testNode, integrationStartRun(t, "run-ask", "ASKME"))
	require.NoError(t, err)

	var askMsgID string
	select {
	case ev := <-sub:
		require.NotNil(t, ev.Message)
		assert.Equal(t, "ask_user", ev.Message.Role)
		assert.Equal(t, "proceed?", ev.Message.Content)
		askMsgID = ev.Message.ID
		assert.Equal(t, "ask-run-ask-1", askMsgID, "message id derived by the runner from run_id and call_seq")
	case <-time.After(20 * time.Second):
		t.Fatal("the question never reached the backend")
	}
	// The ask-user call is still blocked on the user.
	_, err = run.Wait(shortCtx(t, 100*time.Millisecond))
	require.ErrorIs(t, err, context.DeadlineExceeded)

	body, err := json.Marshal(api.AskUserReplyRequest{ChatID: "chat-int", MessageID: askMsgID, ReplyText: "go ahead"})
	require.NoError(t, err)
	rr := httptest.NewRecorder()
	st.server.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/ask-user/reply", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rr.Code)

	res, err := run.Wait(ctx)
	require.NoError(t, err)
	assert.Empty(t, res.RunResult.GetError())
	assert.Contains(t, string(res.Output), `ask-reply: {"reply":"go ahead"}`)
}

// TestSandboxCallPeerRunsPeerOnSameRunner covers the transport and the
// dispatch only: the caller run is dispatched straight through the hub, so it
// does NOT hold the chat's execution guard the way a run started through
// POST /api/agents/{id}/message does. See
// TestSandboxCallPeerIntoOwnChatIsRejectedByGuard for that path. Here the
// backend triggers the peer agent in the caller's chat, which the hub
// dispatches to the same runner.
func TestSandboxCallPeerRunsPeerOnSameRunner(t *testing.T) {
	st := newCallbackStack(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	run, err := st.hub.Dispatch(ctx, testNode, integrationStartRun(t, "run-peer", "PEERME"))
	require.NoError(t, err)

	res, err := run.Wait(ctx)
	require.NoError(t, err)
	assert.Empty(t, res.RunResult.GetError())
	out := string(res.Output)
	require.Contains(t, out, "peer-reply: ", out)
	line := out[strings.Index(out, "peer-reply: ")+len("peer-reply: "):]
	line = line[:strings.Index(line, "\n")]
	var peer struct {
		Status string `json:"status"`
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(line), &peer), line)
	assert.Equal(t, "completed", peer.Status, peer.Error)
	assert.Contains(t, peer.Output, "fake agent output")
}

func shortCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

// TestSandboxCallPeerIntoOwnChatIsRejectedByGuard starts the caller through
// the backend's trigger endpoint, so it holds the chat-wide execution guard.
// call-peer into the caller's own chat then gets the pre-existing 409 "session
// is already running a task", reported as a failed peer call.
func TestSandboxCallPeerIntoOwnChatIsRejectedByGuard(t *testing.T) {
	st := newCallbackStack(t)

	body, err := json.Marshal(map[string]any{"prompt": "PEERME", "chatId": "chat-int", "wait": true})
	require.NoError(t, err)
	rr := httptest.NewRecorder()
	st.server.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/agents/a/message", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	var resp struct {
		Output string `json:"output"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	i := strings.Index(resp.Output, "peer-reply: ")
	require.GreaterOrEqual(t, i, 0, resp.Output)
	line := resp.Output[i+len("peer-reply: "):]
	line = line[:strings.Index(line, "\n")]
	var peer struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(line), &peer), line)
	assert.Equal(t, "failed", peer.Status)
	assert.Contains(t, peer.Error, "session is already running a task")
}
