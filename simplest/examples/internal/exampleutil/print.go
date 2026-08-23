// Package exampleutil holds the small event-printing loop shared by the
// example mains.
package exampleutil

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/AgentDrasil/asgard/simplest/agent"
	"github.com/AgentDrasil/asgard/simplest/types"
)

// LastText returns the concatenated text blocks of an assistant message.
func LastText(m *types.AssistantMessage) string {
	out := ""
	for _, c := range m.Content {
		if t, ok := c.(types.TextContent); ok {
			out += t.Text
		}
	}
	return out
}

func now() int64 { return time.Now().UnixMilli() }

func userMsg(text string) *types.UserMessage {
	return &types.UserMessage{Content: types.TextOnly(text), Timestamp: now()}
}

// GeminiModel returns a ready-to-use gemini-2.5-flash model descriptor.
func GeminiModel() *types.Model {
	return &types.Model{
		ID:            "gemini-2.5-flash",
		Name:          "Gemini 2.5 Flash",
		API:           types.APIGoogleGenerativeAI,
		Provider:      "google",
		ContextWindow: 1_000_000,
		MaxTokens:     8192,
		Input:         []string{"text", "image"},
		Cost:          types.ModelCostRates{Input: 0.3, Output: 2.5},
	}
}

// NewRequest builds a basic agent.Request with a single user message.
func NewRequest(model *types.Model, p types.Provider, systemPrompt, userText string, toolList []types.AgentTool) agent.Request {
	return agent.Request{
		SystemPrompt: systemPrompt,
		Messages:     []types.Message{userMsg(userText)},
		Model:        model,
		Provider:     p,
		Tools:        toolList,
	}
}

// RunAndPrint drives one agent.Run and prints events to stdout.
func RunAndPrint(req agent.Request) {
	events := agent.Run(context.Background(), req)
	printed := 0 // MessageUpdate carries the full accumulated text; print only the new suffix
	for ev := range events {
		switch ev.Kind {
		case types.MessageStart:
			printed = 0
		case types.MessageUpdate:
			if ev.Message != nil {
				text := LastText(ev.Message)
				if len(text) > printed {
					fmt.Print(text[printed:])
					printed = len(text)
				}
			}
		case types.MessageEnd:
			printed = 0
			if ev.Message != nil && ev.Message.StopReason == types.StopError {
				fmt.Printf("\n[error] %s\n", ev.Message.ErrorMessage)
			}
		case types.ToolExecutionStart:
			fmt.Printf("\n[tool] %s %s\n", ev.ToolName, mustJSON(ev.Args))
		case types.ToolExecutionEnd:
			if ev.IsError {
				fmt.Printf("[tool error] %s\n", mustJSON(ev.Result))
			}
		case types.AgentEnd:
			fmt.Println("\n--- done ---")
		}
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	if len(b) > 200 {
		b = append(b[:200], []byte("...")...)
	}
	return string(b)
}
