package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

type opencodeLine struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      struct {
		Type      string `json:"type"`
		Text      string `json:"text"`
		Reason    string `json:"reason"`
		MessageID string `json:"messageID"`
		Tool      string `json:"tool"`
		State     struct {
			Status string         `json:"status"`
			Input  map[string]any `json:"input"`
			Output string         `json:"output"`
		} `json:"state"`
		Tokens struct {
			Total int `json:"total"`
			Input int `json:"input"`
		} `json:"tokens"`
		// Legacy / alternative tool-call fields
		ToolName  string         `json:"toolName"`
		ToolInput map[string]any `json:"input"`
	} `json:"part"`
}

// classifyLine maps an opencode output line to an entry type.
func classifyLine(opl *opencodeLine) string {
	switch opl.Type {
	case "tool_use", "tool_result", "tool":
		return "tool_call"
	case "text":
		return "agent_response"
	default:
		if opl.Part.Type == "tool" || opl.Part.Reason == "tool_use" || opl.Part.Tool != "" || opl.Part.ToolName != "" {
			return "tool_call"
		}
		return "other"
	}
}

// SplitModelVariant parses a model string that may contain a variant suffix
// (e.g. "zai-coding-plan/glm-5.3/low" -> "zai-coding-plan/glm-5.3", "low") or
// ("openai/gpt-5/high" -> "openai/gpt-5", "high").
// Only a trailing segment matching a known variant is treated as a variant,
// so models with multi-segment provider paths (e.g.
// "openrouter/deepseek/deepseek-chat") are left intact.
// If no variant suffix is present, it returns (model, "").
func SplitModelVariant(model string) (string, string) {
	return types.SplitModelVariant(model)
}

// buildPromptArgv constructs the CLI arguments for running an opencode prompt.
func buildPromptArgv(prompt string, opts types.PromptOptions) []string {
	argv := []string{"run", "--format", "json", "--auto"}
	if opts.SessionID != "" {
		argv = append(argv, "--session", opts.SessionID)
	}
	if opts.Model != "" {
		baseModel, variant := SplitModelVariant(opts.Model)
		argv = append(argv, "--model", baseModel)
		if variant != "" {
			argv = append(argv, "--variant", variant)
		}
	}
	argv = append(argv, "--", prompt)
	return argv
}

// Prompt sends a prompt to opencode and parses its JSONL output in real-time.
// If opts.ReportCallback is set, it is called for each meaningful output line.
func Prompt(ctx context.Context, prompt string, opts types.PromptOptions) (*types.PromptResult, error) {
	argv := buildPromptArgv(prompt, opts)

	cmd := exec.CommandContext(ctx, "opencode", argv...)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating opencode stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting opencode: %w", err)
	}

	var sessionID string
	var inputTokens int
	var totalTokens int
	var targetMessageID string
	var lastToolOutput string
	stepIndex := 0

	// Map to accumulate text contents by messageID
	textMap := make(map[string]*strings.Builder)

	scanner := bufio.NewScanner(stdout)
	// Use a large buffer for potentially long lines.
	buf := make([]byte, 4*1024*1024)
	scanner.Buffer(buf, len(buf))

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		var opl opencodeLine
		if err := json.Unmarshal([]byte(trimmed), &opl); err != nil {
			continue
		}

		if opl.SessionID != "" {
			sessionID = opl.SessionID
		}

		if opl.Type == "text" && opl.Part.MessageID != "" {
			builder, exists := textMap[opl.Part.MessageID]
			if !exists {
				builder = &strings.Builder{}
				textMap[opl.Part.MessageID] = builder
			}
			builder.WriteString(opl.Part.Text)
		}

		if opl.Type == "step_finish" {
			if opl.Part.Tokens.Input > 0 {
				inputTokens = opl.Part.Tokens.Input
			}
			if opl.Part.Tokens.Total > 0 {
				totalTokens = opl.Part.Tokens.Total
			}
			if opl.Part.Reason == "stop" && opl.Part.MessageID != "" {
				targetMessageID = opl.Part.MessageID
			}
		}

		// Classify line and track tool/text updates.
		entryType := classifyLine(&opl)
		if entryType != "other" {
			content := opl.Part.Text
			toolName := opl.Part.ToolName
			if toolName == "" {
				toolName = opl.Part.Tool
			}
			if entryType == "tool_call" {
				if content == "" {
					content = opl.Part.State.Output
				}
				if content == "" && len(opl.Part.State.Input) > 0 {
					if inputBytes, err := json.Marshal(opl.Part.State.Input); err == nil {
						content = string(inputBytes)
					}
				}
				if content == "" && len(opl.Part.ToolInput) > 0 {
					if inputBytes, err := json.Marshal(opl.Part.ToolInput); err == nil {
						content = string(inputBytes)
					}
				}
				if content == "" {
					content = fmt.Sprintf("Executing tool %s", toolName)
				}
				lastToolOutput = content
			}

			if content != "" {
				if opts.ReportCallback != nil {
					metadata := map[string]any{
						"max_tokens": 1048576,
					}
					if entryType == "agent_response" {
						metadata["is_append"] = true
					} else {
						metadata["is_append"] = false
					}
					if toolName != "" {
						metadata["tool_name"] = toolName
					}
					if tfs := extractTargetFiles(toolName, opl.Part.State.Input, opl.Part.ToolInput); len(tfs) > 0 {
						metadata["target_files"] = tfs
					}
					if inputTokens > 0 {
						metadata["input_tokens"] = inputTokens
						metadata["total_input_tokens"] = inputTokens
					}
					opts.ReportCallback(stepIndex, "MODEL", entryType, content, metadata)
				}
				stepIndex++
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if stderrMsg := strings.TrimSpace(stderrBuf.String()); stderrMsg != "" {
			return nil, fmt.Errorf("running opencode prompt: %w: %s", err, stderrMsg)
		}
		return nil, fmt.Errorf("running opencode prompt: %w", err)
	}

	var lastContent string
	if targetMessageID != "" {
		if builder, exists := textMap[targetMessageID]; exists {
			lastContent = builder.String()
		}
	}
	if lastContent == "" && len(textMap) > 0 {
		// Fallback to the text of any completed/recorded message
		for _, builder := range textMap {
			if bStr := builder.String(); bStr != "" {
				lastContent = bStr
				break
			}
		}
	}
	if lastContent == "" && lastToolOutput != "" {
		lastContent = lastToolOutput
	}

	maxTokens := 1048576
	remaining := 1.0
	if maxTokens > 0 {
		remaining = 1.0 - (float64(totalTokens) / float64(maxTokens))
		if remaining < 0 {
			remaining = 0
		}
	}

	return &types.PromptResult{
		SessionID:   sessionID,
		InputTokens: inputTokens,
		MaxTokens:   maxTokens,
		Remaining:   remaining,
		LastContent: lastContent,
	}, nil
}

// fileModifyingTools is the set of built-in opencode tools that create or
// modify files. Mirrors opencode's own toToolKind() "edit" classification:
// edit, write, apply_patch, and the legacy "patch" alias.
//
// See: packages/opencode/test/acp/tool.test.ts (toToolKind / toLocations).
var fileModifyingTools = map[string]bool{
	"edit":        true,
	"write":       true,
	"apply_patch": true,
	"patch":       true,
}

// extractTargetFiles resolves the target file path(s) from a file-modifying
// tool's input payload, so the UI can surface them as artifacts.
//
// opencode's input schema is not uniform across tools:
//   - edit / write expose the path via the "filePath" field in the legacy
//     packages/opencode schema, and via "path" in the newer V2 core schema
//     (packages/core). Both keys are checked.
//   - apply_patch (and its "patch" alias) take a single "patchText" argument;
//     the target path is embedded inline as "*** Update File:" /
//     "*** Add File:" / "*** Move to:" / "*** Delete File:" headers rather
//     than a dedicated field. parsePatchTextFiles extracts every such path,
//     so a multi-file patch yields all of them.
//
// See: packages/opencode/test/acp/tool.test.ts (toToolKind / toLocations).
func extractTargetFiles(toolName string, inputs ...map[string]any) []string {
	if !fileModifyingTools[toolName] {
		return nil
	}
	for _, input := range inputs {
		if len(input) == 0 {
			continue
		}
		if toolName == "apply_patch" || toolName == "patch" {
			if pt, ok := input["patchText"].(string); ok && pt != "" {
				if files := parsePatchTextFiles(pt); len(files) > 0 {
					remapped := make([]string, 0, len(files))
					for _, f := range files {
						remapped = append(remapped, types.RemapSandboxPath(f))
					}
					return remapped
				}
			}
			continue
		}
		for _, key := range []string{"filePath", "path"} {
			if val, ok := input[key].(string); ok && strings.TrimSpace(val) != "" {
				return []string{types.RemapSandboxPath(val)}
			}
		}
	}
	return nil
}

// parsePatchTextFiles scans an apply_patch patchText and returns every target
// file path declared by a header marker, in order of appearance and de-duplicated.
func parsePatchTextFiles(patchText string) []string {
	scanner := bufio.NewScanner(strings.NewReader(patchText))
	var files []string
	seen := make(map[string]bool)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		for _, marker := range []string{"*** Update File:", "*** Add File:", "*** Move to:", "*** Delete File:"} {
			if strings.HasPrefix(line, marker) {
				f := strings.TrimSpace(strings.TrimPrefix(line, marker))
				if f != "" && !seen[f] {
					seen[f] = true
					files = append(files, f)
				}
				break
			}
		}
	}
	return files
}
