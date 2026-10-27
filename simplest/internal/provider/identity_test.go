package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// The identity tests read and write the package-level UA override, so none of
// them may run in parallel with the others.

func TestFormatUserAgent(t *testing.T) {
	assert.Equal(t,
		"simplest/v9.9.9 ("+runtime.GOOS+"; "+runtime.Version()+")",
		formatUserAgent("v9.9.9"))
	assert.Equal(t,
		"simplest/dev ("+runtime.GOOS+"; "+runtime.Version()+")",
		formatUserAgent("dev"))
}

func TestDefaultUserAgent(t *testing.T) {
	ua := defaultUserAgent()
	assert.True(t, len(ua) > len("simplest/"), "UA must have a version: %q", ua)
	assert.Contains(t, ua, "simplest/")
	assert.Contains(t, ua, runtime.GOOS)
	assert.Contains(t, ua, runtime.Version())
}

func TestResolveModuleVersionFallsBackToDev(t *testing.T) {
	// In test builds the main module version is "(devel)" or the deps do not
	// include simplest, so the resolver must fall back to "dev".
	assert.Equal(t, "dev", resolveModuleVersion())
}

func TestResolveUserAgentOverrideAndFallback(t *testing.T) {
	t.Cleanup(func() { SetClientUserAgent("") })
	assert.Equal(t, defaultUserAgent(), resolveUserAgent())
	SetClientUserAgent("custom/1.2.3")
	assert.Equal(t, "custom/1.2.3", resolveUserAgent())
	SetClientUserAgent("")
	assert.Equal(t, defaultUserAgent(), resolveUserAgent())
}

func TestApplyUserAgent(t *testing.T) {
	t.Run("default when no overrides", func(t *testing.T) {
		headers := applyUserAgent(map[string]string{"Authorization": "Bearer k"}, nil)
		assert.Equal(t, "Bearer k", headers["Authorization"])
		assert.Equal(t, resolveUserAgent(), headers["User-Agent"])
	})

	t.Run("model User-Agent wins (exact case)", func(t *testing.T) {
		headers := applyUserAgent(map[string]string{}, map[string]string{"User-Agent": "mine/1.0"})
		assert.Equal(t, "mine/1.0", headers["User-Agent"])
	})

	t.Run("model User-Agent wins (lowercase)", func(t *testing.T) {
		headers := applyUserAgent(map[string]string{}, map[string]string{"user-agent": "mine/2.0"})
		for k, v := range headers {
			if k != "user-agent" {
				continue
			}
			assert.Equal(t, "mine/2.0", v)
		}
		assert.NotContains(t, headers, "User-Agent")
	})

	t.Run("other custom headers merge", func(t *testing.T) {
		headers := applyUserAgent(map[string]string{}, map[string]string{"X-Custom": "yes"})
		assert.Equal(t, "yes", headers["X-Custom"])
		assert.Equal(t, resolveUserAgent(), headers["User-Agent"])
	})
}

func TestGenaiHeaders(t *testing.T) {
	t.Run("default UA", func(t *testing.T) {
		h := genaiHeaders(nil)
		assert.Equal(t, resolveUserAgent(), h.Get("User-Agent"))
	})

	t.Run("override case-insensitive", func(t *testing.T) {
		h := genaiHeaders(map[string]string{"user-agent": "gem/1"})
		assert.Equal(t, "gem/1", h.Get("User-Agent"))
	})

	t.Run("extra headers overlay", func(t *testing.T) {
		h := genaiHeaders(map[string]string{"X-Trace": "t1"})
		assert.Equal(t, "t1", h.Get("X-Trace"))
		assert.Equal(t, resolveUserAgent(), h.Get("User-Agent"))
	})
}

func TestOpenAIUserAgentDefault(t *testing.T) {
	t.Cleanup(func() { SetClientUserAgent("") })

	var hdr http.Header
	srv := sseServer(t, []string{
		`{"id":"c1","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"[DONE]",
	}, nil, &hdr, "/chat/completions")
	defer srv.Close()

	p := NewOpenAICompat("sk-test")
	_, _, errEv := drain(p.Stream(context.Background(), oaModel(srv.URL), simpleContext(), nil))
	require.Nil(t, errEv)
	assert.Equal(t, defaultUserAgent(), hdr.Get("User-Agent"))
}

func TestOpenAIUserAgentModelHeaderOverrides(t *testing.T) {
	for _, key := range []string{"User-Agent", "user-agent"} {
		t.Run(key, func(t *testing.T) {
			var hdr http.Header
			srv := sseServer(t, []string{
				`{"id":"c1","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"}}]}`,
				`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				"[DONE]",
			}, nil, &hdr, "/chat/completions")
			defer srv.Close()

			m := oaModel(srv.URL)
			m.Headers = map[string]string{key: "override/1.0", "X-Custom": "c1"}
			p := NewOpenAICompat("sk-test")
			_, _, errEv := drain(p.Stream(context.Background(), m, simpleContext(), nil))
			require.Nil(t, errEv)
			assert.Equal(t, "override/1.0", hdr.Get("User-Agent"))
			assert.Equal(t, "c1", hdr.Get("X-Custom"))
			assert.Equal(t, "Bearer sk-test", hdr.Get("Authorization"))
		})
	}
}

func TestGeminiUserAgentDefaultAndOverride(t *testing.T) {
	t.Cleanup(func() { SetClientUserAgent("") })

	cx := &types.Context{
		Messages: []types.Message{
			&types.UserMessage{Content: types.TextOnly("hi"), Timestamp: 1},
		},
	}

	t.Run("default UA", func(t *testing.T) {
		var hdr http.Header
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hdr = r.Header.Clone()
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\r\n\r\n", `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)
			w.(http.Flusher).Flush()
		}))
		defer srv.Close()

		p := NewGemini("g-key")
		_, _, errEv := drain(p.Stream(context.Background(), gModel(srv.URL), cx, nil))
		require.Nil(t, errEv)
		assert.Equal(t, defaultUserAgent(), hdr.Get("User-Agent"))
	})

	t.Run("model header overrides", func(t *testing.T) {
		var hdr http.Header
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hdr = r.Header.Clone()
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\r\n\r\n", `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)
			w.(http.Flusher).Flush()
		}))
		defer srv.Close()

		m := gModel(srv.URL)
		m.Headers = map[string]string{"user-agent": "gem-override/1.0"}
		p := NewGemini("g-key")
		_, _, errEv := drain(p.Stream(context.Background(), m, cx, nil))
		require.Nil(t, errEv)
		assert.Equal(t, "gem-override/1.0", hdr.Get("User-Agent"))
	})
}
