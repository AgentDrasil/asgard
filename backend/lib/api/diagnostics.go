package api

import (
	"strings"
	"sync"
	"time"
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

// LogEntry represents an individual system log entry in the diagnostic history.
type LogEntry struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"` // "warn" | "error"
	Source    string    `json:"source"`
	Message   string    `json:"message"`
	Details   string    `json:"details,omitempty"`
}

// SystemLogsResponse represents the response containing system log entries.
type SystemLogsResponse struct {
	Logs []LogEntry `json:"logs"`
}

const (
	maxLogBufferCapacity = 1000
	maxLogDetailsLength  = 8192
)

// SystemDiagnostics provides thread-safe tracking of diagnostic errors and warnings,
// as well as an in-memory chronological history buffer of diagnostic logs.
type SystemDiagnostics struct {
	mu       sync.RWMutex
	errors   []DiagnosticEntry
	warnings []DiagnosticEntry

	logMu     sync.RWMutex
	logs      []LogEntry
	nextLogID int64
}

// NewSystemDiagnostics creates a new SystemDiagnostics instance.
func NewSystemDiagnostics() *SystemDiagnostics {
	return &SystemDiagnostics{
		errors:   make([]DiagnosticEntry, 0),
		warnings: make([]DiagnosticEntry, 0),
		logs:     make([]LogEntry, 0, 64),
	}
}

// recordLogEntry appends a log entry to the ring buffer, truncating details if necessary.
func (d *SystemDiagnostics) recordLogEntry(level, source, message, details string) {
	if len(details) > maxLogDetailsLength {
		details = details[:maxLogDetailsLength] + "... [truncated]"
	}

	d.logMu.Lock()
	defer d.logMu.Unlock()

	d.nextLogID++
	entry := LogEntry{
		ID:        d.nextLogID,
		Timestamp: time.Now(),
		Level:     level,
		Source:    source,
		Message:   message,
		Details:   details,
	}

	if len(d.logs) >= maxLogBufferCapacity {
		// Discard oldest entry
		d.logs = append(d.logs[1:], entry)
	} else {
		d.logs = append(d.logs, entry)
	}
}

// AddLog records a diagnostic log entry and updates the diagnostic error/warning snapshot.
func (d *SystemDiagnostics) AddLog(level, source, message, details string) {
	if d == nil {
		return
	}
	normLevel := strings.ToLower(strings.TrimSpace(level))
	if normLevel != "error" && normLevel != "warn" {
		normLevel = "warn"
	}

	d.recordLogEntry(normLevel, source, message, details)

	d.mu.Lock()
	defer d.mu.Unlock()

	if normLevel == "error" {
		for _, e := range d.errors {
			if e.Source == source && e.Message == message {
				return
			}
		}
		d.errors = append(d.errors, DiagnosticEntry{
			Source:  source,
			Message: message,
		})
	} else {
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
}

// AddError adds an error entry for a source, deduplicating identical (source, message) pairs in snapshot,
// while preserving every occurrence in the system log history.
func (d *SystemDiagnostics) AddError(source, message string) {
	if d == nil {
		return
	}
	d.recordLogEntry("error", source, message, "")

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

// AddWarning adds a warning entry for a source, deduplicating identical (source, message) pairs in snapshot,
// while preserving every occurrence in the system log history.
func (d *SystemDiagnostics) AddWarning(source, message string) {
	if d == nil {
		return
	}
	d.recordLogEntry("warn", source, message, "")

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

// ResetSource removes all error and warning entries for a specific source from active snapshot.
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

// GetLogs returns a chronological copy of log entries, optionally filtered by level ("all", "warn", "error").
func (d *SystemDiagnostics) GetLogs(levelFilter string) []LogEntry {
	if d == nil {
		return []LogEntry{}
	}

	d.logMu.RLock()
	defer d.logMu.RUnlock()

	filter := strings.ToLower(strings.TrimSpace(levelFilter))
	if filter == "" || filter == "all" {
		result := make([]LogEntry, len(d.logs))
		copy(result, d.logs)
		return result
	}

	result := make([]LogEntry, 0, len(d.logs))
	for _, entry := range d.logs {
		if strings.EqualFold(entry.Level, filter) {
			result = append(result, entry)
		}
	}
	return result
}
