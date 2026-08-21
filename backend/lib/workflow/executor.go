package workflow

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// WorkflowRunParams contains parameters for executing a workflow.
type WorkflowRunParams struct {
	SessionID string
	Prompt    string
	RunDir    string
	// Headless marks no-interaction execution; human nodes fail fast
	// instead of suspending (cron / scheduled runs).
	Headless bool
	Metadata map[string]any
}

// WorkflowExecutor adapts the workflow engine to handle execution and persistence.
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

// Execute runs the workflow DAG and ensures all events are persisted.
func (e *WorkflowExecutor) Execute(ctx context.Context, params WorkflowRunParams) (*WorkflowRunResult, error) {
	rc := RunContext{
		SessionID:         params.SessionID,
		RunDir:            e.runDir,
		Input:             params.Prompt,
		AgentName:         e.AgentName,
		WorkflowRunDirs:   e.WorkflowRunDirs,
		WorkflowMountDirs: e.WorkflowMountDirs,
		Headless:          params.Headless,
	}
	if rc.RunDir == "" && params.RunDir != "" {

		rc.RunDir = params.RunDir
	} else if rc.RunDir == "" && params.Metadata != nil {
		if rd, ok := params.Metadata["run_dir"].(string); ok && rd != "" {
			rc.RunDir = rd
		}
	}
	if rc.RunDir != "" {

		if err := validateRunDir(rc.RunDir); err != nil {
			return nil, err
		}
	}

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
	}

	// Seed the root workflow name into the context call chain so a
	// self-referencing sub-workflow (A -> A) is detected before the first
	// inline execution even happens.
	ctx = context.WithValue(ctx, wfCallChainKey{}, []string{e.defn.Name})

	result, err := e.engine.Execute(context.WithoutCancel(ctx), e.defn, rc)
	close(persistCh)
	<-persistDone
	return result, err
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

// SummarizeRun formats a human-readable summary of the workflow run result.
func SummarizeRun(result *WorkflowRunResult) string {
	if result == nil {
		return ""
	}
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
