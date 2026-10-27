// Package agent implements the Run event-channel loop: streaming assistant
// turns, tool dispatch with hooks,
// steering/follow-up queues, and opt-in auto-compaction.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// Request configures one Run invocation. Everything is programmatic.
type Request struct {
	SystemPrompt string
	// Messages is the initial conversation context. The run does not mutate it.
	Messages []types.Message
	Model    *types.Model
	Provider types.Provider
	Tools    []types.AgentTool

	ThinkingLevel types.ThinkingLevel
	Temperature   *float64
	MaxTokens     *int64
	APIKey        string

	// ToolExecutionMode forces the batch strategy; "" means parallel unless a
	// called tool declares ExecutionSequential.
	ToolExecutionMode types.ToolExecutionMode

	// Queues drained by the loop (nil = empty).
	GetSteeringMessages func() []types.Message
	GetFollowUpMessages func() []types.Message

	// PrepareRequest runs immediately before each provider stream request (including the first),
	// allowing dynamic adjustment of request parameters without polling queues.
	// Returning nil means no change; returning a new *Request replaces the active configuration.
	PrepareRequest func(*Request) *Request

	// Persist is called incrementally whenever a completed message (user steering/follow-up/compaction-summary,
	// assistant, or tool result) is added to the active message log. A non-nil
	// error terminates the run with a synthetic StopError assistant turn that
	// is event-only — it is emitted to the event stream but NOT written to the
	// session. agent_end.Messages may include the message whose persist failed
	// (it is still recorded there), but no further provider requests or tool
	// executions happen after the failure.
	Persist func(types.Message) error

	// FinishTurn hook runs after an assistant turn (and all its tool executions)
	// completes. Called on normal, error, and aborted turns. Returning FinishTurnEnd
	// causes the loop to stop after turn_end. Returning FinishTurnContinue ensures
	// another assistant turn is started even if no tools were called.
	// For error and aborted turns, the return decision is ignored and the run terminates.
	FinishTurn func(TurnSummary) *FinishTurnDecision

	// PrepareNextTurn runs ONLY when the loop is confirmed to start another assistant turn.
	// It does not run on final/terminating turns. Optional returned messages are appended.
	// turnIndex is the 1-based index of the upcoming turn. Returned messages are
	// appended to the transcript immediately — before this iteration's steering
	// messages, which are drained at the top of the next loop iteration.
	PrepareNextTurn func(turnIndex int) ([]types.Message, error)

	// Hooks.
	BeforeToolCall func(BeforeToolCallInput) *BeforeToolCallDecision
	AfterToolCall  func(AfterToolCallInput) *AfterToolCallOverride

	// Validate checks tool arguments before execution; nil disables validation.
	// Wire tools.ValidateAgainstSchema here for schema checking.
	Validate func(args json.RawMessage, parameters json.RawMessage) error

	// ConvertToLlm transforms context messages at the LLM boundary;
	// nil means identity.
	ConvertToLlm func([]types.Message) ([]types.Message, error)

	// AutoCompact enables token-threshold compaction (opt-in).
	AutoCompact *AutoCompactConfig
}

// AutoCompactConfig estimates context size as chars/4 and compacts through the
// caller-provided Summarize function before crossing ThresholdFrac of the
// model's context window.
type AutoCompactConfig struct {
	ThresholdFrac float64
	Summarize     func(ctx context.Context, msgs []types.Message) (string, error)
}

// TurnSummary describes a completed turn.
type TurnSummary struct {
	Message     *types.AssistantMessage
	ToolResults []*types.ToolResultMessage
}

// FinishTurnAction specifies the action to take after finishing a turn.
type FinishTurnAction string

const (
	FinishTurnEnd      FinishTurnAction = "end"
	FinishTurnContinue FinishTurnAction = "continue"
)

// FinishTurnDecision directs the agent loop whether to continue or stop.
type FinishTurnDecision struct {
	Action FinishTurnAction
}

// BeforeToolCallInput is passed to the BeforeToolCall hook.
type BeforeToolCallInput struct {
	AssistantMessage *types.AssistantMessage
	ToolCall         types.ToolCall
	ValidatedArgs    map[string]any
}

// BeforeToolCallDecision can block execution (optionally terminating the run).
type BeforeToolCallDecision struct {
	Block     bool
	Reason    string
	Terminate bool
}

// AfterToolCallInput is passed to the AfterToolCall hook.
type AfterToolCallInput struct {
	AssistantMessage *types.AssistantMessage
	ToolCall         types.ToolCall
	Result           *types.ToolResult
	IsError          bool
}

// AfterToolCallOverride replaces fields of the final tool result. Nil fields
// keep the original values.
type AfterToolCallOverride struct {
	Content   []types.AssistantContent
	Details   any
	Usage     *types.Usage
	IsError   *bool
	Terminate bool
}

func injectModel(tools []types.AgentTool, m *types.Model) {
	for _, t := range tools {
		if sm, ok := t.(interface{ SetModel(*types.Model) }); ok {
			sm.SetModel(m)
		}
	}
}

// Run starts the agent loop and streams events on a buffered channel that is
// closed after the terminal agent_end event (delivered exactly once, even on
// cancellation).
func Run(ctx context.Context, req Request) <-chan types.AgentEvent {
	injectModel(req.Tools, req.Model)
	ch := make(chan types.AgentEvent, 64)
	go func() {
		defer close(ch)
		l := &loop{
			ctx:         ctx,
			req:         &req,
			messages:    append([]types.Message{}, req.Messages...),
			newMsgs:     []types.Message{},
			toolsByName: toolIndex(req.Tools),
			ch:          ch,
		}
		l.run()
		l.end()
	}()
	return ch
}

func toolIndex(tools []types.AgentTool) map[string]types.AgentTool {
	m := make(map[string]types.AgentTool, len(tools))
	for _, t := range tools {
		m[t.Name()] = t
	}
	return m
}

type loop struct {
	ctx         context.Context
	req         *Request
	messages    []types.Message
	newMsgs     []types.Message
	toolsByName map[string]types.AgentTool
	ch          chan types.AgentEvent
	endSent     bool
	turnOpen    bool // a turn_start is currently unmatched by a turn_end
}

// appendNewMsg records a message for persistence. Callers must only invoke
// it once a message is final and complete: it pairs the transcript append with
// an immediate Persist call, so partially-built messages must not be passed.
// The message is recorded in l.newMsgs exactly once, before Persist runs; a
// Persist failure is returned so the caller can terminate the run instead of
// continuing with model context the session does not have.
func (l *loop) appendNewMsg(m types.Message) error {
	l.newMsgs = append(l.newMsgs, m)
	if l.req.Persist != nil {
		return l.req.Persist(m)
	}
	return nil
}

// failPersist terminates the run after a session persist failure. It
// synthesizes a terminal StopError assistant message and appends it to the
// transcript and agent_end messages (without calling Persist on it — the
// session is already failing, and retrying here could only fail again), then
// emits the closing terminal turn. Synthetic terminal turns bypass FinishTurn:
// the hook is not run for them. The caller must unwind immediately: no further
// turns, tool executions, or terminal events.
func (l *loop) failPersist(perr error) {
	final := &types.AssistantMessage{
		Content:      []types.AssistantContent{},
		API:          l.req.Model.API,
		Provider:     l.req.Model.Provider,
		Model:        l.req.Model.ID,
		StopReason:   types.StopError,
		ErrorMessage: fmt.Sprintf("session persist: %v", perr),
		Timestamp:    time.Now().UnixMilli(),
	}
	l.messages = append(l.messages, final)
	l.newMsgs = append(l.newMsgs, final)
	l.emitTerminalErrorTurn(final)
}

// emitTerminalErrorTurn closes the run with a synthetic StopError assistant
// message: message_start/message_end/turn_end/agent_end. Failures can surface
// after the previous turn_end (between turns), so if no turn is currently
// open, exactly one turn_start is emitted first to keep the stream balanced.
// FinishTurn is intentionally bypassed (see failPersist).
func (l *loop) emitTerminalErrorTurn(final *types.AssistantMessage) {
	if !l.turnOpen {
		l.emitTurnStart()
	}
	l.emitAssistant(final, true)
	l.emitTurnEnd(types.AgentEvent{Message: final})
	l.end()
}

// end delivers the terminal agent_end event exactly once. It bypasses the
// context guard so cancellation still yields a well-formed event stream.
func (l *loop) end() {
	if l.endSent {
		return
	}
	l.endSent = true
	l.ch <- types.AgentEvent{Kind: types.AgentEnd, Messages: l.newMsgs}
}

const compactionPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"
const compactionSuffix = "\n</summary>"

func (l *loop) emit(ev types.AgentEvent) bool {
	select {
	case l.ch <- ev:
		return true
	case <-l.ctx.Done():
		return false
	}
}

// emitTurnStart opens a turn, keeping turnOpen in sync so terminal failure
// paths can tell whether a turn is still open and emit a balanced sequence.
func (l *loop) emitTurnStart() bool {
	if !l.emit(types.AgentEvent{Kind: types.TurnStart}) {
		return false
	}
	l.turnOpen = true
	return true
}

// emitTurnEnd closes the current turn; see emitTurnStart.
func (l *loop) emitTurnEnd(ev types.AgentEvent) {
	ev.Kind = types.TurnEnd
	l.turnOpen = false
	l.emit(ev)
}

func (l *loop) run() {
	l.emit(types.AgentEvent{Kind: types.AgentStart})
	l.emitTurnStart()

	pending := l.drain(l.req.GetSteeringMessages)
	firstTurn := true
	turnIndex := 0

outer:
	for {
		hasMoreToolCalls := true
		for hasMoreToolCalls || len(pending) > 0 {
			if l.ctx.Err() != nil {
				// Cancelled between turns: stop cleanly.
				l.end()
				return
			}
			if !firstTurn {
				if !l.emitTurnStart() {
					return
				}
			} else {
				firstTurn = false
			}

			if l.req.AutoCompact != nil {
				if err := l.maybeCompact(); err != nil {
					// Terminal path: drained steering/pending messages are
					// dropped — the run ends here.
					l.failPersist(err)
					return
				}
				if l.ctx.Err() != nil {
					return
				}
			}

			for _, m := range pending {
				l.emitUserish(m)
				l.messages = append(l.messages, m)
				if err := l.appendNewMsg(m); err != nil {
					l.failPersist(err)
					return
				}
			}

			if l.req.PrepareRequest != nil {
				if nextReq := l.req.PrepareRequest(l.req); nextReq != nil {
					l.req = nextReq
					l.toolsByName = toolIndex(nextReq.Tools)
					injectModel(nextReq.Tools, nextReq.Model)
				}
			}

			msg := l.streamAssistant()
			if msg == nil {
				return // stream aborted/emitted failure already
			}
			if err := l.appendNewMsg(msg); err != nil {
				l.failPersist(err)
				return
			}

			if msg.StopReason == types.StopError || msg.StopReason == types.StopAborted {
				if l.req.FinishTurn != nil {
					l.req.FinishTurn(TurnSummary{Message: msg})
				}
				l.emitTurnEnd(types.AgentEvent{Message: msg})
				l.end()
				return
			}

			var toolCalls []types.ToolCall
			for _, blk := range msg.Content {
				if tc, ok := blk.(types.ToolCall); ok {
					toolCalls = append(toolCalls, tc)
				}
			}

			var toolResults []*types.ToolResultMessage
			hasMoreToolCalls = false
			if len(toolCalls) > 0 {
				var batch []*types.ToolResultMessage
				var terminate bool
				if msg.StopReason == types.StopLength {
					batch, terminate = l.failTruncatedToolCalls(msg, toolCalls)
				} else {
					batch, terminate = l.executeToolCalls(msg, toolCalls)
				}
				toolResults = batch
				hasMoreToolCalls = len(toolCalls) > 0 && !terminate
				for _, r := range batch {
					l.messages = append(l.messages, r)
					if err := l.appendNewMsg(r); err != nil {
						l.failPersist(err)
						return
					}
				}
			}

			var decision *FinishTurnDecision
			if l.req.FinishTurn != nil {
				decision = l.req.FinishTurn(TurnSummary{Message: msg, ToolResults: toolResults})
			}

			l.emitTurnEnd(types.AgentEvent{Message: msg, ToolResults: toolResults})

			if decision != nil {
				if decision.Action == FinishTurnEnd {
					l.end()
					return
				}
				if decision.Action == FinishTurnContinue {
					hasMoreToolCalls = true
				}
			}

			turnIndex++
			pending = l.drain(l.req.GetSteeringMessages)

			// If another assistant turn is confirmed to start (either through remaining tool results /
			// FinishTurnContinue or pending steering messages), trigger PrepareNextTurn.
			if hasMoreToolCalls || len(pending) > 0 {
				if !l.runPrepareNextTurn(turnIndex) {
					return
				}
			}
		}

		followUps := l.drain(l.req.GetFollowUpMessages)
		if len(followUps) > 0 {
			pending = followUps
			if !l.runPrepareNextTurn(turnIndex) {
				return
			}
			continue outer
		}
		break
	}

	l.end()
}

func (l *loop) drain(fn func() []types.Message) []types.Message {
	if fn == nil {
		return nil
	}
	return fn()
}

// runPrepareNextTurn invokes the PrepareNextTurn hook (if set) for the given
// upcoming turn index. On hook error it synthesizes a terminal StopError
// assistant message (folding in a persist failure of that terminal message,
// if any) in a single balanced error turn, ends the run and returns false;
// otherwise it appends any returned messages (terminating the run on a
// persist failure) and returns true.
func (l *loop) runPrepareNextTurn(turnIndex int) bool {
	if l.req.PrepareNextTurn == nil {
		return true
	}
	extraMsgs, err := l.req.PrepareNextTurn(turnIndex)
	if err != nil {
		final := &types.AssistantMessage{
			Content:      []types.AssistantContent{},
			API:          l.req.Model.API,
			Provider:     l.req.Model.Provider,
			Model:        l.req.Model.ID,
			StopReason:   types.StopError,
			ErrorMessage: err.Error(),
			Timestamp:    time.Now().UnixMilli(),
		}
		l.messages = append(l.messages, final)
		// Fold a persist failure of this terminal message into the same error
		// turn instead of emitting a second one.
		if perr := l.appendNewMsg(final); perr != nil {
			final.ErrorMessage = fmt.Sprintf("%v; session persist: %v", err, perr)
		}
		l.emitTerminalErrorTurn(final)
		return false
	}
	for _, m := range extraMsgs {
		l.emitUserish(m)
		l.messages = append(l.messages, m)
		if err := l.appendNewMsg(m); err != nil {
			l.failPersist(err)
			return false
		}
	}
	return true
}

func (l *loop) emitUserish(m types.Message) {
	ev := types.AgentEvent{Kind: types.MessageStart}
	switch t := m.(type) {
	case *types.UserMessage:
		ev.UserMsg = t
	case *types.ToolResultMessage:
		ev.ToolMsg = t
	case *types.AssistantMessage:
		ev.Message = t
	}
	l.emit(ev)
	ev.Kind = types.MessageEnd
	l.emit(ev)
}

// snapshotAssistant returns a copy of m that owns its top-level struct and
// Content slice. Emitted agent events must carry snapshots: the provider
// goroutine keeps mutating the live partial message, and consumers reading an
// aliased struct would observe torn, still-changing state. The copy is
// shallow beyond that: nested slices and pointers (e.g. ToolCall.Arguments,
// Usage.Reasoning, EndTurn) are still shared with the live message, so
// providers must treat block payloads as immutable once the event is emitted.
// l.messages keeps pointing at the live partial so provider updates and the
// final replacement still work.
func snapshotAssistant(m *types.AssistantMessage) *types.AssistantMessage {
	if m == nil {
		return nil
	}
	cp := *m
	// append to a non-nil empty slice so an empty Content stays [] not nil.
	cp.Content = append([]types.AssistantContent{}, m.Content...)
	return &cp
}

// snapshotEvent copies a stream Partial so the emitted AgentEvent references
// only the snapshot message (plus a copied ToolCall). It returns the message
// snapshot separately so callers can share it between AgentEvent.Message and
// AssistantEv.Partial instead of copying twice. See snapshotAssistant for the
// nested-shared-state caveat.
func snapshotEvent(e types.Partial) (*types.AssistantMessage, *types.AssistantMessageEvent) {
	msg := snapshotAssistant(e.Partial)
	e.Partial = msg
	if e.ToolCall != nil {
		tc := *e.ToolCall
		e.ToolCall = &tc
	}
	var ev types.AssistantMessageEvent = e
	return msg, &ev
}

// streamAssistant runs one provider stream, forwarding protocol events as
// agent message_* events. Returns the final assistant message, or nil when
// the run was aborted while emitting.
func (l *loop) streamAssistant() *types.AssistantMessage {
	llmMessages := l.messages
	if l.req.ConvertToLlm != nil {
		converted, err := l.req.ConvertToLlm(l.messages)
		if err != nil {
			final := &types.AssistantMessage{
				Content:      []types.AssistantContent{},
				API:          l.req.Model.API,
				Provider:     l.req.Model.Provider,
				Model:        l.req.Model.ID,
				StopReason:   types.StopError,
				ErrorMessage: err.Error(),
				Timestamp:    time.Now().UnixMilli(),
			}
			l.emitAssistant(final, true)
			return final
		}
		llmMessages = converted
	}
	cx := &types.Context{
		SystemPrompt: l.req.SystemPrompt,
		Messages:     llmMessages,
		Tools:        toolDefs(l.req.Tools),
	}
	opts := &types.StreamOptions{
		Temperature:   l.req.Temperature,
		MaxTokens:     l.req.MaxTokens,
		ThinkingLevel: l.req.ThinkingLevel,
		APIKey:        l.req.APIKey,
	}

	events := l.req.Provider.Stream(l.ctx, l.req.Model, cx, opts)
	var partial *types.AssistantMessage
	started := false
	for ev := range events {
		switch e := ev.(type) {
		case types.Partial:
			partial = e.Partial
			switch e.Kind {
			case types.EvStart:
				started = true
				l.messages = append(l.messages, partial)
				// One snapshot shared by Message and AssistantEv.Partial.
				snap, ae := snapshotEvent(e)
				l.emit(types.AgentEvent{Kind: types.MessageStart, Message: snap, AssistantEv: ae})
			default:
				if started && partial != nil {
					l.messages[len(l.messages)-1] = partial
					snap, ae := snapshotEvent(e)
					l.emit(types.AgentEvent{Kind: types.MessageUpdate, Message: snap, AssistantEv: ae})
				}
			}
		case types.DoneEvent:
			final := e.Message
			if final == nil {
				final = partial
			}
			started = l.replaceOrAppendPartial(started, final)
			l.emitAssistant(final, !started)
			return final
		case types.StreamErrorEvent:
			final := e.Message
			if final == nil {
				final = partial
			}
			started = l.replaceOrAppendPartial(started, final)
			l.emitAssistant(final, !started)
			return final
		}
	}
	// Provider closed without a terminal event.
	if partial == nil {
		return nil
	}
	partial.StopReason = types.StopError
	partial.ErrorMessage = "provider stream ended without a terminal event"
	started = l.replaceOrAppendPartial(started, partial)
	l.emitAssistant(partial, !started)
	return partial
}

// replaceOrAppendPartial swaps the in-flight partial for the final message,
// returning whether the message had been announced with message_start.
func (l *loop) replaceOrAppendPartial(started bool, final *types.AssistantMessage) bool {
	if started && len(l.messages) > 0 {
		if _, ok := l.messages[len(l.messages)-1].(*types.AssistantMessage); ok {
			l.messages[len(l.messages)-1] = final
			return true
		}
	}
	l.messages = append(l.messages, final)
	return false
}

func (l *loop) emitAssistant(m *types.AssistantMessage, emitStart bool) {
	if emitStart {
		l.emit(types.AgentEvent{Kind: types.MessageStart, Message: m})
	}
	l.emit(types.AgentEvent{Kind: types.MessageEnd, Message: m})
}

func toolDefs(tools []types.AgentTool) []types.ToolDef {
	defs := make([]types.ToolDef, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters()
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		defs = append(defs, types.ToolDef{Name: t.Name(), Description: t.Description(), Parameters: params})
	}
	return defs
}

func errorToolResult(message string) *types.ToolResult {
	return &types.ToolResult{
		Content: []types.AssistantContent{types.TextContent{Type: types.TypeText, Text: message}},
		Details: map[string]any{},
		IsError: true,
	}
}

// failTruncatedToolCalls reports every tool call from a length-stopped
// message as an error so the model re-issues them with complete arguments.
func (l *loop) failTruncatedToolCalls(msg *types.AssistantMessage, calls []types.ToolCall) ([]*types.ToolResultMessage, bool) {
	var out []*types.ToolResultMessage
	for _, tc := range calls {
		l.emit(types.AgentEvent{Kind: types.ToolExecutionStart, ToolCallID: tc.ID, ToolName: tc.Name, Args: tc.Arguments})
		result := errorToolResult(fmt.Sprintf(
			"Tool call %q was not executed: the response hit the output token limit, so its arguments may be truncated. Re-issue the tool call with complete arguments.", tc.Name))
		out = append(out, l.finishToolCall(msg, tc, result, true))
	}
	return out, false
}

// finishToolCall applies the AfterToolCall hook and emits the terminal events
// for one finalized tool call.
func (l *loop) finishToolCall(msg *types.AssistantMessage, tc types.ToolCall, result *types.ToolResult, isError bool) *types.ToolResultMessage {
	if l.req.AfterToolCall != nil {
		override := l.req.AfterToolCall(AfterToolCallInput{
			AssistantMessage: msg,
			ToolCall:         tc,
			Result:           result,
			IsError:          isError,
		})
		if override != nil {
			if override.Content != nil {
				result.Content = override.Content
			}
			if override.Details != nil {
				result.Details = override.Details
			}
			if override.Usage != nil {
				result.Usage = override.Usage
			}
			if override.IsError != nil {
				isError = *override.IsError
			}
			if override.Terminate {
				result.Terminate = true
			}
		}
	}

	l.emit(types.AgentEvent{
		Kind: types.ToolExecutionEnd, ToolCallID: tc.ID, ToolName: tc.Name,
		Result: result, IsError: isError,
	})

	contentRaw, _ := types.MarshalBlocks(result.Content)
	detailsRaw, _ := json.Marshal(result.Details)
	trm := &types.ToolResultMessage{
		ToolCallID: tc.ID,
		ToolName:   tc.Name,
		Content:    contentRaw,
		Details:    detailsRaw,
		Usage:      result.Usage,
		IsError:    isError,
		Timestamp:  time.Now().UnixMilli(),
	}
	l.emit(types.AgentEvent{Kind: types.MessageStart, ToolMsg: trm})
	l.emit(types.AgentEvent{Kind: types.MessageEnd, ToolMsg: trm})
	return trm
}

// prepare validates args and runs the BeforeToolCall hook. Returns either a
// prepared execution or an immediate (failed/blocked) outcome.
func (l *loop) prepare(msg *types.AssistantMessage, tc types.ToolCall) (*preparedCall, *immediateOutcome) {
	tool := l.toolsByName[tc.Name]
	if tool == nil {
		return nil, &immediateOutcome{result: errorToolResult("Tool " + tc.Name + " not found"), isError: true}
	}
	// Port of pi 87f9a3641: malformed final arguments never reach validation
	// or execution; the raw text is reported back to the model instead.
	if tc.ArgumentsError != "" {
		return nil, &immediateOutcome{
			result:  errorToolResult(fmt.Sprintf("Invalid arguments for tool %q: %s", tc.Name, tc.ArgumentsError)),
			isError: true,
		}
	}
	var validated map[string]any
	if l.req.Validate != nil {
		if err := l.req.Validate(tc.Arguments, tool.Parameters()); err != nil {
			return nil, &immediateOutcome{result: errorToolResult(err.Error()), isError: true}
		}
	}
	if len(tc.Arguments) > 0 {
		var m map[string]any
		if json.Unmarshal(tc.Arguments, &m) == nil {
			validated = m
		}
	}
	if l.req.BeforeToolCall != nil {
		decision := l.req.BeforeToolCall(BeforeToolCallInput{
			AssistantMessage: msg,
			ToolCall:         tc,
			ValidatedArgs:    validated,
		})
		if decision != nil && decision.Block {
			result := errorToolResult(orStr(decision.Reason, "Tool execution was blocked"))
			if decision.Terminate {
				result.Terminate = true
			}
			return nil, &immediateOutcome{result: result, isError: true}
		}
	}
	if l.ctx.Err() != nil {
		return nil, &immediateOutcome{result: errorToolResult("Operation aborted"), isError: true}
	}
	return &preparedCall{tc: tc, tool: tool}, nil
}

type preparedCall struct {
	tc   types.ToolCall
	tool types.AgentTool
}

type immediateOutcome struct {
	result  *types.ToolResult
	isError bool
}

func (l *loop) executePrepared(p *preparedCall) (*types.ToolResult, bool) {
	result, err := p.tool.Execute(l.ctx, p.tc.ID, p.tc.Arguments, func(partial *types.ToolResult) {
		l.emit(types.AgentEvent{
			Kind: types.ToolExecutionUpdate, ToolCallID: p.tc.ID, ToolName: p.tc.Name,
			Args: p.tc.Arguments, PartialResult: partial,
		})
	})
	if err != nil {
		msg := err.Error()
		if msg == "" {
			msg = "tool execution failed"
		}
		return errorToolResult(msg), true
	}
	if result == nil {
		return errorToolResult("tool returned no result"), true
	}
	return result, result.IsError
}

func (l *loop) executeToolCalls(msg *types.AssistantMessage, calls []types.ToolCall) ([]*types.ToolResultMessage, bool) {
	sequential := l.req.ToolExecutionMode == types.ExecutionSequential
	for _, tc := range calls {
		if t := l.toolsByName[tc.Name]; t != nil && t.ExecutionMode() == types.ExecutionSequential {
			sequential = true
		}
	}
	if sequential {
		return l.executeSequential(msg, calls)
	}
	return l.executeParallel(msg, calls)
}

func (l *loop) executeSequential(msg *types.AssistantMessage, calls []types.ToolCall) ([]*types.ToolResultMessage, bool) {
	var out []*types.ToolResultMessage
	var terminatedAll = true
	sawAny := false
	for _, tc := range calls {
		l.emit(types.AgentEvent{Kind: types.ToolExecutionStart, ToolCallID: tc.ID, ToolName: tc.Name, Args: tc.Arguments})
		prepared, immediate := l.prepare(msg, tc)
		var result *types.ToolResult
		var isErr bool
		if immediate != nil {
			result, isErr = immediate.result, immediate.isError
		} else {
			result, isErr = l.executePrepared(prepared)
		}
		trm := l.finishToolCall(msg, tc, result, isErr)
		out = append(out, trm)
		sawAny = true
		if !result.Terminate {
			terminatedAll = false
		}
		if l.ctx.Err() != nil {
			break
		}
	}
	return out, sawAny && terminatedAll
}

func (l *loop) executeParallel(msg *types.AssistantMessage, calls []types.ToolCall) ([]*types.ToolResultMessage, bool) {
	type slot struct {
		immediate *immediateOutcome
		prepared  *preparedCall
		done      chan struct{}
		result    *types.ToolResult
		isError   bool
	}
	slots := make([]*slot, len(calls))
	for i, tc := range calls {
		l.emit(types.AgentEvent{Kind: types.ToolExecutionStart, ToolCallID: tc.ID, ToolName: tc.Name, Args: tc.Arguments})
		s := &slot{}
		prepared, immediate := l.prepare(msg, tc)
		s.immediate = immediate
		s.prepared = prepared
		slots[i] = s
		if immediate == nil {
			s.done = make(chan struct{})
			go func(s *slot, p *preparedCall) {
				defer close(s.done)
				s.result, s.isError = l.executePrepared(p)
			}(s, prepared)
		}
	}

	allTerminated := len(calls) > 0
	out := make([]*types.ToolResultMessage, 0, len(calls))
	for i, tc := range calls {
		s := slots[i]
		if s.done != nil {
			<-s.done
		}
		var result *types.ToolResult
		var isErr bool
		if s.immediate != nil {
			result, isErr = s.immediate.result, s.immediate.isError
		} else {
			result, isErr = s.result, s.isError
		}
		trm := l.finishToolCall(msg, tc, result, isErr)
		out = append(out, trm)
		if !result.Terminate {
			allTerminated = false
		}
	}
	return out, allTerminated
}

// maybeCompact summarizes the transcript when the chars/4 estimate crosses
// ThresholdFrac of the model context window. On success the working context
// becomes just the summary user message. Summarize failures are ignored (the
// next turn retries); a summary that cannot be persisted is returned as an
// error so the run stops instead of continuing without the session write.
func (l *loop) maybeCompact() error {
	cfg := l.req.AutoCompact
	if cfg == nil || cfg.Summarize == nil || l.req.Model.ContextWindow <= 0 {
		return nil
	}
	total := 0
	for _, m := range l.messages {
		total += messageCharLen(m)
	}
	estimated := total / 4
	frac := cfg.ThresholdFrac
	if frac <= 0 {
		frac = 0.8
	}
	if int64(estimated) < int64(float64(l.req.Model.ContextWindow)*frac) {
		return nil
	}
	summary, err := cfg.Summarize(l.ctx, l.messages)
	if err != nil || strings.TrimSpace(summary) == "" {
		return nil
	}
	raw, _ := types.MarshalBlocks([]types.AssistantContent{
		types.TextContent{Type: types.TypeText, Text: compactionPrefix + summary + compactionSuffix},
	})
	sum := &types.UserMessage{Content: raw, Timestamp: time.Now().UnixMilli()}
	l.messages = []types.Message{sum}
	return l.appendNewMsg(sum)
}

func messageCharLen(m types.Message) int {
	switch t := m.(type) {
	case *types.UserMessage:
		blocks, err := types.DecodeUserContent(t.Content)
		if err != nil {
			return 0
		}
		return len(types.StringContentOf(blocks))
	case *types.AssistantMessage:
		n := 0
		for _, blk := range t.Content {
			switch b := blk.(type) {
			case types.TextContent:
				n += len(b.Text)
			case types.ThinkingContent:
				n += len(b.Thinking)
			case types.ToolCall:
				n += len(b.Arguments)
			}
		}
		return n
	case *types.ToolResultMessage:
		blocks, err := types.DecodeToolResultContent(t.Content)
		if err != nil {
			return 0
		}
		return len(types.StringContentOf(blocks))
	}
	return 0
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
