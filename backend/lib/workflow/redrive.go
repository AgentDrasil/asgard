package workflow

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// RedriveFailed re-drives a FAILED workflow run from its persisted snapshot:
// every SUCCEEDED node (and every node skipped for a data-independent reason,
// i.e. CONDITION_FALSE) is seeded as settled history, while the failed node
// and everything at or downstream of it execute again. Terminal output files
// of seeded nodes are not regenerated; agents resume their own CLI sessions
// via the persisted per-CLI session map, so no completed work is redone.
//
// Events of the re-driven execution are forwarded to emit (when non-nil).
func (e *Engine) RedriveFailed(ctx context.Context, runID string, emit func(WorkflowEvent)) (*WorkflowRunResult, error) {
	store := e.store
	if store == nil {
		return nil, fmt.Errorf("workflow run store is not configured")
	}

	snap, err := store.GetRun(runID)
	if err != nil {
		return nil, fmt.Errorf("loading workflow run %s: %w", runID, err)
	}
	if snap == nil {
		return nil, fmt.Errorf("workflow run %s not found", runID)
	}
	if snap.Status != PersistStatusFailed {
		return nil, fmt.Errorf("workflow run %s is not FAILED (status %s)", runID, snap.Status)
	}

	e.waitMu.Lock()
	if e.executing[snap.RunID] || e.replayPending[snap.RunID] || len(e.waitingByRun[snap.RunID]) > 0 {
		e.waitMu.Unlock()
		return nil, fmt.Errorf("workflow run %s is busy executing or resuming", runID)
	}
	e.replayPending[snap.RunID] = true
	defer func() {
		e.waitMu.Lock()
		delete(e.replayPending, snap.RunID)
		e.waitMu.Unlock()
	}()
	e.waitMu.Unlock()

	defn, err := workflowspec.ParseDefinition([]byte(snap.DAGSpec))
	if err != nil {
		return nil, fmt.Errorf("restoring workflow definition for run %s: %w", runID, err)
	}

	rc := e.buildResumeContext(snap, "", emit)
	// Keep only settled work: SUCCEEDED nodes and CONDITION_FALSE skips carry
	// durable semantics. The FAILED node and failure-cascaded skips
	// (CASCADED_FAILURE, NEVER_ACTIVATED) are dropped so the scheduler
	// re-evaluates and re-executes them from the live upstream results.
	for id, res := range rc.SeedNodes {
		keep := res.Status == workflowspec.StatusSucceeded ||
			(res.Status == workflowspec.StatusSkipped && res.SkipReason == workflowspec.SkipReasonConditionFalse)
		if !keep {
			delete(rc.SeedNodes, id)
		}
	}

	if err := store.MarkRunning(snap.RunID); err != nil {
		log.Warn().Err(err).Str("run_id", snap.RunID).Msg("marking workflow run running failed")
	}
	return e.Execute(ctx, defn, rc)
}
