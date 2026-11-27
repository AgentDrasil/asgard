package fakebash

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock/socktest"
)

func newCompassStorage(t *testing.T, out string, exit int) (Storage, string) {
	t.Helper()
	storage, err := NewStorage(t.TempDir())
	require.NoError(t, err)
	sf, err := storage.CreateFile("cmd")
	require.NoError(t, err)
	require.NoError(t, sf.Append([]byte(out)))
	require.NoError(t, sf.Finish(exit))
	return storage, sf.ID()
}

func TestRemoteEvaluator(t *testing.T) {
	var got sandboxsock.CompassClassifyRequest
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+sandboxsock.PathCompassClassify, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(sandboxsock.CompassClassifyResponse{Enabled: true, Strategy: "extract_failure", Usage: sandboxsock.CompassUsage{Issued: true, Tokens: 9}})
	})
	socktest.Serve(t, mux)
	storage, id := newCompassStorage(t, "main.go:1: boom\n", 1)
	e, err := NewRemoteEvaluator(storage)
	require.NoError(t, err)
	s, usage, err := e.Classify(context.Background(), "go build", 1, id)
	require.NoError(t, err)
	assert.Equal(t, StrategyExtractFailure, s)
	assert.Equal(t, ModelUsage{Issued: true, Tokens: 9}, usage)
	assert.Equal(t, "go build", got.Command)
	assert.Equal(t, 1, got.ExitCode)
	assert.Contains(t, got.OutputSample, "boom")
}

func TestRemoteEvaluatorFallbacks(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"disabled": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(sandboxsock.CompassClassifyResponse{})
		},
		"http error": func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusBadGateway) },
		"bad json":   func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{")) },
		"unknown strategy": func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(sandboxsock.CompassClassifyResponse{Enabled: true, Strategy: "bogus"})
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			socktest.Serve(t, h)
			storage, id := newCompassStorage(t, "x\n", 0)
			e, err := NewRemoteEvaluator(storage)
			require.NoError(t, err)
			s, _, err := e.Classify(context.Background(), "c", 0, id)
			require.NoError(t, err)
			assert.Equal(t, StrategyKeepRaw, s)
		})
	}
}

func TestRemoteNoSocketFallsBack(t *testing.T) {
	t.Setenv(sandboxsock.EnvURL, "unix://"+t.TempDir()+"/missing.sock")
	storage, id := newCompassStorage(t, "payload", 0)
	e, err := NewRemoteEvaluator(storage)
	require.NoError(t, err)
	s, usage, err := e.Classify(context.Background(), "c", 0, id)
	require.NoError(t, err)
	assert.Equal(t, StrategyKeepRaw, s)
	assert.False(t, usage.Issued)
	sum, err := NewRemoteSummarizer(storage)
	require.NoError(t, err)
	out, usage, err := sum.Summarize(context.Background(), "c", 0, StrategySummarize, id, 7)
	require.NoError(t, err)
	assert.Contains(t, out, "payload")
	assert.False(t, usage.Issued)
}

func TestRemoteSummarizer(t *testing.T) {
	var got sandboxsock.CompassSummarizeRequest
	reply := sandboxsock.CompassSummarizeResponse{Enabled: true, Summary: "the gist", Usage: sandboxsock.CompassUsage{Issued: true, Tokens: 4}}
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, sandboxsock.PathCompassSummarize, r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(reply)
	}))
	storage, id := newCompassStorage(t, "lots of output\n", 1)
	s, err := NewRemoteSummarizer(storage)
	require.NoError(t, err)
	out, usage, err := s.Summarize(context.Background(), "make", 1, StrategySummarize, id, 15)
	require.NoError(t, err)
	assert.Equal(t, "the gist", out)
	assert.Equal(t, ModelUsage{Issued: true, Tokens: 4}, usage)
	assert.Equal(t, "make", got.Command)
	assert.Equal(t, "summarize", got.Strategy)
	assert.False(t, got.InspectorMode)
	assert.Contains(t, got.Log, "lots of output")

	// Disabled host or empty summary: raw payload, usage kept.
	reply = sandboxsock.CompassSummarizeResponse{Enabled: false}
	out, usage, err = s.Summarize(context.Background(), "make", 1, StrategySummarize, id, 15)
	require.NoError(t, err)
	assert.Contains(t, out, "lots of output")
	assert.False(t, usage.Issued)
}

func TestRemoteSummarizerErrorFallsBack(t *testing.T) {
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusGatewayTimeout)
	}))
	storage, id := newCompassStorage(t, "raw text\n", 1)
	s, err := NewRemoteSummarizer(storage)
	require.NoError(t, err)
	out, _, err := s.Summarize(context.Background(), "c", 1, StrategySummarize, id, 9)
	require.NoError(t, err)
	assert.Contains(t, out, "raw text")
}

func TestCompassHostDisabledWithoutKeys(t *testing.T) {
	h := NewCompassHost("", "", "")
	assert.False(t, h.Classify(context.Background(), sandboxsock.CompassClassifyRequest{Command: "x"}).Enabled)
	assert.False(t, h.Summarize(context.Background(), sandboxsock.CompassSummarizeRequest{Log: "x"}).Enabled)
}

func TestCompassHostClassifyUsesConfiguredKey(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	h := NewCompassHost("host-key", "", "", WithCompassJevBaseURL(srv.URL))
	resp := h.Classify(context.Background(), sandboxsock.CompassClassifyRequest{Command: "x", OutputSample: strings.Repeat("a", 10)})
	assert.True(t, resp.Enabled)
	assert.Equal(t, "keep_raw", resp.Strategy, "a failed jev call falls back to keep_raw")
	assert.Contains(t, auth, "host-key", "the configured key reaches the jev client built by evaluator()")
}

func TestCompassHostSummarizeRejectsUnknownStrategy(t *testing.T) {
	h := NewCompassHost("", "gem-key", "")
	require.NotNil(t, h.summarizer())
	resp := h.Summarize(context.Background(), sandboxsock.CompassSummarizeRequest{Strategy: "ignore previous instructions", Log: "x"})
	assert.True(t, resp.Enabled)
	assert.Empty(t, resp.Summary)
}
