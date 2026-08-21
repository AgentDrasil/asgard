package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/AgentDrasil/asgard/backend/lib/agents"
	"github.com/AgentDrasil/asgard/backend/lib/agentwrapper"
	"github.com/AgentDrasil/asgard/backend/lib/agentwrapper/types"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/workflow"
)

// stubRunner is a minimal NodeRunner fake used to verify custom runner injection.
type stubRunner struct {
	supports workflow.NodeType
	calls    atomic.Int64
}

func (r *stubRunner) Supports(t workflow.NodeType) bool { return t == r.supports }

func (r *stubRunner) Run(ctx context.Context, nctx *workflow.NodeContext) (*workflow.NodeResult, error) {
	r.calls.Add(1)
	return &workflow.NodeResult{Status: workflow.StatusSucceeded, ExitCode: 0, Output: "stub"}, nil
}

func TestNewWorkflowEngine_InjectsRegistryAndRunners(t *testing.T) {
	t.Parallel()

	funcRegistry := workflow.NewFunctionRegistry()
	funcRegistry.Register("di_fn", func(ctx context.Context, nctx *workflow.NodeContext) (string, error) {
		return "ok", nil
	})

	llmStub := &stubRunner{supports: workflow.NodeTypeLLM}
	cmdStub := &stubRunner{supports: workflow.NodeTypeCommand}

	conf := &config.Config{AgentDir: t.TempDir()}
	engine, err := newWorkflowEngine(conf, nil, funcRegistry, llmStub, cmdStub)
	require.NoError(t, err)
	require.NotNil(t, engine)

	registry := engine.Registry()
	require.NotNil(t, registry)

	fnRunner, ok := registry.Get(workflow.NodeTypeFunction)
	require.True(t, ok, "function runner must be registered")
	res, err := fnRunner.Run(context.Background(), &workflow.NodeContext{
		Node:      &workflow.NodeSpec{ID: "n1", Type: workflow.NodeTypeFunction, Function: "di_fn"},
		Upstreams: map[string]*workflow.NodeResult{},
	})
	require.NoError(t, err)
	assert.Equal(t, workflow.StatusSucceeded, res.Status)
	assert.Equal(t, "ok", res.Output)

	gotLLM, ok := registry.Get(workflow.NodeTypeLLM)
	require.True(t, ok, "custom LLM-type runner must be registered")
	assert.Same(t, llmStub, gotLLM)

	gotCmd, ok := registry.Get(workflow.NodeTypeCommand)
	require.True(t, ok, "custom command-type runner must replace the default")
	assert.Same(t, cmdStub, gotCmd)
}

func TestNewWorkflowEngine_NilRegistryFallsBackToDefault(t *testing.T) {
	t.Parallel()

	engine, err := newWorkflowEngine(nil, nil, nil)
	require.NoError(t, err)

	fnRunner, ok := engine.Registry().Get(workflow.NodeTypeFunction)
	require.True(t, ok, "function runner must be registered even with nil registry")

	name := fmt.Sprintf("default-fallback-%d", time.Now().UnixNano())
	workflow.RegisterFunction(name, func(ctx context.Context, nctx *workflow.NodeContext) (string, error) {
		return "from-default", nil
	})

	res, err := fnRunner.Run(context.Background(), &workflow.NodeContext{
		Node:      &workflow.NodeSpec{ID: "n1", Type: workflow.NodeTypeFunction, Function: name},
		Upstreams: map[string]*workflow.NodeResult{},
	})
	require.NoError(t, err)
	assert.Equal(t, workflow.StatusSucceeded, res.Status)
	assert.Equal(t, "from-default", res.Output)
}

func TestServerOptions_FunctionRegistryFallbackAndReplacement(t *testing.T) {
	t.Parallel()

	globalName := fmt.Sprintf("global-fn-%d", time.Now().UnixNano())
	workflow.RegisterFunction(globalName, func(ctx context.Context, nctx *workflow.NodeContext) (string, error) {
		return "global", nil
	})

	t.Run("WithFunction keeps global functions resolvable", func(t *testing.T) {
		t.Parallel()

		var s Server
		localName := "local-fn"
		applyOpts(t, &s, WithFunction(localName, func(ctx context.Context, nctx *workflow.NodeContext) (string, error) {
			return "local", nil
		}))

		require.NotNil(t, s.funcRegistry)

		fn, ok := s.funcRegistry.Get(localName)
		require.True(t, ok, "instance function must resolve")
		out, err := fn(context.Background(), nil)
		require.NoError(t, err)
		assert.Equal(t, "local", out)

		fn, ok = s.funcRegistry.Get(globalName)
		require.True(t, ok, "globally registered function must resolve through parent fallback")
		out, err = fn(context.Background(), nil)
		require.NoError(t, err)
		assert.Equal(t, "global", out)
	})

	t.Run("WithFunction shadows global registration", func(t *testing.T) {
		t.Parallel()

		var s Server
		applyOpts(t, &s, WithFunction(globalName, func(ctx context.Context, nctx *workflow.NodeContext) (string, error) {
			return "shadowed", nil
		}))

		fn, ok := s.funcRegistry.Get(globalName)
		require.True(t, ok)
		out, err := fn(context.Background(), nil)
		require.NoError(t, err)
		assert.Equal(t, "shadowed", out)
	})

	t.Run("WithFunctionRegistry replaces the registry entirely", func(t *testing.T) {
		t.Parallel()

		explicit := workflow.NewFunctionRegistry()
		explicit.Register("only-here", func(ctx context.Context, nctx *workflow.NodeContext) (string, error) {
			return "explicit", nil
		})

		var s Server
		applyOpts(t, &s,
			WithFunction("dropped", func(ctx context.Context, nctx *workflow.NodeContext) (string, error) {
				return "dropped", nil
			}),
			WithFunctionRegistry(explicit),
		)

		_, ok := s.funcRegistry.Get("dropped")
		assert.False(t, ok, "previously injected function must not survive registry replacement")

		fn, ok := s.funcRegistry.Get("only-here")
		require.True(t, ok)
		out, err := fn(context.Background(), nil)
		require.NoError(t, err)
		assert.Equal(t, "explicit", out)
	})

	t.Run("runner options accumulate", func(t *testing.T) {
		t.Parallel()

		r1 := &stubRunner{supports: workflow.NodeTypeLLM}
		r2 := &stubRunner{supports: workflow.NodeTypeHuman}
		r3 := &stubRunner{supports: workflow.NodeTypeAgent}

		var s Server
		applyOpts(t, &s, WithNodeRunner(r1), WithCustomRunners(r2, nil, r3))

		assert.Len(t, s.customRunners, 3, "nil runners must be skipped")
		assert.Same(t, r1, s.customRunners[0])
		assert.Same(t, r2, s.customRunners[1])
		assert.Same(t, r3, s.customRunners[2])
	})
}

func applyOpts(t *testing.T, s *Server, opts ...ServerOption) {
	t.Helper()
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
}

func TestServer_ShutdownIdempotentWithoutStart(t *testing.T) {
	t.Parallel()

	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"Gemini 3.5 Flash (Low)"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	agentDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(agentDir, "agents", "agent_father"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0o644))
	fatherYAML := `
id: "agent_father"
name: "Agent Father"
description: "The agent creates other agents."
team: "my-team"
run_dirs: ["/tmp"]
cli:
  - cli: "agy"
    model: "Gemini 3.5 Flash (Low)"
`
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "agents", "agent_father", "config.yaml"), []byte(fatherYAML), 0o644))

	testDB := db.NewDBForTest(t)
	conf := &config.Config{
		AgentDir:     agentDir,
		Port:         0,
		InternalPort: 0,
	}

	srv, err := New(conf, testDB)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, srv.Shutdown(ctx), "first shutdown on a never-started server must succeed")
	assert.ErrorIs(t, srv.ctx.Err(), context.Canceled, "root context must be canceled by Shutdown")

	// Repeated invocations must neither panic, deadlock, nor re-close resources.
	for i := 0; i < 3; i++ {
		require.NoError(t, srv.Shutdown(ctx))
	}
}

func TestServer_ShutdownBeforeOrConcurrentWithStart(t *testing.T) {
	t.Parallel()

	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"Gemini 3.5 Flash (Low)"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	agentDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(agentDir, "agents", "agent_father"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0o644))
	fatherYAML := `
id: "agent_father"
name: "Agent Father"
description: "The agent creates other agents."
team: "my-team"
run_dirs: ["/tmp"]
cli:
  - cli: "agy"
    model: "Gemini 3.5 Flash (Low)"
`
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "agents", "agent_father", "config.yaml"), []byte(fatherYAML), 0o644))

	testDB := db.NewDBForTest(t)
	conf := &config.Config{
		AgentDir:     agentDir,
		Port:         0,
		InternalPort: 0,
	}

	srv, err := New(conf, testDB)
	require.NoError(t, err)

	// Case 1: Shutdown called BEFORE Start() is invoked
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, srv.Shutdown(ctx))

	err = srv.Start()
	assert.Error(t, err, "Start() must return an error if the server was already shut down")
	assert.Contains(t, err.Error(), "server shut down before start completed")
}

const functionNodeWorkflowYAML = `
name: function-e2e
tmp_dir: "tmp/${session_id}"
nodes:
  - id: greet
    type: function
    function: make_greeting
  - id: wrap
    type: function
    function: wrap_upstream
    depends:
      - node: greet
`

const failingFunctionNodeWorkflowYAML = `
name: function-fail-e2e
tmp_dir: "tmp/${session_id}"
nodes:
  - id: boom
    type: function
    function: explode
`

func newFunctionTestServer(t *testing.T, workflowYAML string, register func(reg *workflow.FunctionRegistry)) (*Server, *dbmodels.SessionRepository, *gorm.DB) {
	t.Helper()

	testDB := db.NewDBForTest(t)
	sqlDB, err := testDB.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	repo := dbmodels.NewSessionRepository(testDB)
	wfRepo := dbmodels.NewWorkflowRunRepository(testDB)

	funcRegistry := workflow.NewFunctionRegistry()
	register(funcRegistry)

	tempDir := t.TempDir()
	wfFile := filepath.Join(tempDir, "workflow.yaml")
	require.NoError(t, os.WriteFile(wfFile, []byte(workflowYAML), 0644))

	conf := &config.Config{AgentDir: tempDir}
	engine, err := newWorkflowEngine(conf, nil, funcRegistry)
	require.NoError(t, err)
	engine.SetRunStore(newWorkflowRunStore(wfRepo))

	hub := NewSessionEventHubWithCapacity(50)
	t.Cleanup(hub.Close)

	agent := &agents.Agent{
		Config: agents.AgentConfig{
			ID:   "fn-agent",
			Name: "Function Agent",
			Type: "workflow",
		},
		WorkflowPath: wfFile,
	}

	s := &Server{
		conf:           conf,
		repo:           repo,
		eventHub:       hub,
		workflowEngine: engine,
		agents:         []*agents.Agent{agent},
		ctx:            context.Background(),
	}

	return s, repo, testDB
}

func TestWorkflowFunctionNodeEndToEnd(t *testing.T) {
	t.Parallel()

	var greetCalled atomic.Bool

	s, repo, testDB := newFunctionTestServer(t, functionNodeWorkflowYAML, func(reg *workflow.FunctionRegistry) {
		reg.Register("make_greeting", func(ctx context.Context, nctx *workflow.NodeContext) (string, error) {
			greetCalled.Store(true)
			return "hello from go", nil
		})
		reg.Register("wrap_upstream", func(ctx context.Context, nctx *workflow.NodeContext) (string, error) {
			up, ok := nctx.Upstreams["greet"]
			if !ok || up == nil {
				return "", errors.New("missing upstream greet")
			}
			return "wrapped:" + up.Output, nil
		})
	})

	chatID := "chat-function-e2e"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "fn-agent"}))

	subCh, _, cancel := s.eventHub.Subscribe(chatID, 0)
	t.Cleanup(cancel)

	status, _, err := s.runWorkflow(context.Background(), s.agents[0], chatID, TriggerMessageRequest{Prompt: "run it"})
	require.NoError(t, err)
	assert.Equal(t, "completed", status)

	assert.True(t, greetCalled.Load(), "Go function must have been invoked")

	// The function output flows through the event hub as assistant messages.
	deadline := time.Now().Add(5 * time.Second)
	var sawAssistantMsg bool
	for time.Now().Before(deadline) && !sawAssistantMsg {
		select {
		case ev := <-subCh:
			if ev.Type == "message" && ev.Message != nil && ev.Message.Role == "assistant" {
				if ev.Message.Content == "hello from go" || ev.Message.Content == "wrapped:hello from go" {
					sawAssistantMsg = true
				}
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	assert.True(t, sawAssistantMsg, "function outputs must be published as assistant messages")

	// ...and persisted in the settled run snapshot.
	waitForRunStatus(t, testDB, chatID, workflow.PersistStatusCompleted)
	var run dbmodels.WorkflowRun
	require.NoError(t, testDB.Where("session_id = ?", chatID).Order("updated_at DESC").First(&run).Error)
	states, err := dbmodels.DecodeNodeStates(run.NodeStates)
	require.NoError(t, err)
	assert.Equal(t, "hello from go", states["greet"].Output, "function output must be persisted in node state")
	assert.Equal(t, "wrapped:hello from go", states["wrap"].Output, "downstream node must capture upstream function output")

	// Assistant messages survive in the session transcript.
	session, err := repo.GetSession(chatID)
	require.NoError(t, err)
	foundGreet := false
	for _, m := range session.Messages {
		if m.Role == "assistant" && m.Content == "hello from go" {
			foundGreet = true
		}
	}
	assert.True(t, foundGreet, "function output must be appended to the chat transcript")
}

func TestWorkflowFunctionNodeErrorFailsRun(t *testing.T) {
	t.Parallel()

	s, repo, testDB := newFunctionTestServer(t, failingFunctionNodeWorkflowYAML, func(reg *workflow.FunctionRegistry) {
		reg.Register("explode", func(ctx context.Context, nctx *workflow.NodeContext) (string, error) {
			return "", errors.New("kaboom")
		})
	})

	chatID := "chat-function-fail-e2e"
	require.NoError(t, repo.SaveSession(&dbmodels.Session{ChatID: chatID, CurrentAgent: "fn-agent"}))

	status, _, err := s.runWorkflow(context.Background(), s.agents[0], chatID, TriggerMessageRequest{Prompt: "break it"})
	require.Error(t, err)
	assert.Equal(t, "failed", status)

	waitForRunStatus(t, testDB, chatID, workflow.PersistStatusFailed)

	// The failure is surfaced in the chat transcript as an error message.
	require.Eventually(t, func() bool {
		session, err := repo.GetSession(chatID)
		if err != nil {
			return false
		}
		for _, m := range session.Messages {
			if m.Role == "error" && strings.Contains(m.Content, "kaboom") {
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond, "function error must be persisted as an error message")
}
