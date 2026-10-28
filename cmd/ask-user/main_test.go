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

func TestAskUserTalksOverUnixSocket(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ASGARD_AGENT_NAME", "alice")
	t.Setenv("ASGARD_MODEL", "m1")

	var got sandboxsock.AskUserRequest
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, sandboxsock.PathAskUser, r.URL.Path)
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(sandboxsock.AskUserResponse{Reply: "postgres"})
	}))

	var stdout, stderr bytes.Buffer
	code := run([]string{"Which", "database?"}, &stdout, &stderr)

	require.Equal(t, 0, code, stderr.String())
	assert.Equal(t, "postgres\n", stdout.String())
	assert.Equal(t, sandboxsock.AskUserRequest{Question: "Which database?", AgentName: "alice", Model: "m1"}, got)
}

func TestAskUserExitCodes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	tests := []struct {
		name     string
		args     []string
		handler  http.HandlerFunc
		wantCode int
	}{
		{"no args", nil, nil, 1},
		{"unknown flag", []string{"--bogus"}, nil, 2},
		{"help", []string{"--help"}, nil, 0},
		{"runner error in body", []string{"q"}, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(sandboxsock.AskUserResponse{Error: "boom"})
		}, 1},
		{"non-200", []string{"q"}, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", http.StatusBadGateway)
		}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.handler != nil {
				socktest.Serve(t, tt.handler)
			}
			var stdout, stderr bytes.Buffer
			assert.Equal(t, tt.wantCode, run(tt.args, &stdout, &stderr))
		})
	}
}
