package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// --- fakes ---

// fakeProvider plays a scripted list of assistant responses, one per call.
type fakeProvider struct {
	mu        sync.Mutex
	responses []*types.AssistantMessage
	calls     int
	contexts  []*types.Context
}

func (f *fakeProvider) Stream(ctx context.Context, model *types.Model, cx *types.Context, opts *types.StreamOptions) <-chan types.AssistantMessageEvent {
	f.mu.Lock()
	i := f.calls
	f.calls++
	f.contexts = append(f.contexts, cx)
	f.mu.Unlock()

	ch := make(chan types.AssistantMessageEvent, 8)
	go func() {
		defer close(ch)
		partial := &types.AssistantMessage{}
		ch <- types.Partial{Kind: types.EvStart, Partial: partial}
		if i >= len(f.responses) || f.responses[i] == nil {
			errMsg := &types.AssistantMessage{Content: []types.AssistantContent{}, StopReason: types.StopError, ErrorMessage: "no scripted response"}
			ch <- types.StreamErrorEvent{Kind: types.EvStreamError, Reason: types.StopError, Message: errMsg}
			return
		}
		resp := f.responses[i]
		if len(resp.Content) > 0 {
			ch <- types.Partial{Kind: types.EvTextDelta, ContentIndex: 0, Delta: "x", Partial: partial}
		}
		ch <- types.DoneEvent{Kind: types.EvDone, Reason: resp.StopReason, Message: resp}
	}()
	return ch
}

func textMsg(text string) *types.AssistantMessage {
	return &types.AssistantMessage{
		Content:    []types.AssistantContent{types.TextContent{Type: "text", Text: text}},
		StopReason: types.StopStop,
		Timestamp:  1,
	}
}

func toolCallMsg(calls ...types.ToolCall) *types.AssistantMessage {
	content := []types.AssistantContent{}
	for _, tc := range calls {
		content = append(content, tc)
	}
	return &types.AssistantMessage{
		Content:    content,
		StopReason: types.StopToolUse,
		Timestamp:  1,
	}
}

func call(id, name, argsJSON string) types.ToolCall {
	return types.ToolCall{Type: "toolCall", ID: id, Name: name, Arguments: json.RawMessage(argsJSON)}
}

// recordingTool executes and records invocations.
type recordingTool struct {
	name       string
	mode       types.ToolExecutionMode
	output     string
	err        error
	onStart    func()
	executions []string // tool call ids in completion order
	started    chan string
}

func newRecordingTool(name string) *recordingTool {
	return &recordingTool{name: name, output: "ok:" + name, started: make(chan string, 16)}
}

func (t *recordingTool) Name() string        { return t.name }
func (t *recordingTool) Description() string { return "desc" }
func (t *recordingTool) Label() string       { return t.name }
func (t *recordingTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)
}
func (t *recordingTool) PromptSnippet() string                  { return "" }
func (t *recordingTool) PromptGuidelines() []string             { return nil }
func (t *recordingTool) ExecutionMode() types.ToolExecutionMode { return t.mode }

func (t *recordingTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate types.UpdateFunc) (*types.ToolResult, error) {
	t.started <- id
	if t.onStart != nil {
		t.onStart()
	}
	if onUpdate != nil {
		onUpdate(&types.ToolResult{Content: []types.AssistantContent{types.TextContent{Type: "text", Text: "partial"}}})
	}
	t.executions = append(t.executions, id)
	if t.err != nil {
		return nil, t.err
	}
	return &types.ToolResult{
		Content: []types.AssistantContent{types.TextContent{Type: "text", Text: t.output}},
	}, nil
}

func baseRequest(p types.Provider, tools ...types.AgentTool) Request {
	return Request{
		SystemPrompt: "sys",
		Messages:     []types.Message{&types.UserMessage{Content: types.TextOnly("go"), Timestamp: 1}},
		Model:        &types.Model{ID: "m", API: types.APIOpenAICompat, Provider: "p", ContextWindow: 100000},
		Provider:     p,
		Tools:        tools,
	}
}

func collect(t *testing.T, ch <-chan types.AgentEvent) ([]types.AgentEvent, *types.AgentEvent) {
	t.Helper()
	var all []types.AgentEvent
	for ev := range ch {
		all = append(all, ev)
		if ev.Kind == types.AgentEnd {
			return all, &ev
		}
	}
	t.Fatal("channel closed without agent_end")
	return nil, nil
}

func kinds(evs []types.AgentEvent) string {
	s := ""
	for _, e := range evs {
		if s != "" {
			s += ","
		}
		s += string(e.Kind)
	}
	return s
}

// --- tests ---

func TestRunTextOnlySingleTurn(t *testing.T) {
	fp := &fakeProvider{responses: []*types.AssistantMessage{textMsg("done")}}
	evs, end := collect(t, Run(context.Background(), baseRequest(fp)))

	want := "agent_start,turn_start,message_start,message_update,message_end,turn_end,agent_end"
	if got := kinds(evs); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
	if len(end.Messages) != 1 {
		t.Fatalf("agent_end messages = %d", len(end.Messages))
	}
}

func TestRunLoopWithToolCall(t *testing.T) {
	tool := newRecordingTool("read")
	fp := &fakeProvider{responses: []*types.AssistantMessage{
		toolCallMsg(call("c1", "read", `{"x":"y"}`)),
		textMsg("final"),
	}}
	req := baseRequest(fp, tool)
	evs, end := collect(t, Run(context.Background(), req))

	got := kinds(evs)
	for _, want := range []string{"tool_execution_start", "tool_execution_update", "tool_execution_end", "message_end"} {
		if !contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
	if len(tool.executions) != 1 || tool.executions[0] != "c1" {
		t.Fatalf("tool executions: %v", tool.executions)
	}
	if len(end.Messages) != 3 { // assistant + toolResult + assistant
		t.Fatalf("messages = %d: %+v", len(end.Messages), end.Messages)
	}
	trm, ok := end.Messages[1].(*types.ToolResultMessage)
	if !ok || trm.ToolCallID != "c1" || trm.IsError {
		t.Fatalf("tool result message wrong: %+v", end.Messages[1])
	}
	blocks, _ := types.DecodeToolResultContent(trm.Content)
	if types.StringContentOf(blocks) != "ok:read" {
		t.Fatalf("tool result content: %q", types.StringContentOf(blocks))
	}

	// The second provider call must include the tool result in context.
	second := fp.contexts[1]
	if n := len(second.Messages); n == 0 {
		t.Fatal("second call context empty")
	}
	lastIsToolResult := false
	for _, m := range second.Messages {
		if _, ok := m.(*types.ToolResultMessage); ok {
			lastIsToolResult = true
		}
	}
	if !lastIsToolResult {
		t.Fatal("tool result not in second-turn context")
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

func TestUnknownToolFailsWithoutExecuting(t *testing.T) {
	fp := &fakeProvider{responses: []*types.AssistantMessage{
		toolCallMsg(call("c1", "nope", `{}`)),
		textMsg("recovered"),
	}}
	evs, _ := collect(t, Run(context.Background(), baseRequest(fp)))
	var endEv *types.AgentEvent
	var toolRes *types.ToolResultMessage
	for _, e := range evs {
		if e.Kind == types.AgentEnd {
			endEv = &e
		}
		if e.Kind == types.MessageEnd && e.ToolMsg != nil && e.ToolMsg.IsError {
			toolRes = e.ToolMsg
		}
	}
	if endEv == nil || len(endEv.Messages) != 3 {
		t.Fatalf("run should continue after unknown tool: %+v", endEv)
	}
	if toolRes == nil || toolRes.ToolName != "nope" {
		t.Fatalf("expected error tool result for unknown tool: %+v", toolRes)
	}
}

func TestBeforeHookBlocksTool(t *testing.T) {
	tool := newRecordingTool("bash")
	fp := &fakeProvider{responses: []*types.AssistantMessage{
		toolCallMsg(call("c1", "bash", `{}`)),
		textMsg("after block"),
	}}
	req := baseRequest(fp, tool)
	req.BeforeToolCall = func(in BeforeToolCallInput) *BeforeToolCallDecision {
		return &BeforeToolCallDecision{Block: true, Reason: "not allowed"}
	}
	evs, end := collect(t, Run(context.Background(), req))

	if len(tool.executions) != 0 {
		t.Fatal("blocked tool must not execute")
	}
	var trm *types.ToolResultMessage
	for _, e := range evs {
		if e.Kind == types.MessageEnd && e.ToolMsg != nil {
			trm = e.ToolMsg
		}
	}
	if trm == nil || !trm.IsError {
		t.Fatal("blocked call should yield error tool result")
	}
	blocks, _ := types.DecodeToolResultContent(trm.Content)
	if types.StringContentOf(blocks) != "not allowed" {
		t.Fatalf("block reason lost: %q", types.StringContentOf(blocks))
	}
	if len(end.Messages) != 3 {
		t.Fatalf("loop should continue after block: %d msgs", len(end.Messages))
	}
}

func TestAfterHookOverridesResult(t *testing.T) {
	tool := newRecordingTool("edit")
	fp := &fakeProvider{responses: []*types.AssistantMessage{
		toolCallMsg(call("c1", "edit", `{}`)),
		textMsg("done"),
	}}
	req := baseRequest(fp, tool)
	req.AfterToolCall = func(in AfterToolCallInput) *AfterToolCallOverride {
		isErr := true
		return &AfterToolCallOverride{
			Content: []types.AssistantContent{types.TextContent{Type: "text", Text: "replaced"}},
			IsError: &isErr,
		}
	}
	evs, _ := collect(t, Run(context.Background(), req))
	var trm *types.ToolResultMessage
	for _, e := range evs {
		if e.Kind == types.MessageEnd && e.ToolMsg != nil {
			trm = e.ToolMsg
		}
	}
	if trm == nil || !trm.IsError {
		t.Fatal("override isError not applied")
	}
	blocks, _ := types.DecodeToolResultContent(trm.Content)
	if types.StringContentOf(blocks) != "replaced" {
		t.Fatalf("content override not applied: %q", types.StringContentOf(blocks))
	}
}

func TestTerminateStopsLoop(t *testing.T) {
	tool := newRecordingTool("exit")
	fp := &fakeProvider{responses: []*types.AssistantMessage{
		toolCallMsg(call("c1", "exit", `{}`)),
		textMsg("never"),
	}}
	tool.output = "" // replaced below via hook terminate
	req := baseRequest(fp, tool)
	req.AfterToolCall = func(in AfterToolCallInput) *AfterToolCallOverride {
		return &AfterToolCallOverride{Terminate: true}
	}
	evs, end := collect(t, Run(context.Background(), req))
	if fp.calls != 1 {
		t.Fatalf("loop must not call provider again after terminate, calls=%d", fp.calls)
	}
	if kinds(evs) == "" || len(end.Messages) != 2 {
		t.Fatalf("unexpected run shape: %d msgs", len(end.Messages))
	}
}

func TestLengthStopFailsAllToolCalls(t *testing.T) {
	tool := newRecordingTool("write")
	msg := toolCallMsg(call("c1", "write", `{`), call("c2", "write", `{}`))
	msg.StopReason = types.StopLength
	fp := &fakeProvider{responses: []*types.AssistantMessage{msg, textMsg("retry")}}
	evs, _ := collect(t, Run(context.Background(), baseRequest(fp, tool)))

	if len(tool.executions) != 0 {
		t.Fatal("length-stopped tool calls must not execute")
	}
	errorCount := 0
	for _, e := range evs {
		if e.Kind == types.ToolExecutionEnd && e.IsError {
			errorCount++
		}
	}
	if errorCount != 2 {
		t.Fatalf("want 2 failed tool calls, got %d", errorCount)
	}
}

func TestErrorStopEndsRunImmediately(t *testing.T) {
	fp := &fakeProvider{responses: []*types.AssistantMessage{
		{Content: []types.AssistantContent{}, StopReason: types.StopError, ErrorMessage: "boom"},
		textMsg("never"),
	}}
	evs, end := collect(t, Run(context.Background(), baseRequest(fp)))
	if fp.calls != 1 {
		t.Fatalf("calls = %d", fp.calls)
	}
	if got := kinds(evs); !contains(got, "turn_end") || !contains(got, "agent_end") {
		t.Fatalf("events: %s", got)
	}
	if len(end.Messages) != 1 || end.Messages[0].MessageRole() != types.RoleAssistant {
		t.Fatalf("agent_end messages wrong: %+v", end.Messages)
	}
}

func TestSequentialExecutionForcedByToolMode(t *testing.T) {
	slow := newRecordingTool("slow")
	fast := newRecordingTool("fast")
	slow.mode = types.ExecutionSequential
	fp := &fakeProvider{responses: []*types.AssistantMessage{
		toolCallMsg(call("c1", "slow", `{}`), call("c2", "fast", `{}`)),
		textMsg("fin"),
	}}
	evs, _ := collect(t, Run(context.Background(), baseRequest(fp, slow, fast)))

	// Sequential mode: execution starts are ordered and first completes before second starts.
	var order []string
	for _, e := range evs {
		switch e.Kind {
		case types.ToolExecutionStart:
			order = append(order, "start:"+e.ToolName)
		case types.ToolExecutionEnd:
			order = append(order, "end:"+e.ToolName)
		}
	}
	want := []string{"start:slow", "end:slow", "start:fast", "end:fast"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("sequential order wrong: %v", order)
	}
}

// waitTool blocks inside Execute until released, recording entry.
type waitTool struct {
	tool    *recordingTool
	inside  chan struct{}
	release chan struct{}
}

func (w *waitTool) Name() string                           { return w.tool.Name() }
func (w *waitTool) Description() string                    { return w.tool.Description() }
func (w *waitTool) Label() string                          { return w.tool.Label() }
func (w *waitTool) Parameters() json.RawMessage            { return w.tool.Parameters() }
func (w *waitTool) PromptSnippet() string                  { return "" }
func (w *waitTool) PromptGuidelines() []string             { return nil }
func (w *waitTool) ExecutionMode() types.ToolExecutionMode { return types.ExecutionParallel }

func (w *waitTool) Execute(ctx context.Context, id string, args json.RawMessage, onUpdate types.UpdateFunc) (*types.ToolResult, error) {
	w.inside <- struct{}{}
	select {
	case <-w.release:
	case <-ctx.Done():
	}
	return &types.ToolResult{
		Content: []types.AssistantContent{types.TextContent{Type: "text", Text: "ok:" + id}},
	}, nil
}

func TestParallelExecutionOverlaps(t *testing.T) {
	inside := make(chan struct{}, 2)
	release := make(chan struct{})
	a := &waitTool{tool: newRecordingTool("a"), inside: inside, release: release}
	bb := &waitTool{tool: newRecordingTool("b"), inside: inside, release: release}

	fp := &fakeProvider{responses: []*types.AssistantMessage{
		toolCallMsg(call("c1", "a", `{}`), call("c2", "b", `{}`)),
		textMsg("fin"),
	}}

	// Fail the test if the second tool never enters Execute concurrently.
	go func() {
		<-inside
		<-inside
		close(release)
	}()

	evs, end := collect(t, Run(context.Background(), baseRequest(fp, a, bb)))
	starts := 0
	for _, e := range evs {
		if e.Kind == types.ToolExecutionStart {
			starts++
		}
	}
	if starts != 2 || len(end.Messages) != 4 {
		t.Fatalf("starts=%d msgs=%d", starts, len(end.Messages))
	}
}

func TestSteeringQueueInjectsMessages(t *testing.T) {
	fp := &fakeProvider{responses: []*types.AssistantMessage{
		textMsg("first answer"),
		textMsg("second answer"),
	}}
	req := baseRequest(fp)
	drains := 0
	req.GetSteeringMessages = func() []types.Message {
		drains++
		if drains == 2 { // drained once at start, once after turn 1
			return []types.Message{&types.UserMessage{Content: types.TextOnly("steer!"), Timestamp: 9}}
		}
		return nil
	}
	_, end := collect(t, Run(context.Background(), req))
	if fp.calls != 2 {
		t.Fatalf("steering should trigger another turn, calls=%d", fp.calls)
	}
	if len(end.Messages) != 3 { // assistant + steered user msg + assistant
		t.Fatalf("messages = %d", len(end.Messages))
	}
	u, ok := end.Messages[1].(*types.UserMessage)
	if !ok {
		t.Fatalf("second message should be steered user msg, got %T", end.Messages[1])
	}
	if types.StringContentOf(mustDecodeUser(u.Content)) != "steer!" {
		t.Fatal("steered content wrong")
	}
}

func TestFollowUpQueueContinuesAfterStop(t *testing.T) {
	fp := &fakeProvider{responses: []*types.AssistantMessage{
		textMsg("one"),
		textMsg("two"),
	}}
	req := baseRequest(fp)
	followed := false
	req.GetFollowUpMessages = func() []types.Message {
		if followed {
			return nil
		}
		followed = true
		return []types.Message{&types.UserMessage{Content: types.TextOnly("follow-up"), Timestamp: 9}}
	}
	_, end := collect(t, Run(context.Background(), req))
	if fp.calls != 2 || len(end.Messages) != 3 {
		t.Fatalf("follow-up not processed: calls=%d msgs=%d", fp.calls, len(end.Messages))
	}
}

func TestShouldStopAfterTurn(t *testing.T) {
	tool := newRecordingTool("read")
	fp := &fakeProvider{responses: []*types.AssistantMessage{
		toolCallMsg(call("c1", "read", `{}`)),
		textMsg("never reached"),
	}}
	req := baseRequest(fp, tool)
	req.ShouldStopAfterTurn = func(s TurnSummary) bool { return true }
	_, end := collect(t, Run(context.Background(), req))
	if fp.calls != 1 {
		t.Fatalf("should stop after first turn, calls=%d", fp.calls)
	}
	if len(end.Messages) != 2 { // assistant + tool result
		t.Fatalf("messages = %d", len(end.Messages))
	}
}

func TestValidationFailureProducesErrorResult(t *testing.T) {
	tool := newRecordingTool("read")
	fp := &fakeProvider{responses: []*types.AssistantMessage{
		toolCallMsg(call("c1", "read", `{"wrong":true}`)),
		textMsg("ok"),
	}}
	req := baseRequest(fp, tool)
	req.Validate = func(args json.RawMessage, parameters json.RawMessage) error {
		return fmt.Errorf("invalid arguments")
	}
	evs, _ := collect(t, Run(context.Background(), req))
	if len(tool.executions) != 0 {
		t.Fatal("validation failure must prevent execution")
	}
	for _, e := range evs {
		if e.Kind == types.ToolExecutionEnd && !e.IsError {
			t.Fatal("validation failure should produce error result")
		}
	}
}

func TestConvertToLlmAppliedAtBoundary(t *testing.T) {
	fp := &fakeProvider{responses: []*types.AssistantMessage{textMsg("hi")}}
	req := baseRequest(fp)
	req.ConvertToLlm = func(msgs []types.Message) ([]types.Message, error) {
		return append([]types.Message{}, msgs...), nil
	}
	collect(t, Run(context.Background(), req))
	if len(fp.contexts) == 0 || len(fp.contexts[0].Messages) == 0 {
		t.Fatal("provider context empty")
	}
	if fp.contexts[0].SystemPrompt != "sys" {
		t.Fatalf("system prompt missing")
	}
}

func TestAutoCompactTriggersSummary(t *testing.T) {
	tool := newRecordingTool("read")
	fp := &fakeProvider{responses: []*types.AssistantMessage{
		toolCallMsg(call("c1", "read", `{}`)),
		textMsg("two"),
	}}
	longUser := &types.UserMessage{Content: types.TextOnly(strings.Repeat("filler ", 60)), Timestamp: 1}
	req := Request{
		SystemPrompt: "sys",
		Messages:     []types.Message{longUser},
		Model:        &types.Model{ID: "m", API: types.APIOpenAICompat, Provider: "p", ContextWindow: 10},
		Provider:     fp,
		Tools:        []types.AgentTool{tool},
	}
	req.AutoCompact = &AutoCompactConfig{
		ThresholdFrac: 0.5,
		Summarize: func(ctx context.Context, msgs []types.Message) (string, error) {
			return "the summary", nil
		},
	}
	compactedAt := -1
	origSummarize := req.AutoCompact.Summarize
	req.AutoCompact.Summarize = func(ctx context.Context, msgs []types.Message) (string, error) {
		if compactedAt < 0 {
			compactedAt = fp.calls
		}
		return origSummarize(ctx, msgs)
	}
	_, end := collect(t, Run(context.Background(), req))
	if compactedAt != 0 {
		t.Fatalf("compaction should run before the next provider call, ran at call %d", compactedAt)
	}
	// The next turn context should be just the summary user message.
	ctx2 := fp.contexts[1]
	if len(ctx2.Messages) != 1 {
		t.Fatalf("post-compaction context should be [summary], got %d", len(ctx2.Messages))
	}
	u, ok := ctx2.Messages[0].(*types.UserMessage)
	if !ok {
		t.Fatalf("summary should be a user message, got %T", ctx2.Messages[0])
	}
	blocks, _ := types.DecodeUserContent(u.Content)
	text := types.StringContentOf(blocks)
	if !strings.Contains(text, "the summary") || !strings.Contains(text, "<summary>") {
		t.Fatalf("summary framing wrong: %q", text)
	}
	if len(end.Messages) == 0 {
		t.Fatal("no messages produced")
	}
}

func TestContextCancellationAborts(t *testing.T) {
	tool := newRecordingTool("slow")
	fp := &fakeProvider{responses: []*types.AssistantMessage{
		toolCallMsg(call("c1", "slow", `{}`)),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	tool.onStart = cancel
	tool.err = fmt.Errorf("cancelled")
	evs, end := collect(t, Run(ctx, baseRequest(fp, tool)))
	if len(evs) == 0 || end == nil {
		t.Fatal("must still deliver agent_end after cancellation")
	}
	if fp.calls != 1 {
		t.Fatalf("calls=%d", fp.calls)
	}
}

func mustDecodeUser(raw json.RawMessage) []types.AssistantContent {
	blocks, _ := types.DecodeUserContent(raw)
	return blocks
}
