package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

func TestFuncTool(t *testing.T) {
	ft := &Func{
		ToolName:        "weather",
		ToolDescription: "look up weather",
		Snippet:         "query current weather",
		Fn: func(ctx context.Context, id string, args json.RawMessage, onUpdate types.UpdateFunc) (*types.ToolResult, error) {
			return &types.ToolResult{
				Content: []types.AssistantContent{types.TextContent{Type: "text", Text: "sunny"}},
			}, nil
		},
	}
	r := DefaultRegistry(t.TempDir())
	if err := r.Register(ft); err != nil {
		t.Fatal(err)
	}
	got, ok := r.Get("weather")
	if !ok {
		t.Fatal("tool not registered")
	}
	res, err := got.Execute(context.Background(), "c1", json.RawMessage(`{"city":"x"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	blocks, _ := types.DecodeToolResultContent(mustMarshalBlocks(res.Content))
	if types.StringContentOf(blocks) != "sunny" {
		t.Fatalf("result: %q", types.StringContentOf(blocks))
	}
	if got.Parameters() == nil {
		t.Fatal("default schema missing")
	}

	// Registering over an existing name replaces it.
	replacement := &Func{ToolName: "weather", ToolDescription: "v2"}
	if err := r.Register(replacement); err != nil {
		t.Fatal(err)
	}
	got2, _ := r.Get("weather")
	if got2.Description() != "v2" {
		t.Fatalf("replacement failed: %q", got2.Description())
	}
}

func mustMarshalBlocks(blocks []types.AssistantContent) json.RawMessage {
	raw, err := types.MarshalBlocks(blocks)
	if err != nil {
		panic(err)
	}
	return raw
}
