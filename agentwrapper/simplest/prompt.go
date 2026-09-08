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

// SplitModelVariant parses a model string that may contain a variant/thinking suffix
// (e.g. "zai-coding-plan/glm-5.3/low" -> "zai-coding-plan/glm-5.3", "low" or
// "gemini-3.7-flash/high" -> "gemini-3.7-flash", "high").
// Only a trailing segment matching a known variant is treated as a variant.
// If no variant suffix is present, it returns (model, "").
func SplitModelVariant(model string) (string, string) {
	return types.SplitModelVariant(model)
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
	targetModel := opts.Model
	var thinkingLevel simplest.ThinkingLevel
	var rawVariant string
	if baseModel, variant := SplitModelVariant(targetModel); variant != "" {
		targetModel = baseModel
		rawVariant = variant
		thinkingLevel = simplest.ThinkingLevel(variant)
	}

	model, prov, err := resolver(targetModel)
	if err != nil {
		return nil, fmt.Errorf("resolving model and provider: %w", err)
	}

	if rawVariant != "" && len(model.ReasoningEffort) > 0 && !model.SupportsReasoningEffort(rawVariant) {
		return nil, fmt.Errorf("unsupported reasoning effort %q for model %q: allowed values are %v", rawVariant, model.ID, model.ReasoningEffort)
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

	if thinkingLevel != "" {
		if _, err := sf.AppendThinkingLevelChange(thinkingLevel); err != nil {
			return nil, fmt.Errorf("appending thinking level change to session: %w", err)
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

	// Assemble tools according to the configured tool access mode
	reg := simplest.DefaultRegistry(runDir)
	toolList, toolNames, err := selectTools(opts.ToolAccess, runDir, reg.Tools(), docToolOptions())
	if err != nil {
		return nil, err
	}

	// Build system prompt
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting home directory: %w", err)
	}
	agentCfgDir := (&Client{}).AuthDirectory(home)
	contextFiles := simplest.LoadProjectContextFiles(runDir, agentCfgDir)
	customPrompt, contextFiles := agentIdentityPrompt(agentCfgDir, contextFiles)
	if customPrompt == "" && opts.ToolAccess == types.ToolAccessDocOnly {
		customPrompt = docOnlyIdentity
	}

	toolSnippets := make(map[string]string, len(toolList))
	toolGuidelines := make([]string, 0, len(toolList))
	for _, t := range toolList {
		toolSnippets[t.Name()] = t.PromptSnippet()
		toolGuidelines = append(toolGuidelines, t.PromptGuidelines()...)
	}

	sysPrompt := simplest.BuildSystemPrompt(simplest.PromptBuildOptions{
		CustomPrompt:     customPrompt,
		SelectedTools:    toolNames,
		ToolSnippets:     toolSnippets,
		PromptGuidelines: toolGuidelines,
		CWD:              runDir,
		ContextFiles:     contextFiles,
	})

	req := simplest.Request{
		SystemPrompt:  sysPrompt,
		Messages:      sessionCtx.Messages,
		Model:         model,
		Provider:      prov,
		Tools:         toolList,
		ThinkingLevel: thinkingLevel,
	}

	maxTokens := int(model.ContextWindow)
	if maxTokens <= 0 {
		maxTokens = types.GetModelContextWindow(targetModel)
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

// docOnlyIdentity replaces the default coding-assistant identity when a
// doc-only agent has no agent-level AGENTS.md, so the identity matches the
// trimmed tool set.
const docOnlyIdentity = "You are an analysis and documentation agent operating inside an agent harness. You read and search files, and you produce or revise markdown documents. You cannot run commands or modify source code."

// selectTools returns the tool set for the configured access mode. Unknown
// modes are rejected (fail closed): "" selects the full default set.
func selectTools(toolAccess, runDir string, allTools []simplest.AgentTool, docOpts simplest.DocToolOptions) ([]simplest.AgentTool, []string, error) {
	switch toolAccess {
	case "", types.ToolAccessFull:
		names := make([]string, 0, len(allTools))
		for _, t := range allTools {
			names = append(names, t.Name())
		}
		return allTools, names, nil
	case types.ToolAccessDocOnly:
		filtered := make([]simplest.AgentTool, 0, len(allTools))
		var names []string
		for _, t := range allTools {
			switch t.Name() {
			case "bash":
				continue
			case "write":
				t = simplest.NewWriteDocTool(runDir, docOpts)
			case "edit":
				t = simplest.NewEditDocTool(runDir, docOpts)
			}
			filtered = append(filtered, t)
			names = append(names, t.Name())
		}
		return filtered, names, nil
	default:
		return nil, nil, fmt.Errorf("unsupported tool access mode %q: must be %q or %q", toolAccess, types.ToolAccessFull, types.ToolAccessDocOnly)
	}
}

// docToolOptions loads the doc-tool path policy from the simplest config.
// Missing or unreadable config falls back to the default policy (any .md).
func docToolOptions() simplest.DocToolOptions {
	cfg, err := simplest.LoadConfig()
	if err != nil || cfg == nil {
		return simplest.DocToolOptions{}
	}
	return simplest.DocToolOptions{AllowedDirs: cfg.DocToolAllowedDirs}
}

// agentIdentityPrompt extracts the agent-level instruction file (mounted by
// the sandbox under agentCfgDir) from the loaded context files and returns
// its content to use as the system prompt identity, plus the remaining
// context files.
func agentIdentityPrompt(agentCfgDir string, files []simplest.ContextFile) (string, []simplest.ContextFile) {
	if len(files) == 0 {
		return "", files
	}
	prefix := strings.TrimSuffix(agentCfgDir, "/") + "/"
	if !strings.HasPrefix(files[0].Path, prefix) {
		return "", files
	}
	content := strings.TrimSpace(files[0].Content)
	if content == "" {
		return "", files
	}
	return content, files[1:]
}

func extractTargetFiles(toolName string, rawArgs any) []string {
	switch toolName {
	case "write", "edit", "write_doc", "edit_doc":
	default:
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
