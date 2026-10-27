package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

const bashSchemaJSON = `{
  "type": "object",
  "properties": {
    "command": { "type": "string", "description": "Bash command to execute" },
    "timeout": { "type": "number", "description": "Timeout in seconds (optional, no default timeout)" }
  },
  "required": ["command"],
  "additionalProperties": false
}`

const maxTimeoutSeconds = 2147483647 / 1000

// BashToolDetails carries truncation metadata and the temp file holding the
// full output when the returned output was truncated.
type BashToolDetails struct {
	Truncation     *TruncationResult `json:"truncation,omitempty"`
	FullOutputPath string            `json:"fullOutputPath,omitempty"`
}

// BashTool executes commands via bash -c in a configured working directory,
// tail-truncating combined stdout/stderr to configured limits.
type BashTool struct {
	cwd    string
	runEnv types.RunEnvironment
}

// NewBashTool creates a bash tool that runs commands in cwd.
func NewBashTool(cwd string) *BashTool {
	return &BashTool{cwd: cwd}
}

// SetRunEnvironment receives per-run metadata (port of pi 005af57d8) that is
// exposed to executed commands as PI_* environment variables.
func (t *BashTool) SetRunEnvironment(env types.RunEnvironment) {
	t.runEnv = env
}

// piEnvNames are the PI_* variables dropped from the inherited environment
// (to avoid stale values) and re-exported from the current run metadata.
var piEnvNames = []string{"PI_SESSION_ID", "PI_SESSION_FILE", "PI_PROVIDER", "PI_MODEL", "PI_REASONING_LEVEL"}

// buildEnv constructs the command environment: the inherited environment
// minus all PI_* names (always deleted, even when no run metadata is set),
// then only the non-empty run metadata values appended.
func buildEnv(environ []string, env types.RunEnvironment) []string {
	stale := make(map[string]struct{}, len(piEnvNames))
	for _, name := range piEnvNames {
		stale[name] = struct{}{}
	}
	out := make([]string, 0, len(environ)+len(piEnvNames))
	for _, kv := range environ {
		// Comma-ok membership: a missing key yields the zero value
		// struct{}{}, which would otherwise drop every variable.
		if i := strings.IndexByte(kv, '='); i > 0 {
			if _, drop := stale[kv[:i]]; drop {
				continue
			}
		}
		out = append(out, kv)
	}
	values := []struct {
		name  string
		value string
	}{
		{"PI_SESSION_ID", env.SessionID},
		{"PI_SESSION_FILE", env.SessionFile},
		{"PI_PROVIDER", env.Provider},
		{"PI_MODEL", env.Model},
		{"PI_REASONING_LEVEL", env.ThinkingLevel},
	}
	for _, v := range values {
		if v.value != "" {
			out = append(out, v.name+"="+v.value)
		}
	}
	return out
}

func (t *BashTool) Name() string  { return "bash" }
func (t *BashTool) Label() string { return "bash" }
func (t *BashTool) Parameters() json.RawMessage {
	return json.RawMessage(bashSchemaJSON)
}
func (t *BashTool) PromptSnippet() string { return "Execute bash commands (ls, grep, find, etc.)" }
func (t *BashTool) PromptGuidelines() []string {
	return []string{"You can inspect PI_* environment variables for current model and session details."}
}
func (t *BashTool) ExecutionMode() types.ToolExecutionMode { return "" }

func (t *BashTool) Description() string {
	return fmt.Sprintf("Execute a bash command in the current working directory. Returns stdout and stderr. Output is truncated to last %d lines or %dKB (whichever is hit first). If truncated, full output is saved to a temp file. Optionally provide a timeout in seconds.", DefaultMaxLines, DefaultMaxBytes/1024)
}

type bashArgs struct {
	Command string   `json:"command"`
	Timeout *float64 `json:"timeout"`
}

// Execute runs the command and returns its combined output with tail
// truncation. Non-zero exits, timeouts, and aborts are returned as errors so
// the caller produces error tool results.
func (t *BashTool) Execute(ctx context.Context, toolCallID string, args json.RawMessage, onUpdate types.UpdateFunc) (*types.ToolResult, error) {
	var in bashArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}

	if _, err := os.Stat(t.cwd); err != nil {
		return nil, fmt.Errorf("working directory does not exist: %s; cannot execute bash commands", t.cwd)
	}

	runCtx := ctx
	if in.Timeout != nil {
		timeoutSecs := *in.Timeout
		if timeoutSecs <= 0 || timeoutSecs != timeoutSecs {
			return nil, fmt.Errorf("invalid timeout: must be a finite number of seconds")
		}
		if timeoutSecs > float64(maxTimeoutSeconds) {
			return nil, fmt.Errorf("invalid timeout: maximum is %d seconds", maxTimeoutSeconds)
		}
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSecs*float64(time.Second)))
		defer cancel()
	}

	cmd := exec.Command("bash", "-c", in.Command)
	cmd.Dir = t.cwd
	// Port of pi 005af57d8: drop stale PI_* values, then expose run metadata.
	cmd.Env = buildEnv(os.Environ(), t.runEnv)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	acc := newOutputAccumulator(TruncationOptions{})
	// Same sanitizing writer for both streams: os/exec interleaves them on
	// one pipe, and the accumulator only ever sees clean text.
	sw := newSanitizingWriter(acc)
	cmd.Stdout = sw
	cmd.Stderr = sw

	if onUpdate != nil {
		onUpdate(&types.ToolResult{Content: []types.AssistantContent{}})
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timedOut := false
	var runErr error
	select {
	case runErr = <-done:
	case <-runCtx.Done():
		if ctx.Err() == nil && runCtx.Err() == context.DeadlineExceeded {
			timedOut = true
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
		runErr = <-done
	}

	// Flush held-back bytes/escape sequences (pi flushOutput) so output
	// produced before a timeout/abort/non-zero exit is still returned.
	sw.Flush()
	truncation := acc.Snapshot()
	text := truncation.Content
	details := &BashToolDetails{}
	if truncation.Truncated {
		details.Truncation = &truncation
		details.FullOutputPath = acc.SpillPath()
		text = appendTruncationFooter(text, &truncation, details.FullOutputPath)
	}
	acc.Close(truncation.Truncated)

	outputText := text
	if outputText == "" && !truncation.Truncated {
		outputText = "(no output)"
	}

	appendStatus := func(status string) string {
		if text == "" {
			return status
		}
		return text + "\n\n" + status
	}

	// errorResult reports a tool-level failure (pi 8562bcf66 isError
	// semantics): the command output stays as Content and the structured
	// Details survive, instead of collapsing into a Go error that discards
	// both.
	errorResult := func(status string) *types.ToolResult {
		return &types.ToolResult{
			Content: []types.AssistantContent{types.TextContent{
				Type: types.TypeText,
				Text: appendStatus(status),
			}},
			Details: details,
			IsError: true,
		}
	}

	if ctx.Err() != nil {
		return errorResult("Command aborted"), nil
	}
	if timedOut {
		timeoutDisplay := ""
		if in.Timeout != nil {
			timeoutDisplay = strconv.FormatFloat(*in.Timeout, 'f', -1, 64)
		}
		return errorResult(fmt.Sprintf("Command timed out after %s seconds", timeoutDisplay)), nil
	}
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode := exitErr.ExitCode()
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				exitCode = 128 + int(status.Signal())
			}
			// Non-zero exit is a tool-level failure, not an execution crash.
			return errorResult(fmt.Sprintf("Command exited with code %d", exitCode)), nil
		}
		return nil, runErr
	}

	result := &types.ToolResult{
		Content: []types.AssistantContent{types.TextContent{Type: types.TypeText, Text: outputText}},
	}
	if details.Truncation != nil {
		result.Details = details
	}
	return result, nil
}

func appendTruncationFooter(text string, tr *TruncationResult, fullPath string) string {
	startLine := tr.TotalLines - tr.OutputLines + 1
	endLine := tr.TotalLines
	var footer string
	if tr.LastLinePartial {
		footer = fmt.Sprintf("[Showing last %s of line %d (line is %s). Full output: %s]",
			FormatSize(tr.OutputBytes), endLine, FormatSize(tr.TotalBytes), fullPath)
	} else if tr.TruncatedBy == "lines" {
		footer = fmt.Sprintf("[Showing lines %d-%d of %d. Full output: %s]",
			startLine, endLine, tr.TotalLines, fullPath)
	} else {
		footer = fmt.Sprintf("[Showing lines %d-%d of %d (%s limit). Full output: %s]",
			startLine, endLine, tr.TotalLines, FormatSize(DefaultMaxBytes), fullPath)
	}
	if text == "" {
		return footer
	}
	return text + "\n\n" + footer
}
