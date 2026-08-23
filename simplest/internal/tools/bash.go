package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
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
	cwd string
}

// NewBashTool creates a bash tool that runs commands in cwd.
func NewBashTool(cwd string) *BashTool {
	return &BashTool{cwd: cwd}
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
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var buf bytes.Buffer
	// Same writer for both streams makes os/exec interleave them on one pipe.
	cmd.Stdout = &buf
	cmd.Stderr = &buf

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

	content := buf.String()
	truncation := TruncateTail(content, TruncationOptions{})
	text := truncation.Content
	details := &BashToolDetails{}
	if truncation.Truncated {
		if fullPath, saveErr := saveFullOutput(content); saveErr == nil {
			details.FullOutputPath = fullPath
		}
		details.Truncation = &truncation
		text = appendTruncationFooter(text, &truncation, details.FullOutputPath)
	}

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

	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s", appendStatus("Command aborted"))
	}
	if timedOut {
		timeoutDisplay := ""
		if in.Timeout != nil {
			timeoutDisplay = strconv.FormatFloat(*in.Timeout, 'f', -1, 64)
		}
		return nil, fmt.Errorf("%s", appendStatus(fmt.Sprintf("Command timed out after %s seconds", timeoutDisplay)))
	}
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			return nil, fmt.Errorf("%s", appendStatus(fmt.Sprintf("Command exited with code %d", exitErr.ExitCode())))
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

func saveFullOutput(content string) (string, error) {
	f, err := os.CreateTemp("", "simplest-bash-*.txt")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
