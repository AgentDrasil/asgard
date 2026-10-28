// Package runs tracks the runs executing on a runner.
package runs

import (
	"sort"
	"sync"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

// Table is the set of runs currently executing on the runner, keyed by
// run_id. It is reported in Register.active_runs after a reconnect.
type Table struct {
	mu     sync.Mutex
	active map[string]*runnerpb.RunRef
}

// NewTable returns an empty table.
func NewTable() *Table {
	return &Table{active: make(map[string]*runnerpb.RunRef)}
}

// Begin records ref as active. It reports false if the run_id is already
// active (a replayed StartRun).
func (t *Table) Begin(ref *runnerpb.RunRef) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.active[ref.GetRunId()]; ok {
		return false
	}
	t.active[ref.GetRunId()] = ref
	return true
}

// End removes a run from the table.
func (t *Table) End(runID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.active, runID)
}

// Len returns the number of active runs.
func (t *Table) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.active)
}

// Active returns the active runs ordered by run_id.
func (t *Table) Active() []*runnerpb.RunRef {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*runnerpb.RunRef, 0, len(t.active))
	for _, r := range t.active {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetRunId() < out[j].GetRunId() })
	return out
}
