package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock/socktest"
)

func TestFindPeerTalksOverUnixSocket(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, sandboxsock.PathFindPeer, r.URL.Path)
		_ = json.NewEncoder(w).Encode([]string{"bob", "carol"})
	}))

	var stdout bytes.Buffer
	require.Equal(t, 0, run(&stdout))
	assert.Equal(t, "[\n  \"bob\",\n  \"carol\"\n]\n", stdout.String())
}

func TestFindPeerNullIsEmptyList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("null"))
	}))

	var stdout bytes.Buffer
	require.Equal(t, 0, run(&stdout))
	assert.Equal(t, "[]\n", stdout.String())
}

func TestFindPeerNon200(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusNotFound)
	}))

	var stdout bytes.Buffer
	assert.Equal(t, 1, run(&stdout))
}
