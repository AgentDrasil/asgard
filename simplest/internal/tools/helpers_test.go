package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

func execTool(t *testing.T, tool types.AgentTool, args string) (*types.ToolResult, error) {
	t.Helper()
	return tool.Execute(context.Background(), "test-call-id", json.RawMessage(args), nil)
}

func mustExec(t *testing.T, tool types.AgentTool, args string) *types.ToolResult {
	t.Helper()
	res, err := execTool(t, tool, args)
	if err != nil {
		t.Fatalf("Execute(%s) failed: %v", args, err)
	}
	return res
}

func textOf(t *testing.T, res *types.ToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("expected non-empty content")
	}
	tc, ok := res.Content[0].(types.TextContent)
	if !ok {
		t.Fatalf("expected first block to be TextContent, got %T", res.Content[0])
	}
	return tc.Text
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return string(data)
}

func mkdirIn(dir, name string) error {
	return os.MkdirAll(filepath.Join(dir, name), 0o755)
}
