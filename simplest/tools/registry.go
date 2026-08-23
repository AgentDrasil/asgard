package tools

import (
	"fmt"
	"sync"

	"github.com/AgentDrasil/asgard/simplest/types"
)

// Registry holds the active tool set. Plugins register additional tools here;
// built-ins are installed by DefaultRegistry.
type Registry struct {
	mu     sync.RWMutex
	byName map[string]types.AgentTool
	order  []string
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: map[string]types.AgentTool{}}
}

// Register adds a tool, replacing any existing tool with the same name.
func (r *Registry) Register(tool types.AgentTool) error {
	if tool.Name() == "" {
		return fmt.Errorf("tool name must not be empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byName[tool.Name()]; !exists {
		r.order = append(r.order, tool.Name())
	}
	r.byName[tool.Name()] = tool
	return nil
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (types.AgentTool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.byName[name]
	return t, ok
}

// Names returns registered tool names in registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Tools returns all registered tools in registration order.
func (r *Registry) Tools() []types.AgentTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]types.AgentTool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.byName[name])
	}
	return out
}

// ToolDefs converts every registered tool to its provider-facing definition.
func (r *Registry) ToolDefs() []types.ToolDef {
	var out []types.ToolDef
	for _, t := range r.Tools() {
		out = append(out, types.ToolDef{Name: t.Name(), Description: t.Description(), Parameters: t.Parameters()})
	}
	return out
}

// AllToolNames lists the names of the seven built-in tools.
func AllToolNames() []string {
	return []string{"read", "bash", "edit", "write", "find", "grep", "ls"}
}

// DefaultRegistry builds a registry containing the seven built-in tools bound
// to cwd.
func DefaultRegistry(cwd string) *Registry {
	r := NewRegistry()
	for _, t := range []types.AgentTool{
		NewReadTool(cwd),
		NewBashTool(cwd),
		NewEditTool(cwd),
		NewWriteTool(cwd),
		NewFindTool(cwd),
		NewGrepTool(cwd),
		NewLsTool(cwd),
	} {
		_ = r.Register(t)
	}
	return r
}
