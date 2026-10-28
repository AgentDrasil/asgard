package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moznion/go-optional"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/agents/run"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/runnerpb"
	"github.com/AgentDrasil/asgard/runner/lib/conn"
)

// recordingSink collects everything an executor reports.
type recordingSink struct {
	mu      sync.Mutex
	events  []*runnerpb.RunEvent
	chunks  []*runnerpb.RunOutputChunk
	results chan *runnerpb.RunResult
}

func newRecordingSink() *recordingSink {
	return &recordingSink{results: make(chan *runnerpb.RunResult, 4)}
}

func (s *recordingSink) Event(_ context.Context, ev *runnerpb.RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	return nil
}

func (s *recordingSink) Output(_ context.Context, c *runnerpb.RunOutputChunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chunks = append(s.chunks, c)
	return nil
}

func (s *recordingSink) Result(_ context.Context, r *runnerpb.RunResult) error {
	s.results <- r
	return nil
}

func (s *recordingSink) eventList() []*runnerpb.RunEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*runnerpb.RunEvent(nil), s.events...)
}

func (s *recordingSink) wait(t *testing.T) *runnerpb.RunResult {
	t.Helper()
	select {
	case r := <-s.results:
		return r
	case <-time.After(5 * time.Second):
		require.FailNow(t, "no run result")
		return nil
	}
}

func newTestExecutor(t *testing.T, fn runFunc, agents ...*agentspec.Agent) *Executor {
	t.Helper()
	return New(Options{
		Agents:    &fakeAgents{agents: agents},
		AgentWait: 100 * time.Millisecond,
		AgentPoll: 5 * time.Millisecond,
		Conf:      func() *config.Config { return &config.Config{ConfigPath: "/cfg/config.yaml"} },
		run:       fn,
	})
}

func baseStartRun() *runnerpb.StartRun {
	return &runnerpb.StartRun{
		RunId:       "run-1",
		ChatId:      "chat-1",
		AgentId:     "a",
		AgentHash:   "h1",
		Attempt:     3,
		Prompt:      "hi",
		StatusScope: &runnerpb.StatusScope{NodeId: "node-7", RunToken: "tok"},
	}
}

func TestExecutorRunSuccess(t *testing.T) {
	t.Parallel()

	var gotAgent *agentspec.Agent
	var gotCands []agentspec.CLITarget
	var gotModel optional.Option[string]
	var gotScope run.StatusScope
	var gotOpts run.Options
	fn := func(_ context.Context, agent *agentspec.Agent, cands []agentspec.CLITarget, prompt string, sessions run.SessionMap, runDir, model optional.Option[string], chatID string, scope run.StatusScope, opts run.Options) (run.Result, error) {
		gotAgent, gotCands, gotModel, gotScope, gotOpts = agent, cands, model, scope, opts
		assert.Equal(t, "hi", prompt)
		assert.Equal(t, "chat-1", chatID)
		assert.Equal(t, run.SessionMap{"opencode": "s1"}, sessions)
		assert.Equal(t, "/proj", runDir.Unwrap())
		return run.Result{Output: []byte("out"), Target: agentspec.CLITarget{CLI: "opencode", Model: "m1"}, RunDir: "/proj"}, nil
	}
	ex := newTestExecutor(t, fn, testAgent("a", "h1", ""))
	sr := baseStartRun()
	sr.Candidates = []*runnerpb.Target{{Cli: "opencode", Model: "m1"}}
	sr.Model = "m1"
	sr.Sessions = map[string]string{"opencode": "s1"}
	sr.RunDir = &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, RelOrPath: "/proj"}
	sr.RunDirs = []string{"/proj"}
	sr.EnabledProviders = []string{"opencode"}
	sr.LangRules = "rules"
	sr.StatusScope.Headless = true
	sink := newRecordingSink()

	ex.StartRun(context.Background(), sr, sink)
	res := sink.wait(t)

	assert.Empty(t, res.GetError())
	assert.Equal(t, "run-1", res.GetRunId())
	assert.Equal(t, "chat-1", res.GetChatId())
	assert.Equal(t, uint32(3), res.GetAttempt())
	assert.Equal(t, "node-7", res.GetWorkflowNodeId())
	assert.Equal(t, "tok", res.GetRunToken())
	assert.Equal(t, uint64(3), res.GetOutputSize())
	assert.Equal(t, "opencode", res.GetSelectedTarget().GetCli())
	assert.Equal(t, "/proj", res.GetRunDir())
	assert.Equal(t, sr.GetRunDir(), res.GetRunDirRef())

	assert.Equal(t, []string{"/proj"}, gotAgent.Config.RunDirs)
	assert.Equal(t, []agentspec.CLITarget{{CLI: "opencode", Model: "m1"}}, gotCands)
	assert.Equal(t, "m1", gotModel.Unwrap())
	assert.Equal(t, run.StatusScope{NodeID: "node-7", RunToken: "tok", Headless: true}, gotScope)
	assert.Equal(t, "rules", gotOpts.LangRules)
	assert.Equal(t, "/cfg/config.yaml", gotOpts.ConfigPath)
	assert.True(t, gotOpts.IsProviderEnabled("opencode"))
	assert.False(t, gotOpts.IsProviderEnabled("agy"))
	assert.Contains(t, gotOpts.Env, "ASGARD_STATUS_URL="+SandboxStatusURL)
}

func TestExecutorOutputChunking(t *testing.T) {
	t.Parallel()

	out := bytes.Repeat([]byte("x"), 2*outputChunkSize+10)
	fn := func(context.Context, *agentspec.Agent, []agentspec.CLITarget, string, run.SessionMap, optional.Option[string], optional.Option[string], string, run.StatusScope, run.Options) (run.Result, error) {
		return run.Result{Output: out}, nil
	}
	ex := newTestExecutor(t, fn, testAgent("a", "h1", ""))
	sink := newRecordingSink()

	ex.StartRun(context.Background(), baseStartRun(), sink)
	res := sink.wait(t)

	require.Len(t, sink.chunks, 3)
	var joined []byte
	for i, c := range sink.chunks {
		assert.Equal(t, uint64(len(joined)), c.GetOffset(), "chunk %d", i)
		assert.LessOrEqual(t, len(c.GetData()), outputChunkSize)
		assert.Equal(t, "run-1", c.GetRunId())
		joined = append(joined, c.GetData()...)
	}
	assert.Equal(t, out, joined)
	assert.Equal(t, uint64(len(out)), res.GetOutputSize())
}

func TestExecutorNoQuota(t *testing.T) {
	t.Parallel()

	nq := &run.NoQuotaError{
		AgentID:       "a",
		ExplicitModel: "m1",
		MinThreshold:  0.1,
		PairingNote:   "pair",
		Targets:       []run.QuotaTargetStatus{{CLI: "opencode", Model: "m1", Remaining: 0.05, Enabled: true}, {CLI: "agy", Model: "g", Enabled: false}},
	}
	fn := func(context.Context, *agentspec.Agent, []agentspec.CLITarget, string, run.SessionMap, optional.Option[string], optional.Option[string], string, run.StatusScope, run.Options) (run.Result, error) {
		return run.Result{}, fmt.Errorf("wrapped: %w", nq)
	}
	ex := newTestExecutor(t, fn, testAgent("a", "h1", ""))
	sink := newRecordingSink()

	ex.StartRun(context.Background(), baseStartRun(), sink)
	res := sink.wait(t)

	assert.ErrorContains(t, errors.New(res.GetError()), "no quota remaining")
	got := res.GetNoQuota()
	require.NotNil(t, got)
	assert.Equal(t, "a", got.GetAgentId())
	assert.Equal(t, "m1", got.GetExplicitModel())
	assert.InDelta(t, 0.1, got.GetMinThreshold(), 1e-9)
	assert.Equal(t, "pair", got.GetPairingNote())
	require.Len(t, got.GetTargets(), 2)
	assert.Equal(t, "opencode", got.GetTargets()[0].GetCli())
	assert.InDelta(t, 0.05, got.GetTargets()[0].GetRemaining(), 1e-9)
	assert.False(t, got.GetTargets()[1].GetEnabled())
	assert.Nil(t, res.GetSelectedTarget())
}

func TestExecutorFailures(t *testing.T) {
	t.Parallel()

	failing := func(context.Context, *agentspec.Agent, []agentspec.CLITarget, string, run.SessionMap, optional.Option[string], optional.Option[string], string, run.StatusScope, run.Options) (run.Result, error) {
		return run.Result{Output: []byte("partial")}, errors.New(strings.Repeat("e", maxErrorSize+100))
	}

	t.Run("agent hash missing fails the run", func(t *testing.T) {
		t.Parallel()
		ex := newTestExecutor(t, failing, testAgent("a", "other", ""))
		sink := newRecordingSink()
		ex.StartRun(context.Background(), baseStartRun(), sink)
		res := sink.wait(t)
		assert.Contains(t, res.GetError(), "not available on this runner")
		assert.Contains(t, res.GetError(), "h1")
		assert.Empty(t, sink.chunks)
	})
	t.Run("bad run dir", func(t *testing.T) {
		t.Parallel()
		ex := newTestExecutor(t, failing, testAgent("a", "h1", ""))
		sr := baseStartRun()
		sr.RunDir = &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, RelOrPath: "rel"}
		sink := newRecordingSink()
		ex.StartRun(context.Background(), sr, sink)
		assert.Contains(t, sink.wait(t).GetError(), "resolving run dir")
	})
	t.Run("long error is truncated and partial output kept", func(t *testing.T) {
		t.Parallel()
		ex := newTestExecutor(t, failing, testAgent("a", "h1", ""))
		sink := newRecordingSink()
		ex.StartRun(context.Background(), baseStartRun(), sink)
		res := sink.wait(t)
		assert.True(t, res.GetErrorTruncated())
		assert.Len(t, res.GetError(), maxErrorSize)
		assert.Equal(t, uint64(len("partial")), res.GetOutputSize())
		require.Len(t, sink.chunks, 1)
	})
}

func TestExecutorCancelKillsRun(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	fn := func(ctx context.Context, _ *agentspec.Agent, _ []agentspec.CLITarget, _ string, _ run.SessionMap, _, _ optional.Option[string], _ string, _ run.StatusScope, _ run.Options) (run.Result, error) {
		close(started)
		<-ctx.Done() // the sandbox is killed when the context ends
		return run.Result{Output: []byte("partial")}, ctx.Err()
	}
	ex := newTestExecutor(t, fn, testAgent("a", "h1", ""))
	sink := newRecordingSink()

	ex.StartRun(context.Background(), baseStartRun(), sink) // returns without blocking
	<-started
	ex.CancelRun("run-1")
	res := sink.wait(t)

	assert.Equal(t, ErrCancelled, res.GetError())
	assert.Equal(t, "run-1", res.GetRunId())
	require.Eventually(t, func() bool {
		ex.mu.Lock()
		defer ex.mu.Unlock()
		return len(ex.cancels) == 0
	}, time.Second, 5*time.Millisecond)
	ex.CancelRun("run-1") // unknown run: no-op
}

func TestExecutorRefusesWithoutSandboxSetup(t *testing.T) {
	t.Parallel()

	called := false
	fn := func(context.Context, *agentspec.Agent, []agentspec.CLITarget, string, run.SessionMap, optional.Option[string], optional.Option[string], string, run.StatusScope, run.Options) (run.Result, error) {
		called = true
		return run.Result{}, nil
	}
	tests := []struct {
		name    string
		mod     func(*Options)
		wantErr string
	}{
		{"not ready", func(o *Options) { o.Ready = func() error { return errors.New("proxy unavailable") } }, "proxy unavailable"},
		{"no conf func", func(o *Options) { o.Conf = nil }, "sandbox configuration not available"},
		{"nil conf", func(o *Options) { o.Conf = func() *config.Config { return nil } }, "sandbox configuration not available"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ex := newTestExecutor(t, fn, testAgent("a", "h1", ""))
			tt.mod(&ex.opts)
			sink := newRecordingSink()
			ex.StartRun(context.Background(), baseStartRun(), sink)
			assert.Contains(t, sink.wait(t).GetError(), tt.wantErr)
		})
	}
	assert.False(t, called)
}

// rejectFirstResultSink fails the first Result as unencodable.
type rejectFirstResultSink struct {
	*recordingSink
	rejected bool
}

func (s *rejectFirstResultSink) Result(ctx context.Context, r *runnerpb.RunResult) error {
	if !s.rejected {
		s.rejected = true
		return fmt.Errorf("%w: test", conn.ErrInvalidMessage)
	}
	return s.recordingSink.Result(ctx, r)
}

func TestExecutorFallbackResultKeepsOutputAndDirs(t *testing.T) {
	t.Parallel()

	fn := func(context.Context, *agentspec.Agent, []agentspec.CLITarget, string, run.SessionMap, optional.Option[string], optional.Option[string], string, run.StatusScope, run.Options) (run.Result, error) {
		return run.Result{Output: []byte("queued output"), Target: agentspec.CLITarget{CLI: "opencode", Model: "m1"}, RunDir: "/proj"}, nil
	}
	ex := newTestExecutor(t, fn, testAgent("a", "h1", ""))
	sink := &rejectFirstResultSink{recordingSink: newRecordingSink()}

	ex.StartRun(context.Background(), baseStartRun(), sink)
	res := sink.wait(t)

	assert.Contains(t, res.GetError(), "failed to report run result")
	assert.Equal(t, uint64(len("queued output")), res.GetOutputSize())
	assert.Equal(t, "/proj", res.GetRunDir())
	assert.NotNil(t, res.GetRunDirRef())
	assert.Equal(t, "opencode", res.GetSelectedTarget().GetCli())
}

// The status server computes artifacts with the workflow-effective (overlaid)
// agent config, as the backend did before: a file under an overlay read-write
// mount is an artifact although the agent's own config has no such mount.
func TestExecutorArtifactsUseOverlaidAgentConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rw := "/ovrw"
	fn := func(ctx context.Context, agent *agentspec.Agent, _ []agentspec.CLITarget, _ string, _ run.SessionMap, _, _ optional.Option[string], _ string, _ run.StatusScope, opts run.Options) (run.Result, error) {
		sockDir := shortSockDir(t)
		stop, err := opts.OnSockDir(sockDir, "/proj")
		require.NoError(t, err)
		defer stop()
		status := postStatus(t, unixClient(filepath.Join(sockDir, SocketName)), map[string]any{
			"step_index": 1, "source": "aw", "entry_type": "tool_call", "content": "w",
			"metadata": map[string]any{"target_files": []string{filepath.Join(rw, "out.md")}},
		})
		assert.Equal(t, http.StatusNoContent, status)
		return run.Result{Target: agentspec.CLITarget{CLI: "opencode", Model: "m1"}, RunDir: "/proj"}, nil
	}
	agent := testAgent("a", "h1", "")
	require.Empty(t, agent.Config.MountDirs.ReadWrite, "the agent's own config has no rw mount")
	ex := newTestExecutor(t, fn, agent)
	sr := baseStartRun()
	sr.RunDir = &runnerpb.DirRef{Scope: runnerpb.FileScope_FILE_SCOPE_ABSOLUTE, RelOrPath: "/proj"}
	sr.MountDirsReadwrite = []string{rw}
	sink := newRecordingSink()
	ex.StartRun(context.Background(), sr, sink)
	sink.wait(t)

	evs := sink.eventList()
	require.Len(t, evs, 1)
	require.Len(t, evs[0].GetArtifacts(), 1)
	assert.Equal(t, filepath.Join(rw, "out.md"), evs[0].GetArtifacts()[0].GetViewerPath())

	// Without the overlay the same file is not an artifact.
	sr2 := baseStartRun()
	sr2.RunId = "run-2"
	sr2.RunDir = sr.RunDir
	sink2 := newRecordingSink()
	ex.StartRun(context.Background(), sr2, sink2)
	sink2.wait(t)
	evs = sink2.eventList()
	require.Len(t, evs, 1)
	assert.Empty(t, evs[0].GetArtifacts())
}
