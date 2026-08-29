package api

import (
	"sync"
)

// DiagnosticEntry represents a single diagnostic issue classified by source.
type DiagnosticEntry struct {
	Source  string `json:"source"`
	Message string `json:"message"`
}

// DiagnosticsSnapshot represents the current system health and diagnostic entries.
type DiagnosticsSnapshot struct {
	Status   string   `json:"status"` // "ok" | "degraded"
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

// SystemDiagnostics provides thread-safe tracking of diagnostic errors and warnings.
type SystemDiagnostics struct {
	mu       sync.RWMutex
	errors   []DiagnosticEntry
	warnings []DiagnosticEntry
}

// NewSystemDiagnostics creates a new SystemDiagnostics instance.
func NewSystemDiagnostics() *SystemDiagnostics {
	return &SystemDiagnostics{
		errors:   make([]DiagnosticEntry, 0),
		warnings: make([]DiagnosticEntry, 0),
	}
}

// AddError adds an error entry for a source, deduplicating identical (source, message) pairs.
func (d *SystemDiagnostics) AddError(source, message string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, e := range d.errors {
		if e.Source == source && e.Message == message {
			return
		}
	}
	d.errors = append(d.errors, DiagnosticEntry{
		Source:  source,
		Message: message,
	})
}

// AddWarning adds a warning entry for a source, deduplicating identical (source, message) pairs.
func (d *SystemDiagnostics) AddWarning(source, message string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, w := range d.warnings {
		if w.Source == source && w.Message == message {
			return
		}
	}
	d.warnings = append(d.warnings, DiagnosticEntry{
		Source:  source,
		Message: message,
	})
}

// ResetSource removes all error and warning entries for a specific source.
func (d *SystemDiagnostics) ResetSource(source string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	filteredErrors := make([]DiagnosticEntry, 0, len(d.errors))
	for _, e := range d.errors {
		if e.Source != source {
			filteredErrors = append(filteredErrors, e)
		}
	}
	d.errors = filteredErrors

	filteredWarnings := make([]DiagnosticEntry, 0, len(d.warnings))
	for _, w := range d.warnings {
		if w.Source != source {
			filteredWarnings = append(filteredWarnings, w)
		}
	}
	d.warnings = filteredWarnings
}

// Snapshot returns a copy of the current system diagnostic state.
func (d *SystemDiagnostics) Snapshot() DiagnosticsSnapshot {
	if d == nil {
		return DiagnosticsSnapshot{
			Status:   "ok",
			Errors:   []string{},
			Warnings: []string{},
		}
	}
	d.mu.RLock()
	defer d.mu.RUnlock()

	errMsgs := make([]string, 0, len(d.errors))
	for _, e := range d.errors {
		errMsgs = append(errMsgs, e.Message)
	}

	warnMsgs := make([]string, 0, len(d.warnings))
	for _, w := range d.warnings {
		warnMsgs = append(warnMsgs, w.Message)
	}

	status := "ok"
	if len(errMsgs) > 0 {
		status = "degraded"
	}

	return DiagnosticsSnapshot{
		Status:   status,
		Errors:   errMsgs,
		Warnings: warnMsgs,
	}
}
