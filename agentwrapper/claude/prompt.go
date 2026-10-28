package claude

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/AgentDrasil/asgard/agentwrapper/common"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

// effortVariants are the model variants claude accepts through --effort.
var effortVariants = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

// SplitModelVariant parses a model string with an optional effort suffix
// (e.g. "opus/high" -> "opus", "high").
func SplitModelVariant(model string) (string, string) {
	return types.SplitModelVariant(model)
}

func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating session id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// agentSetup is the contract-derived part of the claude invocation.
type agentSetup struct {
	appendPrompt    string
	disallowedTools []string
	// appendPromptFile is the temp file holding appendPrompt; set by Prompt.
	appendPromptFile string
}

// prepareAgent translates the AW_AGENTS.md contract into claude flags. With
// no contract mounted (plain `aw claude` outside the sandbox) it returns the
// zero value and claude runs with its native defaults.
func prepareAgent(opts types.PromptOptions) (agentSetup, error) {
	toolAccess := strings.TrimSpace(opts.ToolAccess)
	if toolAccess != "" && toolAccess != types.ToolAccessFull {
		return agentSetup{}, fmt.Errorf("unsupported tool access %q", toolAccess)
	}

	contract, ok := common.Load()
	if !ok {
		return agentSetup{}, nil
	}
	if toolAccess == "" {
		toolAccess = strings.TrimSpace(contract.ToolAccess)
	}
	if toolAccess != "" && toolAccess != types.ToolAccessFull {
		return agentSetup{}, fmt.Errorf("unsupported tool access %q", toolAccess)
	}

	c := &Client{}
	setup := agentSetup{
		appendPrompt: common.ComposePrompt(c.SystemPromptHeader(), c.SystemPromptPeerHeader(), contract.Team, contract.Body),
		// User clarification goes through /bin/ask-user.
		disallowedTools: []string{"AskUserQuestion"},
	}
	// Delegation goes through /bin/call-peer for team agents.
	if strings.TrimSpace(contract.Team) != "" {
		setup.disallowedTools = append(setup.disallowedTools, "Task", "Agent")
	}
	return setup, nil
}

// buildPromptArgv constructs the claude CLI arguments. sessionID is the
// already-resolved id and resume tells whether it continues an existing one.
func buildPromptArgv(prompt string, opts types.PromptOptions, setup agentSetup, sessionID string, resume bool) []string {
	argv := []string{"--print", "--verbose", "--output-format", "stream-json", "--dangerously-skip-permissions"}
	if resume {
		argv = append(argv, "--resume", sessionID)
	} else {
		argv = append(argv, "--session-id", sessionID)
	}
	if opts.Model != "" {
		base, variant := SplitModelVariant(opts.Model)
		argv = append(argv, "--model", base)
		if effortVariants[variant] {
			argv = append(argv, "--effort", variant)
		}
	}
	if setup.appendPromptFile != "" {
		argv = append(argv, "--append-system-prompt-file", setup.appendPromptFile)
	}
	if len(setup.disallowedTools) > 0 {
		argv = append(argv, "--disallowedTools", strings.Join(setup.disallowedTools, ","))
	}
	return append(argv, "--", prompt)
}

// Prompt sends a prompt to claude and parses its stream-json output in
// real-time, invoking opts.ReportCallback for each meaningful entry.
func Prompt(ctx context.Context, prompt string, opts types.PromptOptions) (*types.PromptResult, error) {
	setup, err := prepareAgent(opts)
	if err != nil {
		return nil, err
	}

	// The prompt travels as a file: a long contract body would exceed the
	// per-argument size limit (MAX_ARG_STRLEN, 128 KiB) of argv.
	if setup.appendPrompt != "" {
		f, err := os.CreateTemp("", "aw-claude-prompt-*.md")
		if err != nil {
			return nil, fmt.Errorf("creating system prompt file: %w", err)
		}
		defer func() { _ = os.Remove(f.Name()) }()
		_, werr := f.WriteString(setup.appendPrompt)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return nil, fmt.Errorf("writing system prompt file: %w", werr)
		}
		setup.appendPromptFile = f.Name()
	}

	sessionID, resume := opts.SessionID, opts.SessionID != ""
	if !resume {
		if sessionID, err = newSessionID(); err != nil {
			return nil, err
		}
	}

	cmd := exec.CommandContext(ctx, "claude", buildPromptArgv(prompt, opts, setup, sessionID, resume)...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	// claude spawns children (MCP servers, background shells). Run it in its
	// own process group and kill the whole group on cancel, otherwise a
	// grandchild holding stderr keeps Wait blocked after the context ends.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	cmd.Env = commandEnv()
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating claude stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting claude: %w", err)
	}

	// A descendant that outlives claude (and keeps stdout open) would leave
	// consumeStream blocked forever, and WaitDelay cannot help because Wait
	// has not been called yet. Close our read end once the context is done
	// and the grace period has elapsed.
	stop := context.AfterFunc(ctx, func() {
		time.AfterFunc(cmd.WaitDelay, func() { _ = stdout.Close() })
	})
	defer stop()

	maxTokens := types.GetModelContextWindow(types.StripVariant(opts.Model))
	out, parseErr := consumeStream(stdout, maxTokens, opts.ReportCallback)
	waitErr := cmd.Wait()

	if waitErr != nil || (out != nil && out.errMessage != "") {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var parts []string
		if out != nil && out.errMessage != "" {
			parts = append(parts, out.errMessage)
		}
		if msg := strings.TrimSpace(stderrBuf.String()); msg != "" {
			parts = append(parts, msg)
		}
		err := waitErr
		if err == nil {
			err = fmt.Errorf("claude reported an error")
		}
		if len(parts) > 0 {
			return nil, fmt.Errorf("running claude prompt: %w: %s", err, strings.Join(parts, "; "))
		}
		return nil, fmt.Errorf("running claude prompt: %w", err)
	}
	if parseErr != nil {
		return nil, parseErr
	}

	remaining := 1.0
	if maxTokens > 0 {
		remaining = 1.0 - float64(out.inputTokens)/float64(maxTokens)
		if remaining < 0 {
			remaining = 0
		}
	}
	if out.sessionID != "" {
		sessionID = out.sessionID
	}
	return &types.PromptResult{
		SessionID:   sessionID,
		InputTokens: out.inputTokens,
		MaxTokens:   maxTokens,
		Remaining:   remaining,
		LastContent: out.lastContent,
	}, nil
}
