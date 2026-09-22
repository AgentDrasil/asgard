package metrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReport_PostsEventsToBackend(t *testing.T) {
	var (
		gotPath string
		gotBody []Event
		calls   atomic.Int64
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	t.Setenv("ASGARD_INTERNAL_API_HOST", server.URL)

	Report(context.Background(),
		Event{Kind: KindJev, Tokens: 120},
		Event{Kind: KindShowOutput, Bytes: 4096},
	)

	assert.Equal(t, int64(1), calls.Load())
	assert.Equal(t, EndpointPath, gotPath)
	assert.Equal(t, []Event{
		{Kind: KindJev, Tokens: 120},
		{Kind: KindShowOutput, Bytes: 4096},
	}, gotBody)
}

func TestReport_NoEventsIsNoOp(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	t.Setenv("ASGARD_INTERNAL_API_HOST", server.URL)

	Report(context.Background())

	assert.Zero(t, calls.Load())
}

func TestReport_UnreachableBackendIsSwallowed(t *testing.T) {
	// A closed listener yields an immediate connection refusal; Report must
	// absorb it rather than panicking or surfacing an error.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	host := server.URL
	server.Close()

	t.Setenv("ASGARD_INTERNAL_API_HOST", host)

	done := make(chan struct{})
	go func() {
		defer close(done)
		Report(context.Background(), Event{Kind: KindCompass, Tokens: 7})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Report blocked on an unreachable backend")
	}
}
