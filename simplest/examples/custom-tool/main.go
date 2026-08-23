// custom-tool: register an extra Go-func tool alongside the built-ins.
package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/AgentDrasil/asgard/simplest/examples/internal/exampleutil"
	"github.com/AgentDrasil/asgard/simplest/provider"
	"github.com/AgentDrasil/asgard/simplest/tools"
	"github.com/AgentDrasil/asgard/simplest/types"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	reg := tools.DefaultRegistry(cwd)
	err = reg.Register(&tools.Func{
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
		Fn: func(ctx context.Context, callID string, args json.RawMessage, onUpdate types.UpdateFunc) (*types.ToolResult, error) {
			var in struct {
				City string `json:"city"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, err
			}
			return &types.ToolResult{
				Content: []types.AssistantContent{
					types.TextContent{Type: "text", Text: "sunny, 22C in " + in.City},
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
		provider.NewGemini(os.Getenv("GEMINI_API_KEY")),
		"You are a helpful coding agent.",
		"What's the weather in Tokyo?",
		reg.Tools(),
	))
}
