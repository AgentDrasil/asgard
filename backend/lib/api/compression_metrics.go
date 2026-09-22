package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/metrics"
	"github.com/AgentDrasil/asgard/pkg/paths"
)

// compressionMetricsFile is the durable home of the aggregate tally, kept
// alongside the other runtime data so it survives backend restarts.
func compressionMetricsFile() string {
	return filepath.Join(paths.DataDir(), "compression-metrics.json")
}

// CompressionCounters is the cumulative tally of command-output compression
// activity reported by the sandbox tools.
type CompressionCounters struct {
	JevCalls        int64 `json:"jev_calls"`
	JevTokens       int64 `json:"jev_tokens"`
	CompassCalls    int64 `json:"compass_calls"`
	CompassTokens   int64 `json:"compass_tokens"`
	Truncations     int64 `json:"truncations"`
	ShowOutputCalls int64 `json:"show_output_calls"`
	ShowOutputBytes int64 `json:"show_output_bytes"`
}

// CompressionMetricsSnapshot is the counters plus the per-call averages derived
// from them, which is what the settings UI renders.
type CompressionMetricsSnapshot struct {
	CompressionCounters
	JevAvgTokens     float64 `json:"jev_avg_tokens"`
	CompassAvgTokens float64 `json:"compass_avg_tokens"`
}

// CompressionMetricsStore aggregates compression telemetry and persists it so
// totals are not lost when the backend restarts.
type CompressionMetricsStore struct {
	mu       sync.Mutex
	path     string
	counters CompressionCounters
}

// NewCompressionMetricsStore creates a store backed by path, loading any tally
// written by a previous run. An empty path uses the default location.
func NewCompressionMetricsStore(path string) *CompressionMetricsStore {
	if path == "" {
		path = compressionMetricsFile()
	}
	s := &CompressionMetricsStore{path: path}
	s.load()
	return s
}

// load restores the persisted counters, ignoring a missing or corrupt file:
// losing a telemetry file must never prevent the backend from starting.
func (s *CompressionMetricsStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warn().Err(err).Str("path", s.path).Msg("failed to read compression metrics file; starting from zero")
		}
		return
	}
	if err := json.Unmarshal(data, &s.counters); err != nil {
		log.Warn().Err(err).Str("path", s.path).Msg("failed to parse compression metrics file; starting from zero")
		s.counters = CompressionCounters{}
	}
}

// Record folds a batch of reported events into the tally and persists it.
func (s *CompressionMetricsStore) Record(events []metrics.Event) {
	if s == nil || len(events) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, e := range events {
		switch e.Kind {
		case metrics.KindJev:
			s.counters.JevCalls++
			s.counters.JevTokens += e.Tokens
		case metrics.KindCompass:
			s.counters.CompassCalls++
			s.counters.CompassTokens += e.Tokens
		case metrics.KindTruncate:
			s.counters.Truncations++
		case metrics.KindShowOutput:
			s.counters.ShowOutputCalls++
			s.counters.ShowOutputBytes += e.Bytes
		default:
			log.Debug().Str("kind", string(e.Kind)).Msg("ignoring compression metric event with unknown kind")
		}
	}

	s.persistLocked()
}

// Snapshot returns the tally with per-call averages resolved.
func (s *CompressionMetricsStore) Snapshot() CompressionMetricsSnapshot {
	if s == nil {
		return CompressionMetricsSnapshot{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	snap := CompressionMetricsSnapshot{CompressionCounters: s.counters}
	if s.counters.JevCalls > 0 {
		snap.JevAvgTokens = float64(s.counters.JevTokens) / float64(s.counters.JevCalls)
	}
	if s.counters.CompassCalls > 0 {
		snap.CompassAvgTokens = float64(s.counters.CompassTokens) / float64(s.counters.CompassCalls)
	}
	return snap
}

// persistLocked writes the counters atomically. Callers must hold s.mu.
func (s *CompressionMetricsStore) persistLocked() {
	data, err := json.Marshal(s.counters)
	if err != nil {
		log.Warn().Err(err).Msg("failed to encode compression metrics")
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		log.Warn().Err(err).Str("path", s.path).Msg("failed to create compression metrics directory")
		return
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Warn().Err(err).Str("path", tmp).Msg("failed to write compression metrics file")
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		log.Warn().Err(err).Str("path", s.path).Msg("failed to replace compression metrics file")
		_ = os.Remove(tmp)
	}
}

// handleRecordCompressionMetrics ingests telemetry batches posted by the
// sandbox tools. It is registered on the internal (loopback-only) mux.
func (s *Server) handleRecordCompressionMetrics(w http.ResponseWriter, r *http.Request) {
	var events []metrics.Event
	if err := json.NewDecoder(r.Body).Decode(&events); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	s.compressionMetrics.Record(events)
	w.WriteHeader(http.StatusNoContent)
}

// handleGetCompressionMetrics returns the aggregated tally to the WebUI.
func (s *Server) handleGetCompressionMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.compressionMetrics.Snapshot())
}
