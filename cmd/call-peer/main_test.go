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

func TestCallPeerTalksOverUnixSocket(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var got sandboxsock.CallPeerRequest
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, sandboxsock.PathCallPeer, r.URL.Path)
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(sandboxsock.CallPeerResponse{Status: "completed", Output: "peer says hi"})
	}))

	var stdout bytes.Buffer
	code := run([]string{"call-peer", "bob", "hello"}, &stdout)

	require.Equal(t, 0, code)
	assert.Equal(t, "peer says hi\n", stdout.String())
	assert.Equal(t, sandboxsock.CallPeerRequest{AgentID: "bob", Prompt: "hello"}, got)
}

func TestCallPeerExitCodes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tests := []struct {
		name       string
		args       []string
		status     int
		resp       sandboxsock.CallPeerResponse
		wantCode   int
		wantStdout string
	}{
		{"usage", []string{"call-peer", "bob"}, 0, sandboxsock.CallPeerResponse{}, 1, ""},
		{"waiting human exits cleanly", []string{"call-peer", "bob", "x"}, http.StatusOK, sandboxsock.CallPeerResponse{Status: "waiting_human"}, 0, ""},
		{"failed peer", []string{"call-peer", "bob", "x"}, http.StatusOK, sandboxsock.CallPeerResponse{Status: "failed", Error: "boom"}, 1, ""},
		{"non-200", []string{"call-peer", "bob", "x"}, http.StatusBadGateway, sandboxsock.CallPeerResponse{Error: "down"}, 1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_ = json.NewEncoder(w).Encode(tt.resp)
			}))
			var stdout bytes.Buffer
			assert.Equal(t, tt.wantCode, run(tt.args, &stdout))
			assert.Equal(t, tt.wantStdout, stdout.String())
		})
	}
}
