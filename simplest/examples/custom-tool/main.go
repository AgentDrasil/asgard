// custom-tool: register an extra Go-func tool alongside the built-ins.
package main

import (
	"context"
	"encoding/json"
	"os"

	s "github.com/AgentDrasil/asgard/simplest"
	"github.com/AgentDrasil/asgard/simplest/examples/internal/exampleutil"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	reg := s.DefaultRegistry(cwd)
	err = reg.Register(&s.Func{
		ToolName:        "weather",
		ToolDescription: "Get current weather for a city",
		Snippet:         "query live weather", // shown in the system prompt
		ToolParams: json.RawMessage(`{
			"type": "object",
			"properties": {
				"city": {"type": "string", "description": "Name of the city"}
			},
			"required": ["city"]
		}`),
		Fn: func(ctx context.Context, callID string, args json.RawMessage, onUpdate s.UpdateFunc) (*s.ToolResult, error) {
			var in struct {
				City string `json:"city"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, err
			}
			return &s.ToolResult{
				Content: []s.AssistantContent{
					s.TextContent{Type: "text", Text: "sunny, 22C in " + in.City},
				},
				Details: map[string]any{"source": "example"},
			}, nil
		},
	})
	if err != nil {
		panic(err)
	}

	exampleutil.RunAndPrint(exampleutil.NewRequest(
		exampleutil.GeminiModel(),
		s.NewGemini(os.Getenv("GEMINI_API_KEY")),
		"You are a helpful coding agent.",
		"What's the weather in Tokyo?",
		reg.Tools(),
	))
}
