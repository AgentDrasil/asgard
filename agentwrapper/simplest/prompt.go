package simplest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/simplest"
)

// ProviderResolver resolves a model and provider given a model ID string.
type ProviderResolver func(modelID string) (*simplest.Model, simplest.Provider, error)

var (
	resolveMu       sync.RWMutex
	resolveProvider ProviderResolver = simplest.ResolveModelAndProvider
)

// SetProviderResolver overrides the default provider resolver (useful for testing).
func SetProviderResolver(resolver ProviderResolver) {
	resolveMu.Lock()
	defer resolveMu.Unlock()
	resolveProvider = resolver
}

// ResetProviderResolver resets the provider resolver to the default implementation.
func ResetProviderResolver() {
	resolveMu.Lock()
	defer resolveMu.Unlock()
	resolveProvider = simplest.ResolveModelAndProvider
}

func getProviderResolver() ProviderResolver {
	resolveMu.RLock()
	defer resolveMu.RUnlock()
	if resolveProvider != nil {
		return resolveProvider
	}
	return simplest.ResolveModelAndProvider
}

// Prompt runs simplest agent loop in-process and returns structured PromptResult.
func Prompt(ctx context.Context, prompt string, opts types.PromptOptions) (*types.PromptResult, error) {
	runDir := opts.Dir
	if runDir == "" {
		var err error
		runDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("getting current working directory: %w", err)
		}
	}

	resolver := getProviderResolver()
	model, prov, err := resolver(opts.Model)
	if err != nil {
		return nil, fmt.Errorf("resolving model and provider: %w", err)
	}

	baseDir := simplest.DefaultBaseDir()
	mgr := simplest.New(baseDir)

	var sf *simplest.SessionFile
	if opts.SessionID != "" {
		sessionDir, err := mgr.SessionDir(runDir)
		if err != nil {
			return nil, fmt.Errorf("getting session dir: %w", err)
		}

		// Look for existing session file matching opts.SessionID
		var matchPath string
		if dirents, err := os.ReadDir(sessionDir); err == nil {
			for _, de := range dirents {
				if de.IsDir() || !strings.HasSuffix(de.Name(), ".jsonl") {
					continue
				}
				p := filepath.Join(sessionDir, de.Name())
				header, _, err := simplest.LoadSessionFile(p)
				if err == nil && header != nil && header.ID == opts.SessionID {
					matchPath = p
					break
				}
			}
		}

		if matchPath != "" {
			sf, err = mgr.Open(matchPath)
			if err != nil {
				return nil, fmt.Errorf("opening session file %s: %w", matchPath, err)
			}
		} else {
			sf, err = mgr.Create(runDir, &simplest.CreateOptions{ID: opts.SessionID})
			if err != nil {
				return nil, fmt.Errorf("creating session with id %s: %w", opts.SessionID, err)
			}
		}
	} else {
		sf, err = mgr.Create(runDir, nil)
		if err != nil {
			return nil, fmt.Errorf("creating session: %w", err)
		}
	}

	// Append user prompt message to session
	userMsg := &simplest.UserMessage{
		Content:   simplest.TextOnly(prompt),
		Timestamp: time.Now().UnixMilli(),
	}
	if _, err := sf.AppendMessage(userMsg); err != nil {
		return nil, fmt.Errorf("appending user message to session: %w", err)
	}

	// Build context from session history
	sessionCtx, err := sf.BuildContext("")
	if err != nil {
		return nil, fmt.Errorf("building session context: %w", err)
	}

	// Assemble tools
	reg := simplest.DefaultRegistry(runDir)
	toolList := reg.Tools()
	toolNames := make([]string, 0, len(toolList))
	for _, t := range toolList {
		toolNames = append(toolNames, t.Name())
	}

	// Build system prompt
	contextFiles := simplest.LoadProjectContextFiles(runDir, baseDir)
	sysPrompt := simplest.BuildSystemPrompt(simplest.PromptBuildOptions{
		SelectedTools: toolNames,
		CWD:           runDir,
		ContextFiles:  contextFiles,
	})

	req := simplest.Request{
		SystemPrompt: sysPrompt,
		Messages:     sessionCtx.Messages,
		Model:        model,
		Provider:     prov,
		Tools:        toolList,
	}

	maxTokens := int(model.ContextWindow)
	if maxTokens <= 0 {
		maxTokens = 1048576
	}

	var finalMessages []simplest.Message
	var lastAssistantContent strings.Builder
	var lastInputTokens int
	stepIndex := 0

	events := simplest.Run(ctx, req)
	for ev := range events {
		switch ev.Kind {
		case simplest.MessageUpdate:
			if ev.AssistantEv != nil {
				if part, ok := (*ev.AssistantEv).(simplest.Partial); ok {
					if part.Kind == simplest.EvTextDelta && part.Delta != "" {
						lastAssistantContent.WriteString(part.Delta)
						if opts.ReportCallback != nil {
							metadata := map[string]any{
								"max_tokens": maxTokens,
								"is_append":  true,
							}
							if lastInputTokens > 0 {
								metadata["input_tokens"] = lastInputTokens
								metadata["total_input_tokens"] = lastInputTokens
							}
							opts.ReportCallback(stepIndex, "MODEL", "agent_response", part.Delta, metadata)
						}
						stepIndex++
					}
				}
			}
		case simplest.ToolExecutionStart:
			var content string
			if ev.Args != nil {
				if b, err := json.Marshal(ev.Args); err == nil {
					content = string(b)
				}
			}
			if content == "" {
				content = fmt.Sprintf("Executing tool %s", ev.ToolName)
			}
			if opts.ReportCallback != nil {
				metadata := map[string]any{
					"max_tokens": maxTokens,
					"is_append":  false,
					"tool_name":  ev.ToolName,
				}
				if tfs := extractTargetFiles(ev.ToolName, ev.Args); len(tfs) > 0 {
					metadata["target_files"] = tfs
				}
				if lastInputTokens > 0 {
					metadata["input_tokens"] = lastInputTokens
					metadata["total_input_tokens"] = lastInputTokens
				}
				opts.ReportCallback(stepIndex, "MODEL", "tool_call", content, metadata)
			}
			stepIndex++
		case simplest.ToolExecutionEnd:
			var content string
			if ev.Result != nil {
				content = simplest.StringContentOf(ev.Result.Content)
			}
			if content == "" {
				content = fmt.Sprintf("Finished tool %s", ev.ToolName)
			}
			if opts.ReportCallback != nil {
				metadata := map[string]any{
					"max_tokens": maxTokens,
					"is_append":  false,
					"tool_name":  ev.ToolName,
				}
				if lastInputTokens > 0 {
					metadata["input_tokens"] = lastInputTokens
					metadata["total_input_tokens"] = lastInputTokens
				}
				opts.ReportCallback(stepIndex, "MODEL", "tool_call", content, metadata)
			}
			stepIndex++
		case simplest.TurnEnd:
			if ev.Message != nil && ev.Message.Usage.Input > 0 {
				lastInputTokens = int(ev.Message.Usage.Input)
			}
		case simplest.AgentEnd:
			finalMessages = ev.Messages
		}
	}

	// Append newly produced messages to session file and flush
	for _, m := range finalMessages {
		if _, err := sf.AppendMessage(m); err != nil {
			return nil, fmt.Errorf("persisting session message: %w", err)
		}
	}
	if err := sf.Flush(); err != nil {
		return nil, fmt.Errorf("flushing session file: %w", err)
	}

	lastContent := ""
	for i := len(finalMessages) - 1; i >= 0; i-- {
		if am, ok := finalMessages[i].(*simplest.AssistantMessage); ok {
			if s := simplest.StringContentOf(am.Content); s != "" {
				lastContent = s
			}
			if am.Usage.Input > 0 {
				lastInputTokens = int(am.Usage.Input)
			}
			if lastContent != "" {
				break
			}
		}
	}
	if lastContent == "" {
		lastContent = lastAssistantContent.String()
	}

	return &types.PromptResult{
		SessionID:   sf.Header().ID,
		InputTokens: lastInputTokens,
		MaxTokens:   maxTokens,
		Remaining:   1.0,
		LastContent: lastContent,
	}, nil
}

func extractTargetFiles(toolName string, rawArgs any) []string {
	if toolName != "write" && toolName != "edit" {
		return nil
	}
	var argsMap map[string]any
	switch v := rawArgs.(type) {
	case map[string]any:
		argsMap = v
	case []byte:
		_ = json.Unmarshal(v, &argsMap)
	case json.RawMessage:
		_ = json.Unmarshal(v, &argsMap)
	case string:
		_ = json.Unmarshal([]byte(v), &argsMap)
	}
	if argsMap == nil {
		return nil
	}
	for _, key := range []string{"path", "filePath"} {
		if val, ok := argsMap[key].(string); ok && strings.TrimSpace(val) != "" {
			return []string{types.RemapSandboxPath(val)}
		}
	}
	return nil
}
