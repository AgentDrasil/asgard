package workflow

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFunctionRegistry_RegisterAndGet(t *testing.T) {
	t.Parallel()

	r := NewFunctionRegistry()
	fn := func(ctx context.Context, nctx *NodeContext) (string, error) {
		return "hello", nil
	}
	r.Register("greet", fn)

	got, ok := r.Get("greet")
	require.True(t, ok)
	assert.NotNil(t, got)

	out, err := got(t.Context(), &NodeContext{})
	require.NoError(t, err)
	assert.Equal(t, "hello", out)
}

func TestFunctionRegistry_Missing(t *testing.T) {
	t.Parallel()

	r := NewFunctionRegistry()
	got, ok := r.Get("ghost")
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestFunctionRegistry_OverrideRegistration(t *testing.T) {
	t.Parallel()

	r := NewFunctionRegistry()
	r.Register("fn", func(ctx context.Context, nctx *NodeContext) (string, error) {
		return "v1", nil
	})
	r.Register("fn", func(ctx context.Context, nctx *NodeContext) (string, error) {
		return "v2", nil
	})

	got, ok := r.Get("fn")
	require.True(t, ok)
	out, err := got(t.Context(), &NodeContext{})
	require.NoError(t, err)
	assert.Equal(t, "v2", out)
}

func TestFunctionRegistry_ParentFallback(t *testing.T) {
	t.Parallel()

	parent := NewFunctionRegistry()
	parent.Register("shared", func(ctx context.Context, nctx *NodeContext) (string, error) {
		return "from-parent", nil
	})
	parent.Register("only-parent", func(ctx context.Context, nctx *NodeContext) (string, error) {
		return "parent-only", nil
	})

	child := NewFunctionRegistryWithParent(parent)
	child.Register("shared", func(ctx context.Context, nctx *NodeContext) (string, error) {
		return "from-child", nil
	})

	// Current registry takes precedence over parent.
	got, ok := child.Get("shared")
	require.True(t, ok)
	out, err := got(t.Context(), &NodeContext{})
	require.NoError(t, err)
	assert.Equal(t, "from-child", out)

	// Missing in child falls back to parent.
	got, ok = child.Get("only-parent")
	require.True(t, ok)
	out, err = got(t.Context(), &NodeContext{})
	require.NoError(t, err)
	assert.Equal(t, "parent-only", out)

	// Missing everywhere.
	_, ok = child.Get("ghost")
	assert.False(t, ok)
}

func TestFunctionRegistry_List(t *testing.T) {
	t.Parallel()

	parent := NewFunctionRegistry()
	parent.Register("b_fn", func(ctx context.Context, nctx *NodeContext) (string, error) { return "", nil })
	parent.Register("a_fn", func(ctx context.Context, nctx *NodeContext) (string, error) { return "", nil })

	child := NewFunctionRegistryWithParent(parent)
	child.Register("c_fn", func(ctx context.Context, nctx *NodeContext) (string, error) { return "", nil })

	assert.Equal(t, []string{"a_fn", "b_fn"}, parent.List())
	assert.Equal(t, []string{"a_fn", "b_fn", "c_fn"}, child.List())
}

func TestFunctionRegistry_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	r := NewFunctionRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("fn_%d", i%10)
			r.Register(name, func(ctx context.Context, nctx *NodeContext) (string, error) {
				return name, nil
			})
		}(i)
	}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if fn, ok := r.Get(fmt.Sprintf("fn_%d", i%10)); ok {
				_, _ = fn(t.Context(), &NodeContext{})
			}
			_ = r.List()
		}(i)
	}
	wg.Wait()

	assert.Len(t, r.List(), 10)
}

// Global-registry tests: no t.Parallel() and globally unique names to avoid
// cross-test pollution of the process-wide default registry.

func TestGlobalFunctionHelpers(t *testing.T) {
	const name = "test_global_helper_unique_name_xyz"

	_, ok := GetFunction(name)
	assert.False(t, ok)

	RegisterFunction(name, func(ctx context.Context, nctx *NodeContext) (string, error) {
		return "global-ok", nil
	})

	fn, ok := GetFunction(name)
	require.True(t, ok)
	out, err := fn(t.Context(), &NodeContext{})
	require.NoError(t, err)
	assert.Equal(t, "global-ok", out)

	assert.Contains(t, DefaultFunctionRegistry().List(), name)
}
