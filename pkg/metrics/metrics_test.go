package metrics

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock/socktest"
)

func TestReport_PostsEventsToRunner(t *testing.T) {
	var (
		gotPath string
		gotBody []Event
		calls   atomic.Int64
	)
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.WriteHeader(http.StatusNoContent)
	}))

	Report(context.Background(),
		Event{Kind: KindJev, Tokens: 120},
		Event{Kind: KindShowOutput, Bytes: 4096},
	)

	assert.Equal(t, int64(1), calls.Load())
	assert.Equal(t, sandboxsock.PathMetrics, gotPath)
	assert.Equal(t, []Event{
		{Kind: KindJev, Tokens: 120},
		{Kind: KindShowOutput, Bytes: 4096},
	}, gotBody)
}

func TestReport_NoEventsIsNoOp(t *testing.T) {
	var calls atomic.Int64
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))

	Report(context.Background())

	assert.Zero(t, calls.Load())
}

func TestReport_UnreachableRunnerIsSwallowed(t *testing.T) {
	// A missing socket yields an immediate connection failure; Report must
	// absorb it rather than panicking or surfacing an error.
	t.Setenv(sandboxsock.EnvURL, "unix://"+filepath.Join(t.TempDir(), "gone.sock"))

	done := make(chan struct{})
	go func() {
		defer close(done)
		Report(context.Background(), Event{Kind: KindCompass, Tokens: 7})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Report blocked on an unreachable runner")
	}
}
