package workflow

import (
	"context"
	"fmt"
	"iter"
	"os"
	"sort"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
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
	// OnEvent, when set, receives every consumed workflow event keyed by the
	// session (chat) ID. The host application uses it for side effects such
	// as persisting node artifacts into the session.
	OnEvent func(sessionID string, ev WorkflowEvent)
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
			SessionID: execCtx.ContextID,
			RunDir:    e.runDir,
			Input:     messageText(execCtx.Message),
			AgentName: e.AgentName,
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
		rc.EmitEvent = func(ev WorkflowEvent) {
			// Non-blocking: if the consumer is gone, dropping progress events
			// is preferred to stalling engine workers.
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
			result, err := e.engine.Execute(ctx, e.defn, rc)
			outCh <- outcome{result: result, err: err}
		}()

		for {
			select {
			case ev, ok := <-events:
				if !ok {
					events = nil // closed; wait for the outcome below
					continue
				}
				if e.OnEvent != nil {
					e.OnEvent(rc.SessionID, ev)
				}
				if !yieldNodeEvent(execCtx, ev, yield) {
					return
				}
			case out := <-outCh:
				// Drain any events buffered before completion.
				for ev := range events {
					if e.OnEvent != nil {
						e.OnEvent(rc.SessionID, ev)
					}
					if !yieldNodeEvent(execCtx, ev, yield) {
						return
					}
				}
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
			event.SetMeta("artifact_files", ev.Artifacts)
		}
		return yield(event, nil)
	case EventWorkflowResumed:
		msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(ev.Message))
		event := a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, msg)
		event.SetMeta("node_id", ev.NodeID)
		return yield(event, nil)
	case EventNodeStarted, EventNodeFinished, EventNodeSkipped:
		msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(ev.Message))
		event := a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, msg)
		event.SetMeta("node_id", ev.NodeID)
		if len(ev.Artifacts) > 0 {
			event.SetMeta("artifact_files", ev.Artifacts)
		}
		return yield(event, nil)
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
	yield(a2a.NewStatusUpdateEvent(execCtx, state, msg), nil)
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
