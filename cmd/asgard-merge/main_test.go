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

func serveMerge(t *testing.T, status int, resp sandboxsock.MergeResponse) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, sandboxsock.PathMerge, r.URL.Path)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestMergeSucceeds(t *testing.T) {
	serveMerge(t, http.StatusOK, sandboxsock.MergeResponse{Status: "merged", Message: "Merged into main.", OK: true})

	var stdout bytes.Buffer
	require.Equal(t, 0, run(&stdout))
	assert.Equal(t, "Merged into main.\n", stdout.String())
}

func TestMergeNeedsAgentAction(t *testing.T) {
	serveMerge(t, http.StatusOK, sandboxsock.MergeResponse{Status: "main_moved", Message: "Run git rebase main."})

	var stdout bytes.Buffer
	require.Equal(t, 1, run(&stdout))
	assert.Equal(t, "Run git rebase main.\n", stdout.String(), "the guidance reaches the agent")
}

func TestMergeServerError(t *testing.T) {
	serveMerge(t, http.StatusInternalServerError, sandboxsock.MergeResponse{Message: "merge failed: boom"})

	var stdout bytes.Buffer
	assert.Equal(t, 1, run(&stdout))
	assert.Contains(t, stdout.String(), "boom")
}
