package fakebash

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/AgentDrasil/asgard/fakebash/pb"
	"github.com/AgentDrasil/asgard/pkg/metrics"
)

// metricsRecorder is a stand-in for the backend's internal metrics endpoint.
type metricsRecorder struct {
	mu     sync.Mutex
	events []metrics.Event
	server *httptest.Server
}

func newMetricsRecorder(t *testing.T) *metricsRecorder {
	t.Helper()
	rec := &metricsRecorder{}
	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, metrics.EndpointPath, r.URL.Path)
		var events []metrics.Event
		require.NoError(t, json.NewDecoder(r.Body).Decode(&events))
		rec.mu.Lock()
		rec.events = append(rec.events, events...)
		rec.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(rec.server.Close)
	t.Setenv("ASGARD_INTERNAL_API_HOST", rec.server.URL)
	return rec
}

func (r *metricsRecorder) recorded() []metrics.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]metrics.Event(nil), r.events...)
}

func TestReportTelemetry_MapsOutcomeToEvents(t *testing.T) {
	rec := newMetricsRecorder(t)

	reportTelemetry(context.Background(), Outcome{
		Output:    "compressed",
		Jev:       ModelUsage{Issued: true, Tokens: 120},
		Compass:   ModelUsage{Issued: true, Tokens: 45},
		Truncated: true,
	})

	assert.Equal(t, []metrics.Event{
		{Kind: metrics.KindJev, Tokens: 120},
		{Kind: metrics.KindCompass, Tokens: 45},
		{Kind: metrics.KindTruncate},
	}, rec.recorded())
}

func TestReportTelemetry_SkipsUnissuedCalls(t *testing.T) {
	rec := newMetricsRecorder(t)

	// Fast paths and fallbacks leave both usages unissued; no model was billed.
	reportTelemetry(context.Background(), Outcome{Output: "raw"})

	assert.Empty(t, rec.recorded())
}

func TestRunStream_ReportsTelemetry(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "telemetry.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	grpcServer := grpc.NewServer()
	pb.RegisterFakebashServiceServer(grpcServer, &fakebashServer{})
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)

	grpcConn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = grpcConn.Close() })

	rec := newMetricsRecorder(t)

	storage, err := NewStorage(tmpDir)
	require.NoError(t, err)

	pipeline := NewPipeline(storage,
		&fakeEvaluator{strategy: StrategyExtractFailure, usage: ModelUsage{Issued: true, Tokens: 120}},
		&fakeSummarizer{summary: "condensed failure diagnosis", usage: ModelUsage{Issued: true, Tokens: 45}},
	)

	// Output must exceed ShortCircuitMaxBytes so the pipeline is actually consulted.
	var stdoutBuf, stderrBuf strings.Builder
	exitCode, err := runStream(context.Background(), pb.NewFakebashServiceClient(grpcConn),
		[]string{"-c", "head -c 1000 /dev/zero | tr '\\0' 'x'"}, tmpDir, os.Environ(), &stdoutBuf, &stderrBuf, pipeline)
	require.NoError(t, err)
	require.Equal(t, 0, exitCode)

	assert.Equal(t, []metrics.Event{
		{Kind: metrics.KindJev, Tokens: 120},
		{Kind: metrics.KindCompass, Tokens: 45},
	}, rec.recorded())
}
