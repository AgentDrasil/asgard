package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock/socktest"
)

func runCLI(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"subagent"}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestSpawnSendsRequestAndPrintsID(t *testing.T) {
	var got sandboxsock.SubagentSpawnRequest
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, sandboxsock.PathSubagentSpawn, r.URL.Path)
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentSpawnResponse{ID: "child-1"})
	}))

	code, stdout, stderr := runCLI("spawn", "--agent", "reviewer", "--model", "flash", "--readonly", "--label", "r1", "--", "review", "-x the diff")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "child-1\n", stdout)
	assert.Equal(t, sandboxsock.SubagentSpawnRequest{AgentID: "reviewer", Model: "flash", Readonly: true, Label: "r1", Prompt: "review -x the diff"}, got)
}

func TestSpawnPromptAfterDashesIsVerbatim(t *testing.T) {
	var got sandboxsock.SubagentSpawnRequest
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentSpawnResponse{ID: "c"})
	}))
	// A prompt that looks like flags stays a prompt after "--".
	code, stdout, _ := runCLI("spawn", "--json", "--agent=a", "--", "--readonly", "--agent b")
	require.Equal(t, 0, code)
	assert.JSONEq(t, `{"id":"c"}`, stdout)
	assert.Equal(t, "--readonly --agent b", got.Prompt)
	assert.False(t, got.Readonly)
	assert.Equal(t, "a", got.AgentID)
}

func TestSpawnUsageErrors(t *testing.T) {
	t.Setenv(sandboxsock.EnvURL, "unix:///nonexistent/asgard.sock")
	tests := map[string][]string{
		"no agent":          {"spawn", "--", "do it"},
		"no prompt":         {"spawn", "--agent", "a"},
		"blank prompt":      {"spawn", "--agent", "a", "--", "  "},
		"stray positional":  {"spawn", "--agent", "a", "x", "--", "do it"},
		"unknown flag":      {"spawn", "--agent", "a", "--bogus", "--", "x"},
		"unknown command":   {"frobnicate"},
		"no command":        {},
		"status without id": {"status"},
		"status two ids":    {"status", "a", "b"},
		"kill without id":   {"kill"},
		"list with args":    {"list", "x"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := runCLI(args...)
			assert.Equal(t, exitUsage, code)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, "Usage: subagent")
		})
	}
}

func TestSpawnPromptWithoutDashes(t *testing.T) {
	var got sandboxsock.SubagentSpawnRequest
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentSpawnResponse{ID: "c"})
	}))
	code, _, _ := runCLI("spawn", "--agent", "a", "hello", "world")
	require.Equal(t, 0, code)
	assert.Equal(t, "hello world", got.Prompt)
}

func TestErrorsAreExplained(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{http.StatusForbidden, "subagent: not allowed: nope\n"},
		{http.StatusNotFound, "subagent: not found: nope\n"},
		{http.StatusTooManyRequests, "subagent: limit reached: nope\n"},
		{http.StatusServiceUnavailable, "subagent: unavailable: nope\n"},
		{http.StatusBadRequest, "subagent: nope\n"},
		{http.StatusBadGateway, "subagent: nope\n"},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_ = json.NewEncoder(w).Encode(sandboxsock.ErrorResponse{Error: "nope"})
			}))
			code, stdout, stderr := runCLI("spawn", "--agent", "a", "--", "x")
			assert.Equal(t, exitError, code)
			assert.Empty(t, stdout)
			assert.Equal(t, tt.want, stderr)
		})
	}
}

func TestUnreachableRunner(t *testing.T) {
	t.Setenv(sandboxsock.EnvURL, "unix:///nonexistent/asgard.sock")
	code, _, stderr := runCLI("list")
	assert.Equal(t, exitError, code)
	assert.Contains(t, stderr, "cannot reach the runner")
}

func TestListTableAndJSON(t *testing.T) {
	now := time.Now()
	list := sandboxsock.SubagentListResponse{Subagents: []sandboxsock.SubagentInfo{
		{ID: "c1", Label: "review", Agent: "reviewer", Model: "flash", Readonly: true, State: "running", LastActivityMs: now.Add(-90 * time.Second).UnixMilli()},
		{ID: "c2", Agent: "coder", State: "done"},
	}}
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, sandboxsock.PathSubagentList, r.URL.Path)
		_ = json.NewEncoder(w).Encode(list)
	}))

	code, stdout, _ := runCLI("list")
	require.Equal(t, 0, code)
	assert.Regexp(t, `ID\s+LABEL\s+AGENT\s+MODEL\s+MODE\s+STATE\s+LAST ACTIVITY`, stdout)
	assert.Regexp(t, `c1\s+review\s+reviewer\s+flash\s+readonly\s+running\s+\d+m ago`, stdout)
	assert.Regexp(t, `c2\s+-\s+coder\s+-\s+writable\s+done\s+-`, stdout)

	code, stdout, _ = runCLI("list", "--json")
	require.Equal(t, 0, code)
	var got sandboxsock.SubagentListResponse
	require.NoError(t, json.Unmarshal([]byte(stdout), &got))
	assert.Equal(t, list, got)
}

func TestListEmpty(t *testing.T) {
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentListResponse{})
	}))
	code, stdout, _ := runCLI("list")
	require.Equal(t, 0, code)
	assert.Equal(t, "no subagents\n", stdout)
}

func TestStatusIDMayComeBeforeOrAfterFlags(t *testing.T) {
	var got sandboxsock.SubagentIDRequest
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, sandboxsock.PathSubagentStatus, r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentStatusResponse{Subagent: sandboxsock.SubagentInfo{
			ID: "c1", Agent: "coder", State: "failed", Summary: "it broke",
		}})
	}))

	for _, args := range [][]string{{"status", "c1"}, {"status", "c1", "--json"}, {"status", "--json", "c1"}} {
		code, stdout, stderr := runCLI(args...)
		require.Equal(t, 0, code, stderr)
		assert.Equal(t, "c1", got.ID)
		assert.Contains(t, stdout, "c1")
		assert.Contains(t, stdout, "it broke")
	}
	_, stdout, _ := runCLI("status", "c1")
	assert.Contains(t, stdout, "state: failed\n")
	assert.Contains(t, stdout, "last output (tail):\nit broke\n")
}

func TestKill(t *testing.T) {
	stopped := 3
	var got sandboxsock.SubagentIDRequest
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, sandboxsock.PathSubagentKill, r.URL.Path)
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentKillResponse{Stopped: stopped})
	}))

	code, stdout, _ := runCLI("kill", "c1")
	require.Equal(t, 0, code)
	assert.Equal(t, "c1", got.ID)
	assert.Equal(t, "stopped 3 run(s) of c1 and its subagents\n", stdout)

	code, stdout, _ = runCLI("kill", "c1", "--json")
	require.Equal(t, 0, code)
	assert.JSONEq(t, `{"stopped":3}`, stdout)

	stopped = 0
	_, stdout, _ = runCLI("kill", "c1")
	assert.Equal(t, "c1 was not running\n", stdout)
}

func TestQueuedStateIsExplained(t *testing.T) {
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == sandboxsock.PathSubagentList {
			_ = json.NewEncoder(w).Encode(sandboxsock.SubagentListResponse{Subagents: []sandboxsock.SubagentInfo{{ID: "c1", Agent: "coder", State: "queued"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentStatusResponse{Subagent: sandboxsock.SubagentInfo{ID: "c1", Agent: "coder", State: "queued"}})
	}))
	_, stdout, _ := runCLI("status", "c1")
	assert.Contains(t, stdout, "state: queued\n")
	assert.Contains(t, stdout, "queued: waiting for the workspace")
	_, stdout, _ = runCLI("list")
	assert.Regexp(t, `c1\s+-\s+coder\s+-\s+writable\s+queued`, stdout)
	assert.Contains(t, stdout, "queued: waiting for the workspace")
}

func TestSendPostsTextAndPrintsDelivery(t *testing.T) {
	var got sandboxsock.SubagentSendRequest
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, sandboxsock.PathSubagentSend, r.URL.Path)
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentSendResponse{Delivery: "injected"})
	}))

	code, stdout, stderr := runCLI("send", "c1", "--", "also", "--json check")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "injected\n", stdout)
	assert.Equal(t, sandboxsock.SubagentSendRequest{ID: "c1", Text: "also --json check"}, got)

	code, stdout, _ = runCLI("send", "--json", "c1", "--", "x")
	require.Equal(t, 0, code)
	assert.JSONEq(t, `{"delivery":"injected"}`, stdout)
}

func TestSendUsageErrors(t *testing.T) {
	t.Setenv(sandboxsock.EnvURL, "unix:///nonexistent/asgard.sock")
	for name, args := range map[string][]string{
		"no id":      {"send", "--", "hi"},
		"no message": {"send", "c1"},
		"blank":      {"send", "c1", "--", " "},
		"two ids":    {"send", "c1", "c2", "--", "hi"},
	} {
		code, _, _ := runCLI(args...)
		assert.Equal(t, 2, code, name)
	}
}
