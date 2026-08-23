package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

type stubTool struct{ name string }

func (s stubTool) Name() string                           { return s.name }
func (s stubTool) Description() string                    { return "stub " + s.name }
func (s stubTool) Label() string                          { return s.name }
func (s stubTool) Parameters() json.RawMessage            { return json.RawMessage(`{"type":"object"}`) }
func (s stubTool) PromptSnippet() string                  { return "" }
func (s stubTool) PromptGuidelines() []string             { return nil }
func (s stubTool) ExecutionMode() types.ToolExecutionMode { return "" }
func (s stubTool) Execute(ctx context.Context, toolCallID string, args json.RawMessage, onUpdate types.UpdateFunc) (*types.ToolResult, error) {
	return &types.ToolResult{}, nil
}

func TestDefaultRegistryHasExactlySevenBuiltins(t *testing.T) {
	r := DefaultRegistry(t.TempDir())
	want := AllToolNames()
	got := r.Names()
	if len(got) != len(want) {
		t.Fatalf("registry names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("names[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, name := range want {
		tool, ok := r.Get(name)
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		if tool.Name() != name {
			t.Errorf("Get(%q).Name() = %q", name, tool.Name())
		}
	}
}

func TestRegistryRegisterReplacesSameName(t *testing.T) {
	r := NewRegistry()
	first := stubTool{name: "ls"}
	second := stubTool{name: "ls"}
	if err := r.Register(first); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(second); err != nil {
		t.Fatal(err)
	}
	got, ok := r.Get("ls")
	if !ok || got.Name() != "ls" {
		t.Fatal("ls missing after re-register")
	}
	if len(r.Names()) != 1 || len(r.Tools()) != 1 {
		t.Errorf("replacement should keep a single entry, got names=%v", r.Names())
	}

	if err := r.Register(stubTool{name: "extra"}); err != nil {
		t.Fatal(err)
	}
	names := r.Names()
	if len(names) != 2 || names[0] != "ls" || names[1] != "extra" {
		t.Errorf("registration order wrong: %v", names)
	}

	if err := r.Register(stubTool{name: ""}); err == nil {
		t.Error("expected error for empty tool name")
	}
}

func TestRegistryToolDefs(t *testing.T) {
	r := DefaultRegistry(t.TempDir())
	defs := r.ToolDefs()
	if len(defs) != 7 {
		t.Fatalf("got %d defs, want 7", len(defs))
	}
	seen := map[string]bool{}
	for _, def := range defs {
		seen[def.Name] = true
		if def.Description == "" {
			t.Errorf("tool %q has empty description", def.Name)
		}
		if !json.Valid(def.Parameters) {
			t.Errorf("tool %q has invalid parameters JSON: %s", def.Name, def.Parameters)
		}
		var schema map[string]any
		if err := json.Unmarshal(def.Parameters, &schema); err != nil {
			t.Errorf("tool %q parameters do not decode to an object: %v", def.Name, err)
		} else if schema["type"] != "object" {
			t.Errorf("tool %q schema type = %v, want object", def.Name, schema["type"])
		}
	}
	for _, name := range AllToolNames() {
		if !seen[name] {
			t.Errorf("missing ToolDef for %q", name)
		}
	}
}
