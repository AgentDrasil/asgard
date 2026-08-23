package simplest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/simplest"
)

// mockProvider implements simplest.Provider without network calls.
type mockProvider struct {
	responses []*simplest.AssistantMessage
}

func (m *mockProvider) Stream(ctx context.Context, model *simplest.Model, cx *simplest.Context, opts *simplest.StreamOptions) <-chan simplest.AssistantMessageEvent {
	ch := make(chan simplest.AssistantMessageEvent, 10)
	go func() {
		defer close(ch)
		for _, resp := range m.responses {
			// Emit start
			partial := &simplest.AssistantMessage{
				Content:   []simplest.AssistantContent{},
				API:       model.API,
				Provider:  model.Provider,
				Model:     model.ID,
				Timestamp: time.Now().UnixMilli(),
			}
			ch <- simplest.Partial{
				Kind:    simplest.EvStart,
				Partial: partial,
			}

			// Emit text deltas
			for _, blk := range resp.Content {
				if tc, ok := blk.(simplest.TextContent); ok {
					ch <- simplest.Partial{
						Kind:    simplest.EvTextDelta,
						Delta:   tc.Text,
						Partial: partial,
					}
				}
			}

			// Emit done
			ch <- simplest.DoneEvent{
				Kind:    simplest.EvDone,
				Reason:  simplest.StopStop,
				Message: resp,
			}
		}
	}()
	return ch
}

func TestPrompt_EndToEndWithCallbackAndSession(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	testDir := filepath.Join(tempHome, "workspace")
	require.NoError(t, os.MkdirAll(testDir, 0755))

	mockResp := &simplest.AssistantMessage{
		Content: []simplest.AssistantContent{
			simplest.TextContent{Type: "text", Text: "Hello from simplest!"},
		},
		Usage: simplest.Usage{
			Input:       42,
			Output:      10,
			TotalTokens: 52,
		},
		StopReason: simplest.StopStop,
		Timestamp:  time.Now().UnixMilli(),
	}

	mockP := &mockProvider{
		responses: []*simplest.AssistantMessage{mockResp},
	}

	testModel := &simplest.Model{
		ID:            "test-model",
		Name:          "Test Model",
		Provider:      "mock",
		API:           "mock",
		ContextWindow: 1048576,
	}

	SetProviderResolver(func(modelID string) (*simplest.Model, simplest.Provider, error) {
		return testModel, mockP, nil
	})
	t.Cleanup(ResetProviderResolver)

	var reportedUpdates []struct {
		stepIndex int
		source    string
		entryType string
		content   string
		metadata  map[string]any
	}

	cb := func(stepIndex int, source, entryType, content string, metadata map[string]any) {
		reportedUpdates = append(reportedUpdates, struct {
			stepIndex int
			source    string
			entryType string
			content   string
			metadata  map[string]any
		}{
			stepIndex: stepIndex,
			source:    source,
			entryType: entryType,
			content:   content,
			metadata:  metadata,
		})
	}

	// 1. First prompt: New session
	res, err := Prompt(context.Background(), "Hi there", types.PromptOptions{
		Dir:            testDir,
		ReportCallback: cb,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, res.SessionID)
	assert.Equal(t, "Hello from simplest!", res.LastContent)
	assert.Equal(t, 42, res.InputTokens)
	assert.Equal(t, 1048576, res.MaxTokens)
	assert.Equal(t, 1.0, res.Remaining)

	require.NotEmpty(t, reportedUpdates)
	assert.Equal(t, "MODEL", reportedUpdates[0].source)
	assert.Equal(t, "agent_response", reportedUpdates[0].entryType)
	assert.Equal(t, "Hello from simplest!", reportedUpdates[0].content)

	// 2. Second prompt: Resume with same SessionID
	resumedResp := &simplest.AssistantMessage{
		Content: []simplest.AssistantContent{
			simplest.TextContent{Type: "text", Text: "Continuing session conversation."},
		},
		Usage: simplest.Usage{
			Input:       84,
			Output:      15,
			TotalTokens: 99,
		},
		StopReason: simplest.StopStop,
		Timestamp:  time.Now().UnixMilli(),
	}
	mockP.responses = []*simplest.AssistantMessage{resumedResp}

	res2, err := Prompt(context.Background(), "Next instruction", types.PromptOptions{
		Dir:       testDir,
		SessionID: res.SessionID,
	})
	require.NoError(t, err)
	assert.Equal(t, res.SessionID, res2.SessionID)
	assert.Equal(t, "Continuing session conversation.", res2.LastContent)
	assert.Equal(t, 84, res2.InputTokens)
}

func TestExtractTargetFiles(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"/tmp/code.go"}, extractTargetFiles("write", map[string]any{"path": "/tmp/code.go"}))
	assert.Equal(t, []string{"/src/file.txt"}, extractTargetFiles("edit", map[string]any{"filePath": "/src/file.txt"}))
	assert.Nil(t, extractTargetFiles("read", map[string]any{"path": "/src/file.txt"}))
	assert.Nil(t, extractTargetFiles("bash", map[string]any{"command": "ls"}))
}

func TestPrompt_ResolverError(t *testing.T) {
	SetProviderResolver(func(modelID string) (*simplest.Model, simplest.Provider, error) {
		return nil, nil, fmt.Errorf("model not found: %s", modelID)
	})
	t.Cleanup(ResetProviderResolver)

	_, err := Prompt(context.Background(), "test", types.PromptOptions{
		Model: "non-existent",
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "model not found: non-existent")
}

// multiTurnMockProvider delivers one assistant turn response per Stream call.
type multiTurnMockProvider struct {
	mu    sync.Mutex
	turns []*simplest.AssistantMessage
	turn  int
}

func (m *multiTurnMockProvider) Stream(ctx context.Context, model *simplest.Model, cx *simplest.Context, opts *simplest.StreamOptions) <-chan simplest.AssistantMessageEvent {
	ch := make(chan simplest.AssistantMessageEvent, 10)
	go func() {
		defer close(ch)
		m.mu.Lock()
		if m.turn >= len(m.turns) {
			m.mu.Unlock()
			return
		}
		resp := m.turns[m.turn]
		m.turn++
		m.mu.Unlock()

		partial := &simplest.AssistantMessage{
			Content:   []simplest.AssistantContent{},
			API:       model.API,
			Provider:  model.Provider,
			Model:     model.ID,
			Timestamp: time.Now().UnixMilli(),
		}
		ch <- simplest.Partial{
			Kind:    simplest.EvStart,
			Partial: partial,
		}

		for _, blk := range resp.Content {
			switch b := blk.(type) {
			case simplest.TextContent:
				ch <- simplest.Partial{
					Kind:    simplest.EvTextDelta,
					Delta:   b.Text,
					Partial: partial,
				}
			case simplest.ThinkingContent:
				ch <- simplest.Partial{
					Kind:    simplest.EvThinkingDelta,
					Delta:   b.Thinking,
					Partial: partial,
				}
			case simplest.ToolCall:
				ch <- simplest.Partial{
					Kind:     simplest.EvToolcallDelta,
					Delta:    string(b.Arguments),
					ToolCall: &b,
					Partial:  partial,
				}
			}
		}

		stopReason := resp.StopReason
		if stopReason == "" {
			stopReason = simplest.StopStop
		}
		ch <- simplest.DoneEvent{
			Kind:    simplest.EvDone,
			Reason:  stopReason,
			Message: resp,
		}
	}()
	return ch
}

func TestPrompt_MultiTurnLastContent(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	testDir := filepath.Join(tempHome, "workspace")
	require.NoError(t, os.MkdirAll(testDir, 0755))

	// Create a dummy file so read tool succeeds
	require.NoError(t, os.WriteFile(filepath.Join(testDir, "test.txt"), []byte("file content"), 0644))

	turn1 := &simplest.AssistantMessage{
		Content: []simplest.AssistantContent{
			simplest.TextContent{Type: "text", Text: "Let me check the file.\n"},
			simplest.ToolCall{
				Type:      "toolCall",
				ID:        "call-1",
				Name:      "read",
				Arguments: []byte(`{"path":"test.txt"}`),
			},
		},
		Usage: simplest.Usage{
			Input:       30,
			Output:      15,
			TotalTokens: 45,
		},
		StopReason: simplest.StopToolUse,
		Timestamp:  time.Now().UnixMilli(),
	}

	turn2 := &simplest.AssistantMessage{
		Content: []simplest.AssistantContent{
			simplest.TextContent{Type: "text", Text: "Here is the final result."},
		},
		Usage: simplest.Usage{
			Input:       75,
			Output:      25,
			TotalTokens: 100,
		},
		StopReason: simplest.StopStop,
		Timestamp:  time.Now().UnixMilli(),
	}

	mockP := &multiTurnMockProvider{
		turns: []*simplest.AssistantMessage{turn1, turn2},
	}

	testModel := &simplest.Model{
		ID:            "test-model",
		Name:          "Test Model",
		Provider:      "mock",
		API:           "mock",
		ContextWindow: 1048576,
	}

	SetProviderResolver(func(modelID string) (*simplest.Model, simplest.Provider, error) {
		return testModel, mockP, nil
	})
	t.Cleanup(ResetProviderResolver)

	var reportedEvents []struct {
		entryType string
		content   string
	}
	cb := func(stepIndex int, source, entryType, content string, metadata map[string]any) {
		reportedEvents = append(reportedEvents, struct {
			entryType string
			content   string
		}{
			entryType: entryType,
			content:   content,
		})
	}

	res, err := Prompt(context.Background(), "Read test.txt", types.PromptOptions{
		Dir:            testDir,
		ReportCallback: cb,
	})
	require.NoError(t, err)

	// D2 check: LastContent must be ONLY the final assistant message text, not concatenated
	assert.Equal(t, "Here is the final result.", res.LastContent)
	assert.Equal(t, 75, res.InputTokens)

	// D1 check: No agent_response callback carries tool argument fragments or non-text deltas
	var agentResponses []string
	var toolCalls []string
	for _, ev := range reportedEvents {
		switch ev.entryType {
		case "agent_response":
			agentResponses = append(agentResponses, ev.content)
		case "tool_call":
			toolCalls = append(toolCalls, ev.content)
		}
	}

	assert.Equal(t, []string{"Let me check the file.\n", "Here is the final result."}, agentResponses)
	assert.NotEmpty(t, toolCalls)
}

func TestPrompt_ThinkingAndToolcallDeltasNotStreamedAsAgentResponse(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	testDir := filepath.Join(tempHome, "workspace")
	require.NoError(t, os.MkdirAll(testDir, 0755))

	resp := &simplest.AssistantMessage{
		Content: []simplest.AssistantContent{
			simplest.ThinkingContent{Type: "thinking", Thinking: "internal thinking reasoning"},
			simplest.TextContent{Type: "text", Text: "User-facing message."},
		},
		Usage: simplest.Usage{
			Input:       50,
			Output:      20,
			TotalTokens: 70,
		},
		StopReason: simplest.StopStop,
		Timestamp:  time.Now().UnixMilli(),
	}

	mockP := &multiTurnMockProvider{
		turns: []*simplest.AssistantMessage{resp},
	}

	testModel := &simplest.Model{
		ID:            "test-model",
		Name:          "Test Model",
		Provider:      "mock",
		API:           "mock",
		ContextWindow: 1048576,
	}

	SetProviderResolver(func(modelID string) (*simplest.Model, simplest.Provider, error) {
		return testModel, mockP, nil
	})
	t.Cleanup(ResetProviderResolver)

	var reportedAgentResponses []string
	cb := func(stepIndex int, source, entryType, content string, metadata map[string]any) {
		if entryType == "agent_response" {
			reportedAgentResponses = append(reportedAgentResponses, content)
		}
	}

	res, err := Prompt(context.Background(), "Think and answer", types.PromptOptions{
		Dir:            testDir,
		ReportCallback: cb,
	})
	require.NoError(t, err)

	assert.Equal(t, "User-facing message.", res.LastContent)
	assert.Equal(t, []string{"User-facing message."}, reportedAgentResponses)
}
