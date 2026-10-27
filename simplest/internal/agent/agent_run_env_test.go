package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// envRecordingTool wraps recordingTool and captures run metadata injected by
// the agent loop.
type envRecordingTool struct {
	recordingTool
	mu  sync.Mutex
	env types.RunEnvironment
}

func newEnvRecordingTool(name string) *envRecordingTool {
	return &envRecordingTool{recordingTool: recordingTool{name: name, output: "ok", started: make(chan string, 16)}}
}

func (t *envRecordingTool) SetRunEnvironment(env types.RunEnvironment) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.env = env
}

func (t *envRecordingTool) snapshot() types.RunEnvironment {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.env
}

func assertCleanRun(t *testing.T, end *types.AgentEvent) {
	t.Helper()
	require.NotNil(t, end)
	require.False(t, end.IsError)
	require.NotEmpty(t, end.Messages)
	last, ok := end.Messages[len(end.Messages)-1].(*types.AssistantMessage)
	require.True(t, ok)
	require.Equal(t, types.StopStop, last.StopReason)
}

// Port of pi 005af57d8: the agent injects run metadata into tools before each
// tool-call batch.
func TestAgent_InjectRunEnvironment(t *testing.T) {
	t.Parallel()
	tool := newEnvRecordingTool("envtool")
	fp := &fakeProvider{responses: []*types.AssistantMessage{
		toolCallMsg(call("c1", "envtool", `{}`)),
		textMsg("done"),
	}}
	req := baseRequest(fp, tool)
	req.ThinkingLevel = types.ThinkingHigh
	req.SessionID = "sess-42"
	req.SessionFile = "/tmp/sess-42.jsonl"
	req.Model.Model = "wire-model-y"

	_, end := collect(t, Run(context.Background(), req))
	assertCleanRun(t, end)

	env := tool.snapshot()
	assert.Equal(t, "p", env.Provider)
	assert.Equal(t, "m", env.Model)
	assert.Equal(t, string(types.ThinkingHigh), env.ThinkingLevel)
	assert.Equal(t, "sess-42", env.SessionID)
	assert.Equal(t, "/tmp/sess-42.jsonl", env.SessionFile)
}

// Port of pi 005af57d8: PrepareRequest model/thinking changes are reflected
// at tool execution time.
func TestAgent_InjectRunEnvironment_RefreshAfterPrepareRequest(t *testing.T) {
	t.Parallel()
	tool := newEnvRecordingTool("envtool")
	fp := &fakeProvider{responses: []*types.AssistantMessage{
		toolCallMsg(call("c1", "envtool", `{}`)),
		textMsg("done"),
	}}
	req := baseRequest(fp, tool)
	req.ThinkingLevel = types.ThinkingLow
	req.PrepareRequest = func(r *Request) *Request {
		next := *r
		next.Model = &types.Model{ID: "m2", API: types.APIOpenAICompat, Provider: "p2", ContextWindow: 100000}
		next.ThinkingLevel = types.ThinkingMax
		return &next
	}

	_, end := collect(t, Run(context.Background(), req))
	assertCleanRun(t, end)

	env := tool.snapshot()
	assert.Equal(t, "p2", env.Provider)
	assert.Equal(t, "m2", env.Model)
	assert.Equal(t, string(types.ThinkingMax), env.ThinkingLevel)
}

// Port of pi 005af57d8: empty Model.ID falls back to WireID (Model field).
func TestAgent_InjectRunEnvironment_WireIDFallback(t *testing.T) {
	t.Parallel()
	tool := newEnvRecordingTool("envtool")
	fp := &fakeProvider{responses: []*types.AssistantMessage{
		toolCallMsg(call("c1", "envtool", `{}`)),
		textMsg("done"),
	}}
	req := baseRequest(fp, tool)
	req.Model.ID = ""
	req.Model.Model = "fallback-wire-id"

	_, end := collect(t, Run(context.Background(), req))
	assertCleanRun(t, end)

	env := tool.snapshot()
	assert.Equal(t, "fallback-wire-id", env.Model)
}
