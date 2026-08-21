package workflow

import (
	"context"
	"sort"
	"sync"
)

// WorkflowFunction is a natively implemented Go function invocable from
// `type: function` workflow nodes. It receives the node's runtime context and
// must honor cooperative cancellation via ctx (e.g. node timeouts).
type WorkflowFunction func(ctx context.Context, nctx *NodeContext) (string, error)

// FunctionRegistry is a thread-safe, optionally hierarchical registry of
// named WorkflowFunctions. Lookups fall back to the parent registry when the
// current one does not contain the name.
type FunctionRegistry struct {
	mu     sync.RWMutex
	funcs  map[string]WorkflowFunction
	parent *FunctionRegistry
}

// NewFunctionRegistry creates an empty root registry.
func NewFunctionRegistry() *FunctionRegistry {
	return &FunctionRegistry{funcs: make(map[string]WorkflowFunction)}
}

// NewFunctionRegistryWithParent creates a registry whose lookups fall back to
// the given parent.
func NewFunctionRegistryWithParent(parent *FunctionRegistry) *FunctionRegistry {
	r := NewFunctionRegistry()
	r.parent = parent
	return r
}

// Register stores fn under name, replacing any previous registration.
func (r *FunctionRegistry) Register(name string, fn WorkflowFunction) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.funcs == nil {
		r.funcs = make(map[string]WorkflowFunction)
	}
	r.funcs[name] = fn
}

// Get returns the function registered under name, preferring the current
// registry and falling back to the parent chain.
func (r *FunctionRegistry) Get(name string) (WorkflowFunction, bool) {
	r.mu.RLock()
	fn, ok := r.funcs[name]
	r.mu.RUnlock()
	if ok {
		return fn, true
	}
	if r.parent != nil {
		return r.parent.Get(name)
	}
	return nil, false
}

// List returns the sorted names of all functions reachable from this registry
// (including inherited ones). Intended for debugging and management views.
func (r *FunctionRegistry) List() []string {
	seen := make(map[string]bool)
	for cur := r; cur != nil; cur = cur.parent {
		cur.mu.RLock()
		for name := range cur.funcs {
			seen[name] = true
		}
		cur.mu.RUnlock()
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// defaultFunctionRegistry is the process-wide fallback registry used by
// function nodes when no explicit registry is provided.
var defaultFunctionRegistry = NewFunctionRegistry()

// RegisterFunction registers fn in the default registry.
func RegisterFunction(name string, fn WorkflowFunction) {
	defaultFunctionRegistry.Register(name, fn)
}

// GetFunction looks up name in the default registry.
func GetFunction(name string) (WorkflowFunction, bool) {
	return defaultFunctionRegistry.Get(name)
}

// DefaultFunctionRegistry returns the process-wide default registry.
func DefaultFunctionRegistry() *FunctionRegistry {
	return defaultFunctionRegistry
}
