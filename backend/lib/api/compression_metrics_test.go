package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/metrics"
)

func tempMetricsPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "compression-metrics.json")
}

func TestCompressionMetricsStore_RecordAndSnapshot(t *testing.T) {
	t.Parallel()

	store := NewCompressionMetricsStore(tempMetricsPath(t))
	store.Record([]metrics.Event{
		{Kind: metrics.KindJev, Tokens: 100},
		{Kind: metrics.KindJev, Tokens: 200},
		{Kind: metrics.KindCompass, Tokens: 60},
		{Kind: metrics.KindCompressed},
		{Kind: metrics.KindCompressed},
		{Kind: metrics.KindCompressed},
		{Kind: metrics.KindCompressed},
		{Kind: metrics.KindTruncate},
		{Kind: metrics.KindShowOutput, Bytes: 4096},
		{Kind: metrics.KindShowOutput, Bytes: 2048},
	})

	snap := store.Snapshot()
	assert.Equal(t, int64(2), snap.JevCalls)
	assert.Equal(t, int64(300), snap.JevTokens)
	assert.Equal(t, 150.0, snap.JevAvgTokens)
	assert.Equal(t, int64(1), snap.CompassCalls)
	assert.Equal(t, int64(60), snap.CompassTokens)
	assert.Equal(t, 60.0, snap.CompassAvgTokens)
	assert.Equal(t, int64(4), snap.CompressedOutputs)
	assert.Equal(t, int64(1), snap.Truncations)
	assert.Equal(t, int64(2), snap.ShowOutputCalls)
	assert.Equal(t, int64(6144), snap.ShowOutputBytes)
	// 2 retrievals out of 4 compressed outputs.
	assert.Equal(t, 0.5, snap.ShowOutputRate)
}

func TestCompressionMetricsStore_NoCallsYieldsZeroAverage(t *testing.T) {
	t.Parallel()

	store := NewCompressionMetricsStore(tempMetricsPath(t))

	snap := store.Snapshot()
	assert.Zero(t, snap.JevCalls)
	assert.Zero(t, snap.JevAvgTokens)
	assert.Zero(t, snap.CompassAvgTokens)
}

func TestCompressionMetricsStore_RateNeedsADenominator(t *testing.T) {
	t.Parallel()

	// Retrievals with nothing compressed must not render as a reassuring 0%.
	store := NewCompressionMetricsStore(tempMetricsPath(t))
	store.Record([]metrics.Event{{Kind: metrics.KindShowOutput, Bytes: 10}})

	assert.Zero(t, store.Snapshot().ShowOutputRate)
}

func TestCompressionMetricsStore_RateExceedingOneIsPreserved(t *testing.T) {
	t.Parallel()

	// Retrieving the same output repeatedly is a genuine signal, not an error,
	// so the rate is reported as-is rather than clamped.
	store := NewCompressionMetricsStore(tempMetricsPath(t))
	store.Record([]metrics.Event{
		{Kind: metrics.KindCompressed},
		{Kind: metrics.KindShowOutput, Bytes: 10},
		{Kind: metrics.KindShowOutput, Bytes: 10},
	})

	assert.Equal(t, 2.0, store.Snapshot().ShowOutputRate)
}

func TestCompressionMetricsStore_SurvivesRestart(t *testing.T) {
	t.Parallel()

	path := tempMetricsPath(t)
	first := NewCompressionMetricsStore(path)
	first.Record([]metrics.Event{{Kind: metrics.KindJev, Tokens: 42}})

	second := NewCompressionMetricsStore(path)
	snap := second.Snapshot()
	assert.Equal(t, int64(1), snap.JevCalls)
	assert.Equal(t, int64(42), snap.JevTokens)
}

func TestCompressionMetricsStore_CorruptFileStartsFromZero(t *testing.T) {
	t.Parallel()

	path := tempMetricsPath(t)
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))

	store := NewCompressionMetricsStore(path)
	assert.Zero(t, store.Snapshot().JevCalls)

	// The store must stay usable and overwrite the corrupt file.
	store.Record([]metrics.Event{{Kind: metrics.KindTruncate}})
	assert.Equal(t, int64(1), NewCompressionMetricsStore(path).Snapshot().Truncations)
}

func TestCompressionMetricsStore_UnknownKindIsIgnored(t *testing.T) {
	t.Parallel()

	store := NewCompressionMetricsStore(tempMetricsPath(t))
	store.Record([]metrics.Event{{Kind: "someday-new-kind", Tokens: 1}})

	assert.Equal(t, CompressionCounters{}, store.Snapshot().CompressionCounters)
}

func TestHandleRecordCompressionMetrics(t *testing.T) {
	t.Parallel()

	store := NewCompressionMetricsStore(tempMetricsPath(t))
	srv := &Server{compressionMetrics: store}

	body, err := json.Marshal([]metrics.Event{{Kind: metrics.KindCompass, Tokens: 11}})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	srv.handleRecordCompressionMetrics(rec, httptest.NewRequest(http.MethodPost, metrics.EndpointPath, bytes.NewReader(body)))

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, int64(1), store.Snapshot().CompassCalls)
}

func TestHandleRecordCompressionMetrics_RejectsMalformedBody(t *testing.T) {
	t.Parallel()

	srv := &Server{compressionMetrics: NewCompressionMetricsStore(tempMetricsPath(t))}

	rec := httptest.NewRecorder()
	srv.handleRecordCompressionMetrics(rec, httptest.NewRequest(http.MethodPost, metrics.EndpointPath, bytes.NewReader([]byte("{not json"))))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleGetCompressionMetrics(t *testing.T) {
	t.Parallel()

	store := NewCompressionMetricsStore(tempMetricsPath(t))
	store.Record([]metrics.Event{
		{Kind: metrics.KindJev, Tokens: 90},
		{Kind: metrics.KindCompressed},
		{Kind: metrics.KindCompressed},
		{Kind: metrics.KindTruncate},
	})
	srv := &Server{compressionMetrics: store}

	rec := httptest.NewRecorder()
	srv.handleGetCompressionMetrics(rec, httptest.NewRequest(http.MethodGet, metrics.EndpointPath, nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var snap CompressionMetricsSnapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snap))
	assert.Equal(t, int64(1), snap.JevCalls)
	assert.Equal(t, int64(90), snap.JevTokens)
	assert.Equal(t, 90.0, snap.JevAvgTokens)
	assert.Equal(t, int64(2), snap.CompressedOutputs)
	assert.Equal(t, int64(1), snap.Truncations)
	assert.Zero(t, snap.ShowOutputRate)
}
