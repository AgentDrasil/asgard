package api

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSystemDiagnostics_AddErrorAndWarning(t *testing.T) {
	t.Parallel()

	diag := NewSystemDiagnostics()
	diag.AddError("config", "invalid yaml")
	diag.AddWarning("ssh", "missing key")

	snap := diag.Snapshot()
	assert.Equal(t, "degraded", snap.Status)
	assert.Equal(t, []string{"invalid yaml"}, snap.Errors)
	assert.Equal(t, []string{"missing key"}, snap.Warnings)

	logs := diag.GetLogs("all")
	require.Len(t, logs, 2)
	assert.Equal(t, int64(1), logs[0].ID)
	assert.Equal(t, "error", logs[0].Level)
	assert.Equal(t, "config", logs[0].Source)
	assert.Equal(t, "invalid yaml", logs[0].Message)

	assert.Equal(t, int64(2), logs[1].ID)
	assert.Equal(t, "warn", logs[1].Level)
	assert.Equal(t, "ssh", logs[1].Source)
	assert.Equal(t, "missing key", logs[1].Message)
}

func TestSystemDiagnostics_DeduplicationVsLogHistory(t *testing.T) {
	t.Parallel()

	diag := NewSystemDiagnostics()
	// Add identical warning twice
	diag.AddWarning("db", "slow query")
	diag.AddWarning("db", "slow query")

	// Snapshot should be deduplicated (1 item)
	snap := diag.Snapshot()
	assert.Equal(t, "ok", snap.Status)
	assert.Len(t, snap.Warnings, 1)

	// Logs history must record both events
	logs := diag.GetLogs("")
	require.Len(t, logs, 2)
	assert.Equal(t, int64(1), logs[0].ID)
	assert.Equal(t, int64(2), logs[1].ID)
	assert.Equal(t, "db", logs[0].Source)
	assert.Equal(t, "db", logs[1].Source)
}

func TestSystemDiagnostics_AddLog_AndFilter(t *testing.T) {
	t.Parallel()

	diag := NewSystemDiagnostics()
	diag.AddLog("error", "runtime", "panic occurred", "stack trace line 1...")
	diag.AddLog("warn", "cache", "miss rate high", "")
	diag.AddLog("unknown_level", "custom", "custom notice", "") // normalizes to warn

	errLogs := diag.GetLogs("error")
	require.Len(t, errLogs, 1)
	assert.Equal(t, "runtime", errLogs[0].Source)
	assert.Equal(t, "stack trace line 1...", errLogs[0].Details)

	warnLogs := diag.GetLogs("warn")
	require.Len(t, warnLogs, 2)
	assert.Equal(t, "cache", warnLogs[0].Source)
	assert.Equal(t, "custom", warnLogs[1].Source)

	allLogs := diag.GetLogs("all")
	require.Len(t, allLogs, 3)
}

func TestSystemDiagnostics_RingBufferCapAndTruncation(t *testing.T) {
	t.Parallel()

	diag := NewSystemDiagnostics()

	// Test long details truncation
	longDetails := make([]byte, 10000)
	for i := range longDetails {
		longDetails[i] = 'a'
	}
	diag.AddLog("error", "big_error", "too much detail", string(longDetails))
	logs := diag.GetLogs("error")
	require.Len(t, logs, 1)
	assert.Contains(t, logs[0].Details, "... [truncated]")
	assert.LessOrEqual(t, len(logs[0].Details), maxLogDetailsLength+20)

	// Test buffer cap overflow (maxLogBufferCapacity = 1000)
	for i := 1; i <= 1050; i++ {
		diag.AddWarning("load", fmt.Sprintf("warning %d", i))
	}

	allLogs := diag.GetLogs("all")
	assert.Len(t, allLogs, maxLogBufferCapacity)
	// Oldest entries discarded, first retained ID is 1 + (1050 + 1 - 1000) = 52
	assert.Equal(t, int64(52), allLogs[0].ID)
	assert.Equal(t, int64(1051), allLogs[len(allLogs)-1].ID)
}

func TestSystemDiagnostics_ResetSourcePreservesLogs(t *testing.T) {
	t.Parallel()

	diag := NewSystemDiagnostics()
	diag.AddError("agent_load", "failed to load agent A")
	diag.AddWarning("agent_load", "deprecated field in agent A")

	snap1 := diag.Snapshot()
	assert.Len(t, snap1.Errors, 1)
	assert.Len(t, snap1.Warnings, 1)

	// Reset source
	diag.ResetSource("agent_load")
	snap2 := diag.Snapshot()
	assert.Empty(t, snap2.Errors)
	assert.Empty(t, snap2.Warnings)

	// Logs history is retained
	logs := diag.GetLogs("all")
	assert.Len(t, logs, 2)
}

func TestSystemDiagnostics_ConcurrencyRace(t *testing.T) {
	t.Parallel()

	diag := NewSystemDiagnostics()
	const workers = 10
	const iterations = 50

	var wg sync.WaitGroup
	wg.Add(workers * 3)

	// Writer goroutines - AddError/AddWarning
	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				diag.AddError("worker", fmt.Sprintf("error from %d-%d", id, j))
				diag.AddWarning("worker", fmt.Sprintf("warning from %d-%d", id, j))
			}
		}(i)
	}

	// Reader goroutines - Snapshot
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = diag.Snapshot()
			}
		}()
	}

	// Reader goroutines - GetLogs
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = diag.GetLogs("error")
				_ = diag.GetLogs("warn")
				_ = diag.GetLogs("all")
			}
		}()
	}

	wg.Wait()
}
