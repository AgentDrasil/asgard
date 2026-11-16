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

func TestWaitSendsRequestAndPrintsResults(t *testing.T) {
	var got sandboxsock.SubagentWaitRequest
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, sandboxsock.PathSubagentWait, r.URL.Path)
		got = sandboxsock.SubagentWaitRequest{}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentWaitResponse{Results: []sandboxsock.SubagentWaitResult{
			{Subagent: sandboxsock.SubagentInfo{ID: "c1", Label: "review", State: "done", Summary: "line1\nline2"}, Notes: "files in /shared"},
			{Subagent: sandboxsock.SubagentInfo{ID: "c2", State: "running"}, StillRunning: true},
		}})
	}))

	code, stdout, stderr := runCLI("wait", "c1", "--any", "c2", "--timeout", "90s")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, sandboxsock.SubagentWaitRequest{IDs: []string{"c1", "c2"}, Any: true, TimeoutMs: 90000}, got)
	assert.Contains(t, stdout, "c1 (review): done\n")
	assert.Contains(t, stdout, "    line1\n    line2\n")
	assert.Contains(t, stdout, "note: files in /shared")
	assert.Contains(t, stdout, "c2 (-): still_running\n")

	// No ids and no timeout: the defaults are the server's.
	code, _, _ = runCLI("wait", "--all", "--timeout", "5")
	require.Equal(t, 0, code)
	assert.Equal(t, sandboxsock.SubagentWaitRequest{TimeoutMs: 5000}, got)
	code, stdout, _ = runCLI("wait", "--json")
	require.Equal(t, 0, code)
	assert.JSONEq(t, `{"results":[{"subagent":{"id":"c1","label":"review","agent":"","state":"done","summary":"line1\nline2","last_activity_ms":0},"notes":"files in /shared"},{"subagent":{"id":"c2","agent":"","state":"running","last_activity_ms":0},"still_running":true}]}`, stdout)
}

func TestWaitNothingRunning(t *testing.T) {
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentWaitResponse{})
	}))
	code, stdout, _ := runCLI("wait")
	require.Equal(t, 0, code)
	assert.Equal(t, "no running subagents to wait for\n", stdout)
}

func TestReplySendsText(t *testing.T) {
	var got sandboxsock.SubagentReplyRequest
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, sandboxsock.PathSubagentReply, r.URL.Path)
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentReplyResponse{Delivery: "queued"})
	}))
	code, stdout, stderr := runCLI("reply", "--", "which", "--branch?")
	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "queued\n", stdout)
	assert.Equal(t, "which --branch?", got.Text)
}

func TestReplyAndWaitUsageErrors(t *testing.T) {
	t.Setenv(sandboxsock.EnvURL, "unix:///nonexistent/asgard.sock")
	tests := map[string][]string{
		"reply without message":    {"reply"},
		"reply blank":              {"reply", "--", " "},
		"reply with positional":    {"reply", "x", "--", "hi"},
		"wait any and all":         {"wait", "--any", "--all"},
		"wait bad timeout":         {"wait", "--timeout", "soon"},
		"wait zero timeout":        {"wait", "--timeout", "0"},
		"wait with message":        {"wait", "c1", "--", "hi"},
		"wait timeout without val": {"wait", "--timeout"},
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

func TestTreeMarksSelfAndPrintsJSON(t *testing.T) {
	tree := sandboxsock.SubagentTreeResponse{Nodes: []sandboxsock.SubagentTreeNode{
		{ID: "r1", Agent: "lead", State: "idle"},
		{ID: "c1", ParentID: "r1", Agent: "reviewer", Label: "rev", State: "running", Readonly: true, Depth: 1, Self: true},
		{ID: "g1", ParentID: "c1", Agent: "coder", State: "done", Depth: 2},
	}}
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, sandboxsock.PathSubagentTree, r.URL.Path)
		_ = json.NewEncoder(w).Encode(tree)
	}))

	code, stdout, stderr := runCLI("tree")
	require.Equal(t, 0, code, stderr)
	assert.Regexp(t, `ID\s+PARENT\s+AGENT\s+LABEL\s+STATE\s+MODE\s+DEPTH`, stdout)
	assert.Regexp(t, `(?m)^\s+r1\s+-\s+lead\s+-\s+idle\s+-\s+0$`, stdout)
	assert.Regexp(t, `(?m)^\*\s+c1\s+r1\s+reviewer\s+rev\s+running\s+readonly\s+1$`, stdout)
	assert.Regexp(t, `(?m)^\s+g1\s+c1\s+coder\s+-\s+done\s+writable\s+2$`, stdout)
	assert.Contains(t, stdout, "$ASGARD_CHAT_ID")

	code, stdout, _ = runCLI("tree", "--json")
	require.Equal(t, 0, code)
	var got sandboxsock.SubagentTreeResponse
	require.NoError(t, json.Unmarshal([]byte(stdout), &got))
	assert.Equal(t, tree, got)

	code, _, _ = runCLI("tree", "x")
	assert.Equal(t, exitUsage, code)
}

func TestWaitReportsWakeByMessage(t *testing.T) {
	resp := sandboxsock.SubagentWaitResponse{
		Results:        []sandboxsock.SubagentWaitResult{{Subagent: sandboxsock.SubagentInfo{ID: "c1", State: "running"}, StillRunning: true}},
		WokenByMessage: true,
		MessageFrom:    []string{"subagent c1 (rev)"},
	}
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(resp)
	}))
	code, stdout, _ := runCLI("wait", "c1")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, "c1 (-): still_running\n")
	assert.Contains(t, stdout, "note: a message from subagent c1 (rev) arrived; read it, then call wait again\n")
	code, stdout, _ = runCLI("wait", "--json")
	require.Equal(t, 0, code)
	assert.Contains(t, stdout, `"woken_by_message": true`)
}

func TestWaitReportsCheckoutHeldAndTreeTruncated(t *testing.T) {
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == sandboxsock.PathSubagentTree {
			_ = json.NewEncoder(w).Encode(sandboxsock.SubagentTreeResponse{Nodes: []sandboxsock.SubagentTreeNode{{ID: "r", Agent: "a", State: "queued", Self: true}}, Truncated: true})
			return
		}
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentWaitResponse{
			Results:        []sandboxsock.SubagentWaitResult{{Subagent: sandboxsock.SubagentInfo{ID: "c1", State: "running"}, StillRunning: true}},
			WokenByMessage: true, MessageFrom: []string{"subagent c1"}, CheckoutHeldBy: "c1",
		})
	}))
	_, stdout, _ := runCLI("wait")
	assert.Contains(t, stdout, "note: a writable subagent c1 holds the checkout; do not modify files, reply/send and wait again\n")
	_, stdout, _ = runCLI("wait", "--json")
	assert.Contains(t, stdout, `"checkout_held_by": "c1"`)
	_, stdout, _ = runCLI("tree")
	assert.Contains(t, stdout, "truncated:")
	assert.Regexp(t, `\*\s+r\s+-\s+a\s+-\s+queued`, stdout)
}

func TestAskPostsQuestionAndPrintsTheAnswer(t *testing.T) {
	var got sandboxsock.SubagentAskRequest
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, sandboxsock.PathSubagentAsk, r.URL.Path)
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentAskResponse{QID: "q-1", State: sandboxsock.QuestionAnswered, Answer: "main\nplease"})
	}))

	code, stdout, stderr := runCLI("ask", "--to", "c-2", "--timeout", "90s", "--", "which", "--branch?")
	require.Equal(t, exitOK, code, stderr)
	assert.Equal(t, "main\nplease\n", stdout)
	assert.Equal(t, sandboxsock.SubagentAskRequest{To: "c-2", Text: "which --branch?", TimeoutMs: 90000}, got)

	code, stdout, _ = runCLI("ask", "--json", "--to", "c-2", "--", "x")
	require.Equal(t, exitOK, code)
	assert.JSONEq(t, `{"qid":"q-1","state":"answered","answer":"main\nplease"}`, stdout)
}

func TestAskTimeoutPrintsPendingAndExitsWith3(t *testing.T) {
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentAskResponse{QID: "q-9", State: sandboxsock.QuestionPending})
	}))
	code, stdout, stderr := runCLI("ask", "--to", "c-2", "--", "x")
	assert.Equal(t, exitPending, code)
	assert.Equal(t, 3, exitPending)
	assert.Equal(t, "pending q-9\n", stdout)
	assert.Contains(t, stderr, "subagent ask --resume q-9")

	code, stdout, _ = runCLI("ask", "--json", "--resume", "q-9")
	assert.Equal(t, exitPending, code)
	assert.JSONEq(t, `{"qid":"q-9","state":"pending"}`, stdout)
}

func TestAskResumeSendsOnlyTheQuestionID(t *testing.T) {
	var got sandboxsock.SubagentAskRequest
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentAskResponse{QID: "q-9", State: sandboxsock.QuestionAnswered, Answer: "done"})
	}))
	code, stdout, stderr := runCLI("ask", "--resume", "q-9", "--timeout", "45")
	require.Equal(t, exitOK, code, stderr)
	assert.Equal(t, "done\n", stdout)
	assert.Equal(t, sandboxsock.SubagentAskRequest{ResumeQID: "q-9", TimeoutMs: 45000}, got)
}

func TestAskErrorsAreExplained(t *testing.T) {
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(sandboxsock.ErrorResponse{Error: "target c-2 was killed"})
	}))
	code, stdout, stderr := runCLI("ask", "--to", "c-2", "--", "x")
	assert.Equal(t, exitError, code)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "unavailable: target c-2 was killed")
}

func TestAskAndAnswerUsageErrors(t *testing.T) {
	t.Setenv(sandboxsock.EnvURL, "unix:///nonexistent/asgard.sock")
	tests := map[string][]string{
		"ask without target":       {"ask", "--", "x"},
		"ask without question":     {"ask", "--to", "c"},
		"ask blank question":       {"ask", "--to", "c", "--", " "},
		"ask positional":           {"ask", "--to", "c", "x", "--", "y"},
		"ask resume with target":   {"ask", "--resume", "q", "--to", "c"},
		"ask resume with question": {"ask", "--resume", "q", "--", "x"},
		"ask bad timeout":          {"ask", "--to", "c", "--timeout", "soon", "--", "x"},
		"ask nothing":              {"ask"},
		"answer without text":      {"answer", "q"},
		"answer without id":        {"answer", "--", "x"},
		"answer blank":             {"answer", "q", "--", " "},
		"questions with args":      {"questions", "x"},
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

func TestAnswerPostsTextAndPrintsDelivery(t *testing.T) {
	var got sandboxsock.SubagentAnswerRequest
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, sandboxsock.PathSubagentAnswer, r.URL.Path)
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentAnswerResponse{Delivery: "unblocked"})
	}))
	code, stdout, stderr := runCLI("answer", "q-1", "--", "use", "main")
	require.Equal(t, exitOK, code, stderr)
	assert.Equal(t, "unblocked\n", stdout)
	assert.Equal(t, sandboxsock.SubagentAnswerRequest{QID: "q-1", Text: "use main"}, got)
}

func TestQuestionsTableAndJSON(t *testing.T) {
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, sandboxsock.PathSubagentQuestions, r.URL.Path)
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentQuestionsResponse{
			Incoming: []sandboxsock.SubagentQuestionInfo{{QID: "q-1", FromChatID: "c-1", FromLabel: "lead", CreatedMs: time.Now().Add(-30 * time.Second).UnixMilli(), FirstLine: "which branch?"}},
			Outgoing: []sandboxsock.SubagentQuestionInfo{{QID: "q-2", ToChatID: "c-2", State: "pending"}},
		})
	}))
	code, stdout, stderr := runCLI("questions")
	require.Equal(t, exitOK, code, stderr)
	assert.Contains(t, stdout, "q-1")
	assert.Contains(t, stdout, "c-1 (lead)")
	assert.Contains(t, stdout, "which branch?")
	assert.Contains(t, stdout, "q-2")
	assert.Contains(t, stdout, "pending")

	code, stdout, _ = runCLI("questions", "--json")
	require.Equal(t, exitOK, code)
	var out sandboxsock.SubagentQuestionsResponse
	require.NoError(t, json.Unmarshal([]byte(stdout), &out))
	assert.Len(t, out.Incoming, 1)
	assert.Len(t, out.Outgoing, 1)
}

func TestQuestionsEmpty(t *testing.T) {
	socktest.Serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(sandboxsock.SubagentQuestionsResponse{})
	}))
	code, stdout, _ := runCLI("questions")
	require.Equal(t, exitOK, code)
	assert.Equal(t, "no open questions\n", stdout)
}
