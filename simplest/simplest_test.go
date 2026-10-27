package simplest_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest"
)

func TestSession_ExportedUsage(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "usage", simplest.TypeUsage)

	mgr := simplest.New(t.TempDir())
	sf, err := mgr.Create(t.TempDir(), nil)
	require.NoError(t, err)

	usageData := &simplest.Usage{
		Input:       50,
		Output:      25,
		TotalTokens: 75,
	}
	id, err := sf.AppendUsage(usageData, "external test note")
	require.NoError(t, err)
	assert.NotEmpty(t, id)
}

func TestSession_ExportedContextEdit(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "context_edit", simplest.TypeContextEdit)

	mgr := simplest.New(t.TempDir())
	sf, err := mgr.Create(t.TempDir(), nil)
	require.NoError(t, err)

	u1, err := sf.AppendMessage(&simplest.UserMessage{
		Content:   simplest.TextOnly("hello"),
		Timestamp: 1000,
	})
	require.NoError(t, err)

	rep := "hello world"
	editID, err := sf.AppendContextEdit(u1, &rep)
	require.NoError(t, err)
	assert.NotEmpty(t, editID)

	ctx, err := sf.BuildContext("")
	require.NoError(t, err)
	require.Len(t, ctx.Messages, 1)
	userMsg, ok := ctx.Messages[0].(*simplest.UserMessage)
	require.True(t, ok)
	assert.Contains(t, string(userMsg.Content), rep)
}

func TestModelCompatAliasIsPublicSurface(t *testing.T) {
	// Proves external callers can build Model.Compat without importing
	// internal/types (compile-level assertion of the ModelCompat alias).
	m := simplest.Model{
		ID: "m1", Name: "M1", API: simplest.APIOpenAICompat, Provider: "p",
		Compat: &simplest.ModelCompat{MaxTokensField: "max_tokens"},
	}
	if m.Compat == nil || m.Compat.MaxTokensField != "max_tokens" {
		t.Fatalf("compat alias broken: %+v", m.Compat)
	}
}

// TestSetClientUserAgent_PublicSurface locks the public identity surface:
// an embedder can set the default client User-Agent via the facade setter,
// and provider requests then carry it.
func TestSetClientUserAgent_PublicSurface(t *testing.T) {
	// Serial: mutates the package-level UA override.
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\r\n\r\n", `{"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"}}]}`)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = fmt.Fprintf(w, "data: %s\r\n\r\n", `{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		_, _ = fmt.Fprintf(w, "data: %s\r\n\r\n", "[DONE]")
	}))
	defer srv.Close()

	simplest.SetClientUserAgent("test-agent/1")
	t.Cleanup(func() { simplest.SetClientUserAgent("") })

	p := simplest.NewOpenAICompat("sk-test")
	m := &simplest.Model{
		ID: "m", Name: "M", API: simplest.APIOpenAICompat, Provider: "p",
		BaseURL: srv.URL, ContextWindow: 128000, MaxTokens: 4096,
		Cost: simplest.ModelCost{ModelCostRates: simplest.ModelCostRates{Input: 1, Output: 1}},
	}
	cx := &simplest.Context{
		Messages: []simplest.Message{
			&simplest.UserMessage{Content: simplest.TextOnly("hi"), Timestamp: 1},
		},
	}
	for ev := range p.Stream(context.Background(), m, cx, nil) {
		if _, isErr := ev.(simplest.StreamErrorEvent); isErr {
			t.Fatalf("stream error: %+v", ev)
		}
	}
	require.Equal(t, "test-agent/1", gotUA)
}
