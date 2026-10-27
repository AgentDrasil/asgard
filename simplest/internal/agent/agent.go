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
	// A non-nil Messages on the returned Request replaces l.messages immediately, so it
	// applies to this and every subsequent provider request until replaced again (an
	// empty non-nil slice clears the transcript); nil Messages leaves it unchanged.
	// l.newMsgs/agent_end.Messages are not affected.
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

	// PrepareNextTurnContext runs ONLY when the loop is confirmed to start another
	// assistant turn. It receives the completed turn's context and may replace the
	// transcript, append messages, switch the model, or change the thinking level.
	// Append messages are persisted immediately but emitted after the next
	// turn_start and before that turn's steering messages. On terminal paths
	// (cancellation or persist failure) they may be persisted without being emitted.
	// When set, it takes precedence over PrepareNextTurn.
	PrepareNextTurnContext func(TurnContext) (*TurnUpdate, error)

	// PrepareNextTurn is the legacy next-turn hook. It runs only when
	// PrepareNextTurnContext is nil; returned messages are treated as
	// TurnUpdate.Append. turnIndex is the 0-based index of the upcoming turn.
	//
	// Deprecated: use PrepareNextTurnContext for access to the turn context and
	// the ability to replace messages, model, or thinking level.
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

// TurnContext describes the turn that just completed. It is passed to
// Request.PrepareNextTurnContext before the next assistant turn starts.
type TurnContext struct {
	Message     *types.AssistantMessage    // last assistant message in the transcript
	ToolResults []*types.ToolResultMessage // tool results after Message
	Messages    []types.Message            // copy of the active transcript
	NewMessages []types.Message            // copy of messages produced by this run
}

// TurnUpdate is returned by Request.PrepareNextTurnContext to adjust the
// upcoming turn.
type TurnUpdate struct {
	// Messages non-nil replaces the active transcript (nil means unchanged; an
	// empty non-nil slice clears it).
	Messages []types.Message
	// Append messages are added to the transcript, persisted, and emitted after
	// the next turn_start, like legacy PrepareNextTurn messages.
	Append []types.Message
	// Model nil means unchanged; otherwise it replaces the active model and is
	// re-injected into tools.
	Model *types.Model
	// ThinkingLevel nil means unchanged.
	ThinkingLevel *types.ThinkingLevel
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
	Context          context.Context
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
	Context          context.Context
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
	prepared    []types.Message // prepared next-turn messages awaiting emission
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
		Content:       []types.AssistantContent{},
		API:           l.req.Model.API,
		Provider:      l.req.Model.Provider,
		Model:         l.req.Model.ID,
		StopReason:    types.StopError,
		ErrorMessage:  fmt.Sprintf("session persist: %v", perr),
		ThinkingLevel: l.thinkingLevel(),
		Timestamp:     time.Now().UnixMilli(),
	}
	l.messages = append(l.messages, final)
	l.newMsgs = append(l.newMsgs, final)
	l.emitTerminalTurn(final)
}

// failAborted synthesizes an event-only StopAborted assistant message and
// closes the run with a balanced terminal turn. The message is recorded in the
// transcript and agent_end messages but never persisted. FinishTurn is
// intentionally bypassed for synthetic abort turns.
func (l *loop) failAborted(reason string) {
	if reason == "" {
		reason = "run aborted"
	}
	final := &types.AssistantMessage{
		Content:       []types.AssistantContent{},
		API:           l.req.Model.API,
		Provider:      l.req.Model.Provider,
		Model:         l.req.Model.ID,
		StopReason:    types.StopAborted,
		ErrorMessage:  reason,
		ThinkingLevel: l.thinkingLevel(),
		Timestamp:     time.Now().UnixMilli(),
	}
	l.messages = append(l.messages, final)
	l.newMsgs = append(l.newMsgs, final)
	l.emitTerminalTurn(final)
}

// abortReason reports the cancellation cause for an aborted terminal turn.
func (l *loop) abortReason() string {
	if err := l.ctx.Err(); err != nil {
		return err.Error()
	}
	return "run aborted"
}

// emitTerminalTurn closes the run with a synthetic terminal assistant message:
// message_start/message_end/turn_end/agent_end. Failures can surface after the
// previous turn_end (between turns), so if no turn is currently open, exactly
// one turn_start is emitted first to keep the stream balanced. Terminal events
// bypass the cancellation guard so a canceled run still yields a well-formed
// stream. FinishTurn is intentionally bypassed (see failPersist/failAborted).
func (l *loop) emitTerminalTurn(final *types.AssistantMessage) {
	if !l.turnOpen {
		l.ch <- types.AgentEvent{Kind: types.TurnStart}
		l.turnOpen = true
	}
	l.ch <- types.AgentEvent{Kind: types.MessageStart, Message: final}
	l.ch <- types.AgentEvent{Kind: types.MessageEnd, Message: final}
	l.ch <- types.AgentEvent{Kind: types.TurnEnd, Message: final}
	l.turnOpen = false
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
	// Terminal event: bypass the cancellation guard so consumers can always
	// close the turn.
	l.ch <- ev
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
				l.failAborted(l.abortReason())
				return
			}
			if !firstTurn {
				if !l.emitTurnStart() {
					l.failAborted(l.abortReason())
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
					l.failAborted(l.abortReason())
					return
				}
			}

			// Prepared next-turn messages were appended/persisted by
			// runPrepareNextTurn; emit them first, then this turn's steering.
			for _, m := range l.prepared {
				l.emitUserish(m)
			}
			l.prepared = nil

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
					if nextReq.Messages != nil {
						// Non-nil Messages replaces the provider-facing transcript
						// for this request; newMsgs is untouched.
						l.messages = append([]types.Message{}, nextReq.Messages...)
					}
					l.toolsByName = toolIndex(nextReq.Tools)
					injectModel(nextReq.Tools, nextReq.Model)
				}
			}

			msg := l.streamAssistant()
			if msg == nil {
				if l.ctx.Err() != nil {
					l.failAborted(l.abortReason())
				}
				return
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

			// FinishTurn may cancel the run. Close the open turn with a
			// synthetic StopAborted message instead of ending bare.
			if l.ctx.Err() != nil {
				l.failAborted(l.abortReason())
				return
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
				// A slow hook can let the user queue steering. Only re-poll when
				// the earlier drain returned nothing, preserving one-at-a-time
				// steering delivery.
				if len(pending) == 0 {
					pending = l.drain(l.req.GetSteeringMessages)
				}
			}
		}

		followUps := l.drain(l.req.GetFollowUpMessages)
		if len(followUps) > 0 {
			pending = followUps
			if !l.runPrepareNextTurn(turnIndex) {
				return
			}
			// The inner loop only exits here when the earlier steering drain was
			// empty, so re-poll once for steering queued during the hook and put
			// it before follow-ups (prepared -> steering -> follow-up). One
			// poll keeps one-at-a-time steering delivery.
			if steering := l.drain(l.req.GetSteeringMessages); len(steering) > 0 {
				pending = append(append([]types.Message{}, steering...), followUps...)
			}
			continue outer
		}
		break
	}

	if l.ctx.Err() != nil {
		l.failAborted(l.abortReason())
		return
	}
	l.end()
}

func (l *loop) drain(fn func() []types.Message) []types.Message {
	if fn == nil {
		return nil
	}
	return fn()
}

// turnContext snapshots the just-completed turn for PrepareNextTurnContext.
// The copies are taken before the hook runs, so messages appended by the hook
// are not visible in the same TurnContext.
func (l *loop) turnContext() TurnContext {
	tc := TurnContext{
		Messages:    append([]types.Message{}, l.messages...),
		NewMessages: append([]types.Message{}, l.newMsgs...),
	}
	for i := len(l.messages) - 1; i >= 0; i-- {
		am, ok := l.messages[i].(*types.AssistantMessage)
		if !ok {
			continue
		}
		tc.Message = am
		for _, m := range l.messages[i+1:] {
			if tr, ok := m.(*types.ToolResultMessage); ok {
				tc.ToolResults = append(tc.ToolResults, tr)
			}
		}
		break
	}
	return tc
}

// runPrepareNextTurn invokes the context-rich hook, or its legacy shim, for the
// upcoming turn. On hook error it synthesizes a terminal StopError assistant
// message (folding in a persist failure of that terminal message, if any) in a
// single balanced error turn, ends the run and returns false; otherwise it
// applies the update (terminating the run on a persist failure) and returns true.
func (l *loop) runPrepareNextTurn(turnIndex int) bool {
	var (
		update *TurnUpdate
		err    error
	)
	switch {
	case l.req.PrepareNextTurnContext != nil:
		update, err = l.req.PrepareNextTurnContext(l.turnContext())
	case l.req.PrepareNextTurn != nil:
		var msgs []types.Message
		msgs, err = l.req.PrepareNextTurn(turnIndex)
		if err == nil {
			update = &TurnUpdate{Append: msgs}
		}
	default:
		return true
	}
	if err != nil {
		l.failPrepareNextTurn(err)
		return false
	}
	if update == nil {
		return true
	}
	if update.Messages != nil {
		l.messages = append([]types.Message{}, update.Messages...)
	}
	for _, m := range update.Append {
		l.messages = append(l.messages, m)
		if err := l.appendNewMsg(m); err != nil {
			l.failPersist(err)
			return false
		}
		l.prepared = append(l.prepared, m)
	}
	if update.Model != nil {
		l.req.Model = update.Model
		injectModel(l.req.Tools, update.Model)
	}
	if update.ThinkingLevel != nil {
		l.req.ThinkingLevel = *update.ThinkingLevel
	}
	return true
}

// failPrepareNextTurn closes the run with a synthetic terminal error turn.
func (l *loop) failPrepareNextTurn(err error) {
	final := &types.AssistantMessage{
		Content:       []types.AssistantContent{},
		API:           l.req.Model.API,
		Provider:      l.req.Model.Provider,
		Model:         l.req.Model.ID,
		StopReason:    types.StopError,
		ErrorMessage:  err.Error(),
		ThinkingLevel: l.thinkingLevel(),
		Timestamp:     time.Now().UnixMilli(),
	}
	l.messages = append(l.messages, final)
	// Fold a persist failure of this terminal message into the same error
	// turn instead of emitting a second one.
	if perr := l.appendNewMsg(final); perr != nil {
		final.ErrorMessage = fmt.Sprintf("%v; session persist: %v", err, perr)
	}
	l.emitTerminalTurn(final)
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
		var (
			converted []types.Message
			cerr      error
			recovered any
		)
		func() {
			defer func() { recovered = recover() }()
			converted, cerr = l.req.ConvertToLlm(l.messages)
		}()
		if recovered != nil {
			cerr = fmt.Errorf("ConvertToLlm panicked: %v", recovered)
		}
		if cerr != nil {
			final := &types.AssistantMessage{
				Content:       []types.AssistantContent{},
				API:           l.req.Model.API,
				Provider:      l.req.Model.Provider,
				Model:         l.req.Model.ID,
				StopReason:    types.StopError,
				ErrorMessage:  cerr.Error(),
				ThinkingLevel: l.thinkingLevel(),
				Timestamp:     time.Now().UnixMilli(),
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

	startTime := time.Now()
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
			l.markAssistantRunMeta(final, startTime)
			started = l.replaceOrAppendPartial(started, final)
			l.emitAssistant(final, !started)
			return final
		case types.StreamErrorEvent:
			final := e.Message
			if final == nil {
				final = partial
			}
			l.markAssistantRunMeta(final, startTime)
			started = l.replaceOrAppendPartial(started, final)
			l.emitAssistant(final, !started)
			return final
		}
	}
	// Provider closed without a terminal event.
	if partial == nil {
		return nil
	}
	if l.ctx.Err() != nil {
		partial.StopReason = types.StopAborted
		partial.ErrorMessage = l.abortReason()
	} else {
		partial.StopReason = types.StopError
		partial.ErrorMessage = "provider stream ended without a terminal event"
	}
	l.markAssistantRunMeta(partial, startTime)
	started = l.replaceOrAppendPartial(started, partial)
	l.emitAssistant(partial, !started)
	return partial
}

// thinkingLevel normalizes the requested thinking level for a turn: an empty
// request means ThinkingOff.
func (l *loop) thinkingLevel() types.ThinkingLevel {
	if l.req.ThinkingLevel == "" {
		return types.ThinkingOff
	}
	return l.req.ThinkingLevel
}

// markAssistantRunMeta records the requested thinking level (empty becomes
// ThinkingOff) and, when appropriate, the elapsed provider request duration on
// the provider-owned final assistant message. ThinkingLevel is always written
// (B10). DurationMs is only filled in when the provider did not supply one and
// the response was timestamped at or after this request started, so a stale or
// reused response never gets a misleading duration. It mutates the final
// message in place; the loop owns that message once the stream terminates.
// Port of upstream B10 + pi 36a686ee8.
func (l *loop) markAssistantRunMeta(final *types.AssistantMessage, start time.Time) {
	if final == nil {
		return
	}
	final.ThinkingLevel = l.thinkingLevel()
	if final.DurationMs == nil && final.Timestamp >= start.UnixMilli() {
		ms := time.Since(start).Milliseconds()
		final.DurationMs = &ms
	}
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

const emptyToolParameters = `{"type":"object","properties":{}}`

func toolDefs(tools []types.AgentTool) []types.ToolDef {
	defs := make([]types.ToolDef, 0, len(tools))
	for _, t := range tools {
		defs = append(defs, types.ToolDef{
			Name: t.Name(), Description: t.Description(), Parameters: safeToolParameters(t),
		})
	}
	return defs
}

// safeToolParameters guards the tool list build: a panicking Parameters()
// implementation must not kill the host before prepare's recover can run. The
// tool stays listed with an empty-object schema.
func safeToolParameters(t types.AgentTool) (params json.RawMessage) {
	defer func() {
		if r := recover(); r != nil {
			params = json.RawMessage(emptyToolParameters)
		}
	}()
	params = t.Parameters()
	if len(params) == 0 {
		params = json.RawMessage(emptyToolParameters)
	}
	return params
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
		out = append(out, l.finishToolCall(msg, tc, result, true, nil))
	}
	return out, false
}

// finalizeToolCall applies the AfterToolCall hook and emits ToolExecutionEnd
// (carrying durationMs when the tool ran), building the ToolResultMessage. It
// does not emit the tool-result message_start/message_end pair; call
// emitToolResultMessage for that (finishToolCall does both). The hook is
// always invoked from the loop goroutine so overrides stay serialized.
func (l *loop) finalizeToolCall(msg *types.AssistantMessage, tc types.ToolCall, result *types.ToolResult, isError bool, durationMs *int64) *types.ToolResultMessage {
	if l.req.AfterToolCall != nil {
		var override *AfterToolCallOverride
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			override = l.req.AfterToolCall(AfterToolCallInput{
				Context:          l.ctx,
				AssistantMessage: msg,
				ToolCall:         tc,
				Result:           result,
				IsError:          isError,
			})
		}()
		if recovered != nil {
			// Port of upstream finalizeExecutedToolCall: a panicking hook
			// replaces the result with an error but keeps the run and the
			// tool's measured duration.
			result = errorToolResult(fmt.Sprintf("afterToolCall panicked: %v", recovered))
			isError = true
		} else if override != nil {
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
		Result: result, IsError: isError, DurationMs: durationMs,
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
		DurationMs: durationMs,
		Timestamp:  time.Now().UnixMilli(),
	}
	return trm
}

// emitToolResultMessage emits the tool-result message_start/message_end pair.
func (l *loop) emitToolResultMessage(trm *types.ToolResultMessage) {
	l.emit(types.AgentEvent{Kind: types.MessageStart, ToolMsg: trm})
	l.emit(types.AgentEvent{Kind: types.MessageEnd, ToolMsg: trm})
}

// finishToolCall finalizes one tool call and immediately emits its tool-result
// messages. Used by sequential execution and failTruncatedToolCalls, whose
// event order is naturally source order.
func (l *loop) finishToolCall(msg *types.AssistantMessage, tc types.ToolCall, result *types.ToolResult, isError bool, durationMs *int64) *types.ToolResultMessage {
	trm := l.finalizeToolCall(msg, tc, result, isError, durationMs)
	l.emitToolResultMessage(trm)
	return trm
}

// prepare validates args and runs the BeforeToolCall hook. Returns either a
// prepared execution or an immediate (failed/blocked) outcome.
func (l *loop) prepare(msg *types.AssistantMessage, tc types.ToolCall) (prepared *preparedCall, immediate *immediateOutcome) {
	// A panicking Validate or BeforeToolCall hook must not kill the host
	// process or run the tool. Mirror upstream prepareToolCall: convert the
	// panic into an immediate error result.
	defer func() {
		if r := recover(); r != nil {
			prepared = nil
			immediate = &immediateOutcome{
				result:  errorToolResult(fmt.Sprintf("tool preparation panicked: %v", r)),
				isError: true,
			}
		}
	}()

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
			Context:          l.ctx,
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

func (l *loop) executePrepared(p *preparedCall) (result *types.ToolResult, isError bool, durationMs *int64) {
	if l.ctx.Err() != nil {
		// Upstream v0.99.2: every deferred thunk re-checks the abort signal
		// before running the tool, so cancellation cannot start prepared work.
		// The tool did not run: no duration.
		return errorToolResult("Operation aborted"), true, nil
	}
	start := time.Now()
	// A panicking Execute (or its onUpdate callback) becomes a normal tool
	// error. The tool did run, so the duration stays non-nil. Mirror upstream
	// executePreparedToolCall.
	defer func() {
		if r := recover(); r != nil {
			d := time.Since(start).Milliseconds()
			result = errorToolResult(fmt.Sprintf("tool execution panicked: %v", r))
			isError = true
			durationMs = &d
		}
	}()

	result, err := p.tool.Execute(l.ctx, p.tc.ID, p.tc.Arguments, func(partial *types.ToolResult) {
		l.emit(types.AgentEvent{
			Kind: types.ToolExecutionUpdate, ToolCallID: p.tc.ID, ToolName: p.tc.Name,
			Args: p.tc.Arguments, PartialResult: partial,
		})
	})
	duration := time.Since(start).Milliseconds()
	if err != nil {
		msg := err.Error()
		if msg == "" {
			msg = "tool execution failed"
		}
		return errorToolResult(msg), true, &duration
	}
	if result == nil {
		return errorToolResult("tool returned no result"), true, &duration
	}
	return result, result.IsError, &duration
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
		var duration *int64
		if immediate != nil {
			result, isErr = immediate.result, immediate.isError
		} else {
			result, isErr, duration = l.executePrepared(prepared)
		}
		trm := l.finishToolCall(msg, tc, result, isErr, duration)
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
		tc        types.ToolCall
		immediate *immediateOutcome
		prepared  *preparedCall
		result    *types.ToolResult
		trm       *types.ToolResultMessage
	}
	// Preflight (upstream v0.99.2): emit every tool_execution_start and
	// prepare every call BEFORE launching any prepared tool. Immediate
	// outcomes are finalized right away (their tool_execution_end emits
	// immediately); prepared calls become deferred thunks.
	slots := make([]*slot, len(calls))
	for i, tc := range calls {
		l.emit(types.AgentEvent{Kind: types.ToolExecutionStart, ToolCallID: tc.ID, ToolName: tc.Name, Args: tc.Arguments})
		s := &slot{tc: tc}
		prepared, immediate := l.prepare(msg, tc)
		s.immediate = immediate
		s.prepared = prepared
		if immediate != nil {
			s.result = immediate.result
			s.trm = l.finalizeToolCall(msg, tc, immediate.result, immediate.isError, nil)
		}
		slots[i] = s
	}

	// Execute all prepared thunks concurrently. Results are finalized (the
	// serialized AfterToolCall hook plus tool_execution_end, in completion
	// order) as each completion lands on the loop goroutine. The channel is
	// buffered so a tool that ignores ctx cannot leak its goroutine.
	type completion struct {
		index    int
		result   *types.ToolResult
		isError  bool
		duration *int64
	}
	pending := 0
	completions := make(chan completion, len(slots))
	for i, s := range slots {
		if s.immediate != nil {
			continue
		}
		pending++
		go func(index int, p *preparedCall) {
			result, isErr, duration := l.executePrepared(p)
			completions <- completion{index: index, result: result, isError: isErr, duration: duration}
		}(i, s.prepared)
	}
	for i := 0; i < pending; i++ {
		c := <-completions
		s := slots[c.index]
		s.result = c.result
		s.trm = l.finalizeToolCall(msg, s.tc, c.result, c.isError, c.duration)
	}

	// Emit the tool-result messages in assistant source order.
	allTerminated := len(calls) > 0
	out := make([]*types.ToolResultMessage, 0, len(calls))
	for _, s := range slots {
		l.emitToolResultMessage(s.trm)
		out = append(out, s.trm)
		if !s.result.Terminate {
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
