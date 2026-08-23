// Package exampleutil holds the small event-printing loop shared by the
// example mains.
package exampleutil

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	s "github.com/AgentDrasil/asgard/simplest"
)

// LastText returns the concatenated text blocks of an assistant message.
func LastText(m *s.AssistantMessage) string {
	out := ""
	for _, c := range m.Content {
		if t, ok := c.(s.TextContent); ok {
			out += t.Text
		}
	}
	return out
}

func now() int64 { return time.Now().UnixMilli() }

func userMsg(text string) *s.UserMessage {
	return &s.UserMessage{Content: s.TextOnly(text), Timestamp: now()}
}

// GeminiModel returns a ready-to-use gemini-2.5-flash model descriptor.
func GeminiModel() *s.Model {
	return &s.Model{
		ID:            "gemini-3.7-flash",
		Name:          "Gemini 3.7 Flash",
		API:           s.APIGoogleGenerativeAI,
		Provider:      "google",
		ContextWindow: 1_000_000,
		MaxTokens:     8192,
		Input:         []string{"text", "image"},
		Cost:          s.ModelCostRates{Input: 0.3, Output: 2.5},
	}
}

// NewRequest builds a basic s.Request with a single user message.
func NewRequest(model *s.Model, p s.Provider, systemPrompt, userText string, toolList []s.AgentTool) s.Request {
	return s.Request{
		SystemPrompt: systemPrompt,
		Messages:     []s.Message{userMsg(userText)},
		Model:        model,
		Provider:     p,
		Tools:        toolList,
	}
}

// RunAndPrint drives one s.Run and prints events to stdout.
func RunAndPrint(req s.Request) {
	events := s.Run(context.Background(), req)
	printed := 0 // MessageUpdate carries the full accumulated text; print only the new suffix
	for ev := range events {
		switch ev.Kind {
		case s.MessageStart:
			printed = 0
		case s.MessageUpdate:
			if ev.Message != nil {
				text := LastText(ev.Message)
				if len(text) > printed {
					fmt.Print(text[printed:])
					printed = len(text)
				}
			}
		case s.MessageEnd:
			printed = 0
			if ev.Message != nil && ev.Message.StopReason == s.StopError {
				fmt.Printf("\n[error] %s\n", ev.Message.ErrorMessage)
			}
		case s.ToolExecutionStart:
			fmt.Printf("\n[tool] %s %s\n", ev.ToolName, mustJSON(ev.Args))
		case s.ToolExecutionEnd:
			if ev.IsError {
				fmt.Printf("[tool error] %s\n", mustJSON(ev.Result))
			}
		case s.AgentEnd:
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
