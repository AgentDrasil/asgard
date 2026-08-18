package workflow

import (
	"context"
	"fmt"
	"iter"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/rs/zerolog/log"
)

// WorkflowExecutor adapts the workflow engine to the a2asrv SDK Executor
// contract. It deliberately does not import lib/api, keeping package
// dependencies one-directional: lib/api -> lib/workflow.
type WorkflowExecutor struct {
	engine *Engine
	defn   *WorkflowDefinition
	// runDir pins the workflow to a working directory (usually a session's
	// RunDir); empty means "resolve from request metadata / engine default".
	runDir string
	// AgentName names the workflow agent for chat routing of human nodes.
	AgentName string
	// WorkflowRunDirs carries workflow/parent configured run directories.
	WorkflowRunDirs []string
	// WorkflowMountDirs carries workflow/parent configured mount directories.
	WorkflowMountDirs MountDirsConfig
	// OnEvent, when set, receives every consumed workflow event keyed by the
	// session (chat) ID. The host application uses it for side effects such
	// as persisting node artifacts into the session.
	OnEvent func(sessionID string, ev WorkflowEvent)
	// persistTimeout is the maximum duration to wait when sending to persistCh. Defaults to 2s if <= 0.
	persistTimeout time.Duration
	// persistDropped tracks the number of workflow events dropped due to persistence channel overflow.
	persistDropped uint64
}

// PersistDropped returns the total count of dropped persistence events.
func (e *WorkflowExecutor) PersistDropped() uint64 {
	return atomic.LoadUint64(&e.persistDropped)
}

// NewWorkflowExecutor creates an executor for the given engine and definition.
func NewWorkflowExecutor(engine *Engine, defn *WorkflowDefinition) *WorkflowExecutor {
	return &WorkflowExecutor{engine: engine, defn: defn}
}

// NewWorkflowExecutorWithRunDir is like NewWorkflowExecutor but pins RunDir.
func NewWorkflowExecutorWithRunDir(engine *Engine, defn *WorkflowDefinition, runDir string) *WorkflowExecutor {
	return &WorkflowExecutor{engine: engine, defn: defn, runDir: runDir}
}

var _ a2asrv.AgentExecutor = (*WorkflowExecutor)(nil)

// Execute runs the workflow DAG and streams node progress as A2A events.
func (e *WorkflowExecutor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if execCtx.StoredTask == nil {
			if !yield(a2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
				return
			}
		}
		if !yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, nil), nil) {
			return
		}

		rc := RunContext{
			SessionID:         execCtx.ContextID,
			RunDir:            e.runDir,
			Input:             messageText(execCtx.Message),
			AgentName:         e.AgentName,
			WorkflowRunDirs:   e.WorkflowRunDirs,
			WorkflowMountDirs: e.WorkflowMountDirs,
		}
		if rc.RunDir == "" && execCtx.Metadata != nil {
			if rd, ok := execCtx.Metadata["run_dir"].(string); ok && rd != "" {
				rc.RunDir = rd
			}
		}
		if rc.RunDir != "" {
			if err := validateRunDir(rc.RunDir); err != nil {
				yield(nil, err)
				return
			}
		}

		events := make(chan WorkflowEvent, 256)
		// Persistence runs on a dedicated goroutine so DB writes survive
		// after the A2A event stream ends (e.g. the INPUT_REQUIRED final
		// event of a human-node suspension terminates the stream while the
		// engine keeps waiting for the user's reply). The SSE yield loop
		// below can also stall once its consumer is gone; persistence must
		// not.
		persistCh := make(chan WorkflowEvent, 256)
		persistDone := make(chan struct{})
		go func() {
			defer close(persistDone)
			for ev := range persistCh {
				if e.OnEvent != nil {
					e.OnEvent(rc.SessionID, ev)
				}
			}
		}()

		timeout := e.persistTimeout
		if timeout <= 0 {
			timeout = 2 * time.Second
		}

		rc.EmitEvent = func(ev WorkflowEvent) {
			timer := time.NewTimer(timeout)
			select {
			case persistCh <- ev:
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
				dropped := atomic.AddUint64(&e.persistDropped, 1)
				log.Error().
					Str("session_id", rc.SessionID).
					Str("event_type", string(ev.Type)).
					Uint64("dropped_count", dropped).
					Msg("persistCh channel full, dropped workflow event after timeout")
			}
			select {
			case events <- ev:
			default:
			}
		}

		type outcome struct {
			result *WorkflowRunResult
			err    error
		}
		outCh := make(chan outcome, 1)
		go func() {
			defer close(events)
			defer close(persistCh)
			// The engine must outlive the A2A producer context: the a2asrv
			// stack cancels that context once a final event (such as the
			// INPUT_REQUIRED event emitted for human-node suspensions) has
			// been processed, which would otherwise kill the engine while
			// it is blocked waiting for the user's reply.
			result, err := e.engine.Execute(context.WithoutCancel(ctx), e.defn, rc)
			outCh <- outcome{result: result, err: err}
		}()

		for {
			select {
			case ev, ok := <-events:
				if !ok {
					events = nil // closed; wait for the outcome below
					continue
				}
				if !yieldNodeEvent(execCtx, ev, yield) {
					return
				}
			case out := <-outCh:
				// Drain any events buffered before completion.
				for ev := range events {
					if !yieldNodeEvent(execCtx, ev, yield) {
						return
					}
				}
				// Wait for the persistence goroutine to drain so DB writes
				// are complete before the final event is emitted.
				<-persistDone
				if out.err != nil {
					yield(nil, out.err)
					return
				}
				yieldFinalEvent(execCtx, out.result, yield)
				return
			}
		}
	}
}

// Cancel responds to A2A cancel requests. The server stack cancels the
// execution context, which the engine honors by broadcasting cancellation to
// all node workers.
func (e *WorkflowExecutor) Cancel(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

func messageText(msg *a2a.Message) string {
	if msg == nil {
		return ""
	}
	var sb strings.Builder
	for _, part := range msg.Parts {
		if part != nil && part.Text() != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(part.Text())
		}
	}
	return sb.String()
}

func validateRunDir(runDir string) error {
	info, err := os.Stat(runDir)
	if err != nil {
		return fmt.Errorf("run_dir %q does not exist: %w", runDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("run_dir %q is not a directory", runDir)
	}
	return nil
}

func yieldNodeEvent(execCtx *a2asrv.ExecutorContext, ev WorkflowEvent, yield func(a2a.Event, error) bool) bool {
	switch ev.Type {
	case EventWorkflowSuspended:
		// WAITING_HUMAN maps to the A2A input-required state so clients
		// know the task is paused awaiting user input. The ask_user
		// metadata mirrors the single-agent AskUser flow so the frontend
		// renders the inline question box with reply routing.
		msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(ev.Message))
		event := a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateInputRequired, msg)
		event.SetMeta("node_id", ev.NodeID)
		event.SetMeta("entry_type", "ask_user")
		event.SetMeta("message_id", ev.MessageID)
		if ev.AgentName != "" {
			event.SetMeta("agent_name", ev.AgentName)
		}
		if len(ev.Artifacts) > 0 {
			event.SetMeta("artifact_files", ToAnySlice(ev.Artifacts))
		}
		return yield(event, nil)
	case EventWorkflowResumed:
		msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(ev.Message))
		event := a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, msg)
		event.SetMeta("node_id", ev.NodeID)
		return yield(event, nil)
	case EventNodeStatusUpdate:
		msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(ev.Message))
		metadata := map[string]any{
			"node_id":    ev.NodeID,
			"entry_type": ev.EntryType,
		}
		if ev.AgentID != "" {
			metadata["agent_id"] = ev.AgentID
		}
		if ev.AgentName != "" {
			metadata["agent_name"] = ev.AgentName
		}
		for k, v := range ev.Metadata {
			metadata[k] = v
		}
		if len(ev.Artifacts) > 0 {
			metadata["artifact_files"] = ToAnySlice(ev.Artifacts)
		}
		msg.Metadata = SanitizeMetadata(metadata)
		event := a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, msg)
		return yield(event, nil)
	case EventNodeStarted, EventNodeFinished, EventNodeSkipped:
		msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(ev.Message))
		event := a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, msg)
		event.SetMeta("node_id", ev.NodeID)
		event.SetMeta("node_status", string(ev.Status))
		if ev.AgentID != "" {
			event.SetMeta("agent_id", ev.AgentID)
		}
		if ev.Status == StatusFailed {
			// Route failed node updates through the error entry type so the
			// frontend renders them as prominent error cards instead of
			// burying them in the tool log.
			event.SetMeta("entry_type", "error")
		}
		if len(ev.Artifacts) > 0 {
			event.SetMeta("artifact_files", ToAnySlice(ev.Artifacts))
		}
		if !yield(event, nil) {
			return false
		}
		// Deliver the node's final response as an agent_response attributed
		// to the node (node_id metadata) so clients render it as a distinct
		// assistant message per node instead of overwriting a shared bubble.
		if ev.Type == EventNodeFinished && ev.Status == StatusSucceeded && ev.Output != "" {
			outMsg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(ev.Output))
			metadata := map[string]any{
				"node_id":    ev.NodeID,
				"entry_type": "agent_response",
			}
			if ev.AgentID != "" {
				metadata["agent_id"] = ev.AgentID
			}
			if ev.AgentName != "" {
				metadata["agent_name"] = ev.AgentName
			}
			if len(ev.Artifacts) > 0 {
				metadata["artifact_files"] = ToAnySlice(ev.Artifacts)
			}
			outMsg.Metadata = SanitizeMetadata(metadata)
			return yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, outMsg), nil)
		}
		return true
	default:
		return true
	}
}

func yieldFinalEvent(execCtx *a2asrv.ExecutorContext, result *WorkflowRunResult, yield func(a2a.Event, error) bool) {
	summary := summarizeRun(result)
	msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(summary))
	var state a2a.TaskState
	switch result.Status {
	case RunStatusCompleted:
		state = a2a.TaskStateCompleted
	case RunStatusCanceled:
		state = a2a.TaskStateCanceled
	default:
		state = a2a.TaskStateFailed
	}
	event := a2a.NewStatusUpdateEvent(execCtx, state, msg)
	if state == a2a.TaskStateFailed {
		// Surface the failure summary through the error entry type so the
		// frontend renders it as an error card rather than a plain reply.
		event.SetMeta("entry_type", "error")
	}
	yield(event, nil)
}

func summarizeRun(result *WorkflowRunResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Workflow %s\n", result.Status)
	ids := make([]string, 0, len(result.Nodes))
	for id := range result.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		res := result.Nodes[id]
		line := fmt.Sprintf("- %s: %s", id, res.Status)
		if res.Status == StatusSkipped {
			line += fmt.Sprintf(" (%s)", res.SkipReason)
		}
		if res.Error != nil {
			line += fmt.Sprintf(": %v", res.Error)
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}
