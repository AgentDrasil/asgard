package claude

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/common"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

const sampleStream = `{"type":"system","subtype":"init","session_id":"s-1"}
{"type":"assistant","message":{"model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":"t1","name":"Write","input":{"file_path":"/work/a.go","content":"x"}}],"usage":{"input_tokens":2,"cache_read_input_tokens":9000,"cache_creation_input_tokens":8000}},"session_id":"s-1"}
{"type":"rate_limit_event","rate_limit_info":{}}
{"type":"user","message":{"role":"user","content":[{"tool_use_id":"t1","type":"tool_result","content":"ok","is_error":false}]},"session_id":"s-1"}
{"type":"assistant","message":{"model":"claude-sonnet-5-5","content":[{"type":"text","text":"Done."}],"usage":{"input_tokens":2,"cache_read_input_tokens":17000,"cache_creation_input_tokens":2000}},"session_id":"s-1"}
{"type":"result","subtype":"success","is_error":false,"result":"Done.","session_id":"s-1"}
`

type entry struct {
	typ, content string
	meta         map[string]any
}

func TestConsumeStream(t *testing.T) {
	t.Parallel()
	var got []entry
	out, err := consumeStream(strings.NewReader(sampleStream), 200000, func(_ int, _, typ, content string, meta map[string]any) {
		got = append(got, entry{typ, content, meta})
	})
	require.NoError(t, err)
	assert.Equal(t, "s-1", out.sessionID)
	assert.Equal(t, "Done.", out.lastContent)
	assert.Equal(t, 19002, out.inputTokens)
	assert.Empty(t, out.errMessage)

	require.Len(t, got, 3)
	assert.Equal(t, "tool_call", got[0].typ)
	assert.Equal(t, "Write", got[0].meta["tool_name"])
	assert.Equal(t, []string{"/work/a.go"}, got[0].meta["target_files"])
	assert.Equal(t, "tool_call", got[1].typ)
	assert.Equal(t, "ok", got[1].content)
	assert.Equal(t, "Write", got[1].meta["tool_name"])
	assert.Equal(t, "agent_response", got[2].typ)
	assert.Equal(t, false, got[2].meta["is_append"])
}

func TestConsumeStreamError(t *testing.T) {
	t.Parallel()
	out, err := consumeStream(strings.NewReader(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}`), 0, nil)
	require.NoError(t, err)
	assert.Equal(t, "boom", out.errMessage)
}

func TestBuildPromptArgv(t *testing.T) {
	t.Parallel()
	argv := buildPromptArgv("hi", types.PromptOptions{Model: "opus/high"},
		agentSetup{appendPromptFile: "/tmp/p.md", disallowedTools: []string{"AskUserQuestion", "Bash"}}, "uuid-1", false)
	assert.Equal(t, []string{
		"--print", "--verbose", "--output-format", "stream-json", "--dangerously-skip-permissions",
		"--session-id", "uuid-1", "--model", "opus", "--effort", "high",
		"--append-system-prompt-file", "/tmp/p.md", "--disallowedTools", "AskUserQuestion,Bash",
		"--", "hi",
	}, argv)

	argv = buildPromptArgv("hi", types.PromptOptions{Model: "sonnet"}, agentSetup{}, "uuid-2", true)
	assert.Equal(t, []string{
		"--print", "--verbose", "--output-format", "stream-json", "--dangerously-skip-permissions",
		"--resume", "uuid-2", "--model", "sonnet", "--", "hi",
	}, argv)
}

func TestPrepareAgentRejectsUnknownToolAccess(t *testing.T) {
	t.Parallel()
	_, err := prepareAgent(types.PromptOptions{ToolAccess: "bogus"})
	assert.Error(t, err)
}

func TestNewSessionID(t *testing.T) {
	t.Parallel()
	id, err := newSessionID()
	require.NoError(t, err)
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, id)
}

func TestParseUsageText(t *testing.T) {
	t.Parallel()
	text := "You are currently using your subscription\n\nCurrent session: 18% used · resets Oct 28, 4:30am (UTC)\nCurrent week (all models): 3% used · resets Oct 28, 9am (UTC)\nCurrent week (Sonnet only): 40% used · resets Oct 28, 9am (UTC)\n"
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	usage, err := parseUsageText(text, now)
	require.NoError(t, err)
	require.Len(t, usage.general, 2)
	require.Len(t, usage.perModel, 1)
	assert.Equal(t, "5h", usage.general[0].name)
	assert.InDelta(t, 0.82, usage.general[0].remaining, 1e-9)
	assert.Equal(t, time.Date(2026, 10, 28, 4, 30, 0, 0, time.UTC).Unix(), usage.general[0].refresh)
	assert.Equal(t, time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC).Unix(), usage.general[1].refresh)

	opus, ok := buildModelUsage("opus", usage, true)
	require.True(t, ok)
	assert.InDelta(t, 0.82, opus.Remaining, 1e-9) // most constrained tier
	assert.Len(t, opus.Limits, 2)
	plain, _ := buildModelUsage("opus", usage, false)
	assert.Empty(t, plain.Limits)

	sonnet, ok := buildModelUsage("sonnet", usage, true)
	require.True(t, ok)
	assert.InDelta(t, 0.6, sonnet.Remaining, 1e-9) // per-model weekly tier binds
	assert.Len(t, sonnet.Limits, 3)
}

func TestParseUsageTextWeekOnlyAndCase(t *testing.T) {
	t.Parallel()
	usage, err := parseUsageText("Current week (all models): 10% used · Resets Oct 28, 9am (UTC)", time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, usage.general, 1)
	assert.NotZero(t, usage.general[0].refresh)
}

func TestBuildModelUsageOnlyModelScopedTier(t *testing.T) {
	t.Parallel()
	usage, err := parseUsageText("Current week (Claude Sonnet only): 90% used · resets Oct 28, 9am (UTC)", time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)

	sonnet, ok := buildModelUsage("sonnet", usage, true)
	require.True(t, ok)
	assert.InDelta(t, 0.1, sonnet.Remaining, 1e-9)
	assert.Len(t, sonnet.Limits, 1)

	_, ok = buildModelUsage("opus", usage, false)
	assert.False(t, ok, "a model without an applicable tier must not inherit another model's quota")
}

func TestParseUsageTextNoMatch(t *testing.T) {
	t.Parallel()
	_, err := parseUsageText("API key in use", time.Now())
	assert.Error(t, err)
}

func TestParseResetTime(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	utc := func(y int, m time.Month, d, h, min int) int64 {
		return time.Date(y, m, d, h, min, 0, 0, time.UTC).Unix()
	}

	tests := []struct {
		in   string
		want int64
	}{
		{"Oct 28, 9am (UTC)", utc(2026, 10, 28, 9, 0)},
		{"12am (UTC)", utc(2026, 10, 29, 0, 0)},           // time-only, already past today -> tomorrow
		{"4:30PM (UTC)", utc(2026, 10, 28, 16, 30)},       // time-only, later today
		{"Oct 28, 9am (PST)", 0},                          // claude prints IANA names, not abbreviations
		{"Jan 2, 2027, 9am (UTC)", utc(2027, 1, 2, 9, 0)}, // year shown when it is not the current one
		{"Oct 28, 9am (America/New_York)", utc(2026, 10, 28, 13, 0)},
		{"garbage", 0},
		{"Oct 28, 9am (Nowhere/Land)", 0},
		{"Jan 1, 9am (UTC)", utc(2027, 1, 1, 9, 0)}, // year-less date already past: next year
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, parseResetTime(tt.in, now), tt.in)
	}
}

func TestConsumeStreamOversizedLineAndDrain(t *testing.T) {
	t.Parallel()
	huge := strings.Repeat("x", maxLineBytes+1024)
	in := `{"type":"assistant","message":{"content":[{"type":"text","text":"before"}]}}` + "\n" +
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":"` + huge + `"}]}}` + "\n" +
		`{"type":"result","subtype":"success","result":"after","session_id":"s"}` + "\n"
	out, err := consumeStream(strings.NewReader(in), 0, nil)
	require.NoError(t, err)
	assert.Equal(t, "after", out.lastContent)
	assert.Equal(t, "s", out.sessionID)
}

func TestConsumeStreamMultipleResults(t *testing.T) {
	t.Parallel()
	in := `{"type":"result","is_error":true,"result":"early failure"}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"result":"real answer"}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"result":""}` + "\n"
	out, err := consumeStream(strings.NewReader(in), 0, nil)
	require.NoError(t, err)
	assert.Equal(t, "real answer", out.lastContent)
	assert.Empty(t, out.errMessage)
}

func TestConsumeStreamErrorSubtypeWithoutIsError(t *testing.T) {
	t.Parallel()
	out, err := consumeStream(strings.NewReader(`{"type":"result","subtype":"error_max_turns","is_error":false,"errors":["max turns"]}`), 0, nil)
	require.NoError(t, err)
	assert.Equal(t, "max turns", out.errMessage)
	assert.Empty(t, out.lastContent, "error text must not become the answer")
}

func TestConsumeStreamOversizedResultLineIsAnError(t *testing.T) {
	t.Parallel()
	in := `{"type":"assistant","message":{"content":[{"type":"text","text":"partial"}]}}` + "\n" +
		`{"type":"result","subtype":"success","result":"` + strings.Repeat("x", maxLineBytes+10) + `"}` + "\n"
	out, err := consumeStream(strings.NewReader(in), 0, nil)
	require.NoError(t, err)
	assert.Contains(t, out.errMessage, "size limit")
}

func TestConsumeStreamErrorsArray(t *testing.T) {
	t.Parallel()
	out, err := consumeStream(strings.NewReader(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":null,"errors":["No conversation found","x"]}`), 0, nil)
	require.NoError(t, err)
	assert.Equal(t, "No conversation found; x", out.errMessage)
}

func TestConsumeStreamToolResultIsError(t *testing.T) {
	t.Parallel()
	in := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":"nope","is_error":true}]}}`
	var meta map[string]any
	_, err := consumeStream(strings.NewReader(in), 0, func(_ int, _, _, _ string, m map[string]any) { meta = m })
	require.NoError(t, err)
	assert.Equal(t, true, meta["is_error"])
}

// fakeClaude installs a `claude` shell script on PATH for the test.
func fakeClaude(t *testing.T, script string) {
	t.Helper()
	// Keep common.Load() away from a real contract when tests run inside an
	// asgard sandbox.
	t.Setenv(common.EnvVar, filepath.Join(t.TempDir(), "missing-AW_AGENTS.md"))
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/bash\n"+script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestPromptSuccessAndResumeArgs(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	fakeClaude(t, `printf '%s\n' "$@" > `+argsFile+`
echo '{"type":"result","subtype":"success","is_error":false,"result":"hi","session_id":"s-9"}'`)

	res, err := Prompt(context.Background(), "hello", types.PromptOptions{Model: "sonnet", SessionID: "s-9"})
	require.NoError(t, err)
	assert.Equal(t, "hi", res.LastContent)
	assert.Equal(t, "s-9", res.SessionID)

	args, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Contains(t, string(args), "--resume\ns-9\n")
}

func TestPromptPinsConfigDirToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "/operator/chosen")
	envFile := filepath.Join(t.TempDir(), "env")
	fakeClaude(t, `echo "$CLAUDE_CONFIG_DIR" > `+envFile+`
echo '{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s"}'`)

	_, err := Prompt(context.Background(), "hello", types.PromptOptions{})
	require.NoError(t, err)
	got, err := os.ReadFile(envFile)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".claude"), strings.TrimSpace(string(got)))
}

func TestPromptNonZeroExitSurfacesStderr(t *testing.T) {
	fakeClaude(t, `echo "No conversation found" >&2
exit 1`)
	_, err := Prompt(context.Background(), "hello", types.PromptOptions{SessionID: "missing"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "No conversation found")
}

func TestPromptResultErrorWithZeroExit(t *testing.T) {
	fakeClaude(t, `echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}'`)
	_, err := Prompt(context.Background(), "hello", types.PromptOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestPromptCancelKillsProcessGroup(t *testing.T) {
	// The grandchild inherits stderr; the whole process group must be killed
	// on cancel, not just claude.
	pidFile := filepath.Join(t.TempDir(), "pid")
	fakeClaude(t, `sleep 60 &
echo $! > `+pidFile+`
wait`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	start := time.Now()
	_, err := Prompt(ctx, "hello", types.PromptOptions{})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	// Without the group kill Prompt only returns after WaitDelay (5s) on top
	// of the 1s timeout.
	assert.Less(t, time.Since(start), 4*time.Second, "group kill must not wait for WaitDelay")

	var pid int
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil
	}, 2*time.Second, 20*time.Millisecond, "fake claude never recorded the grandchild pid")
	assert.Eventually(t, func() bool { return processGone(pid) }, 2*time.Second, 50*time.Millisecond, "grandchild %d still alive", pid)
}

// processGone reports whether pid no longer runs; a zombie awaiting its
// reaper counts as gone.
func processGone(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return true
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	// "pid (comm) S ..." - the state follows the last ')'.
	i := strings.LastIndexByte(string(stat), ')')
	return i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z'
}

func TestPromptCancelWithDetachedStdoutHolder(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not available")
	}
	// A setsid'd descendant escapes the group kill and keeps stdout open;
	// Prompt must still return shortly after ctx + WaitDelay.
	pidFile := filepath.Join(t.TempDir(), "pid")
	fakeClaude(t, `setsid sleep 30 >&1 2>/dev/null &
echo $! > `+pidFile+`
wait`)
	t.Cleanup(func() {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := Prompt(ctx, "hello", types.PromptOptions{})
	require.Error(t, err)
	assert.Less(t, time.Since(start), 15*time.Second)
}

func TestPromptPassesContractAsFile(t *testing.T) {
	dir := t.TempDir()
	contractPath := filepath.Join(dir, "AW_AGENTS.md")
	require.NoError(t, os.WriteFile(contractPath, []byte(common.Render(&common.Contract{
		AgentID: "a1", ToolAccess: types.ToolAccessFull, Body: "You are the test agent.",
	})), 0o644))

	copyFile := filepath.Join(dir, "prompt-copy")
	fakeClaude(t, `while [ $# -gt 0 ]; do
  if [ "$1" = "--append-system-prompt-file" ]; then cp "$2" `+copyFile+`; echo "$2" > `+dir+`/path; fi
  shift
done
echo '{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s"}'`)
	t.Setenv(common.EnvVar, contractPath)

	_, err := Prompt(context.Background(), "hello", types.PromptOptions{})
	require.NoError(t, err)

	got, err := os.ReadFile(copyFile)
	require.NoError(t, err)
	assert.Contains(t, string(got), "You are the test agent.")

	usedPath, err := os.ReadFile(filepath.Join(dir, "path"))
	require.NoError(t, err)
	_, statErr := os.Stat(strings.TrimSpace(string(usedPath)))
	assert.True(t, os.IsNotExist(statErr), "temp prompt file must be removed after the run")
}

func TestPromptHugeLineDoesNotHang(t *testing.T) {
	fakeClaude(t, `head -c 6000000 /dev/zero | tr '\0' 'x'
echo
echo '{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s"}'`)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := Prompt(ctx, "hello", types.PromptOptions{})
	require.NoError(t, err)
	assert.Equal(t, "ok", res.LastContent)
}

func TestSandboxSpec(t *testing.T) {
	t.Parallel()
	c := &Client{}
	assert.Equal(t, "/h/.claude", c.AuthDirectory("/h"))
	assert.Empty(t, c.ExtraArgs())
	assert.Empty(t, c.AuthFiles("/h"))
	assert.Equal(t, map[string]string{"CLAUDE_CONFIG_DIR": "/h/.claude", "DISABLE_AUTOUPDATER": "1"}, c.ExtraEnv("/h"))
}

func TestExtractTargetFiles(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{"/w/a.go"}, extractTargetFiles("Edit", map[string]any{"file_path": "/w/a.go"}))
	assert.Equal(t, []string{"/w/n.ipynb"}, extractTargetFiles("NotebookEdit", map[string]any{"notebook_path": "/w/n.ipynb"}))
	assert.Nil(t, extractTargetFiles("Read", map[string]any{"file_path": "/w/a.go"}))
	assert.Nil(t, extractTargetFiles("Write", map[string]any{"file_path": "  "}))
}

func TestConsumeStreamIgnoresSyntheticUsageAndReportsTokens(t *testing.T) {
	t.Parallel()
	in := `{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"slash output"}],"usage":{"input_tokens":50}}}` + "\n"
	var metas []map[string]any
	out, err := consumeStream(strings.NewReader(in+sampleStream), 1000, func(_ int, _, _, _ string, m map[string]any) { metas = append(metas, m) })
	require.NoError(t, err)
	assert.Equal(t, 19002, out.inputTokens)

	require.GreaterOrEqual(t, len(metas), 2)
	assert.NotContains(t, metas[0], "input_tokens", "synthetic messages must not set token counts")
	assert.Equal(t, 17002, metas[1]["input_tokens"])
	assert.Equal(t, 1000, metas[1]["max_tokens"])
}

func TestBuildPromptArgvEffortGate(t *testing.T) {
	t.Parallel()
	for _, model := range []string{"opus/thinking", "opus/minimal", "opus"} {
		argv := buildPromptArgv("hi", types.PromptOptions{Model: model}, agentSetup{}, "id", false)
		assert.NotContains(t, argv, "--effort", model)
		assert.Contains(t, argv, "opus", model)
	}
	for _, v := range []string{"low", "medium", "high", "xhigh", "max"} {
		argv := buildPromptArgv("hi", types.PromptOptions{Model: "opus/" + v}, agentSetup{}, "id", false)
		assert.Contains(t, strings.Join(argv, " "), "--effort "+v)
	}
}

func TestPrepareAgentToolRestrictions(t *testing.T) {
	tests := []struct {
		name         string
		contract     common.Contract
		toolAccess   string
		wantDisallow []string
		wantAbsent   []string
	}{
		{"full no team", common.Contract{AgentID: "a", Body: "BODY-MARKER"}, "", []string{"AskUserQuestion"}, []string{"Task", "Agent", "Bash"}},
		{"team", common.Contract{AgentID: "a", Team: "t", Body: "BODY-MARKER"}, "", []string{"AskUserQuestion", "Task", "Agent"}, []string{"Bash"}},
		{"full access option", common.Contract{AgentID: "a", Body: "BODY-MARKER"}, types.ToolAccessFull, []string{"AskUserQuestion"}, []string{"Task", "Agent", "Bash"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "AW_AGENTS.md")
			require.NoError(t, os.WriteFile(path, []byte(common.Render(&tt.contract)), 0o644))
			t.Setenv(common.EnvVar, path)

			setup, err := prepareAgent(types.PromptOptions{ToolAccess: tt.toolAccess})
			require.NoError(t, err)
			assert.Contains(t, setup.appendPrompt, "BODY-MARKER")
			for _, tool := range tt.wantDisallow {
				assert.Contains(t, setup.disallowedTools, tool)
			}
			for _, tool := range tt.wantAbsent {
				assert.NotContains(t, setup.disallowedTools, tool)
			}
		})
	}
}

func TestPrepareAgentWithoutContractAddsNothing(t *testing.T) {
	t.Setenv(common.EnvVar, filepath.Join(t.TempDir(), "missing"))
	setup, err := prepareAgent(types.PromptOptions{})
	require.NoError(t, err)
	assert.Empty(t, setup.appendPrompt)
	assert.Empty(t, setup.disallowedTools)
}

func TestPromptNewSessionUsesSessionIDAndReportsUsage(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	fakeClaude(t, `printf '%s\n' "$@" > `+argsFile+`
echo '{"type":"assistant","message":{"model":"claude-sonnet-5-5","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":100,"cache_read_input_tokens":200,"cache_creation_input_tokens":300}}}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"hi","session_id":"from-claude"}'`)

	res, err := Prompt(context.Background(), "hello", types.PromptOptions{Model: "sonnet"})
	require.NoError(t, err)
	assert.Equal(t, "from-claude", res.SessionID)
	assert.Equal(t, 600, res.InputTokens)
	assert.Equal(t, 1048576, res.MaxTokens)
	assert.InDelta(t, 1-600.0/1048576, res.Remaining, 1e-9)

	args, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	assert.Contains(t, lines, "--session-id")
	assert.NotContains(t, lines, "--resume")
	for i, l := range lines {
		if l == "--session-id" {
			assert.Regexp(t, `^[0-9a-f-]{36}$`, lines[i+1])
		}
	}
}

func TestUsageRunsClaudeWithEnvAndDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "/operator/chosen")
	dir := t.TempDir()
	probe := filepath.Join(t.TempDir(), "probe")
	fakeClaude(t, `{ pwd -P; echo "$CLAUDE_CONFIG_DIR"; printf '%s\n' "$@"; } > `+probe+`
echo '{"type":"result","subtype":"success","is_error":false,"result":"Current session: 10% used · resets Oct 28, 4:30am (UTC)"}'`)

	usage, err := Usage(context.Background(), types.UsageOptions{Dir: dir})
	require.NoError(t, err)
	require.Len(t, usage, 4)
	assert.InDelta(t, 0.9, usage[0].Remaining, 1e-9)

	raw, err := os.ReadFile(probe)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	wantDir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	assert.Equal(t, wantDir, lines[0])
	assert.Equal(t, filepath.Join(home, ".claude"), lines[1])
	assert.Contains(t, lines, "/usage")
}

func TestCommandEnvHasSingleConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "/operator/chosen")
	var got []string
	for _, kv := range commandEnv() {
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			got = append(got, kv)
		}
	}
	assert.Equal(t, []string{"CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude")}, got)
}
