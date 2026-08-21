package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
)

// newTestClient returns a genaiClientWrapper pointed at a fake Gemini API
// server. The recorder function receives the decoded request body for
// assertions.
func newTestClient(t *testing.T, recorder func(body map[string]any)) Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		if recorder != nil {
			recorder(body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{map[string]any{"text": "hello world"}},
					},
				},
			},
		})
	}))
	t.Cleanup(srv.Close)

	client, err := genai.NewClient(t.Context(), &genai.ClientConfig{
		APIKey:      "test-key",
		Backend:     genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{BaseURL: srv.URL},
	})
	require.NoError(t, err)
	return &genaiClientWrapper{client: client}
}

func TestGenerateTextPlain(t *testing.T) {
	var got map[string]any
	client := newTestClient(t, func(body map[string]any) { got = body })

	out, err := client.GenerateText(t.Context(), GenerateOptions{
		Model:  "gemini-test",
		Prompt: "say hi",
	})
	require.NoError(t, err)
	assert.Equal(t, "hello world", out)

	assert.Equal(t, []any{map[string]any{"parts": []any{map[string]any{"text": "say hi"}}, "role": "user"}}, got["contents"])
	assert.Empty(t, got["systemInstruction"])
	assert.Empty(t, got["generationConfig"])
}

func TestGenerateTextWithSystemPromptAndTemperature(t *testing.T) {
	var got map[string]any
	client := newTestClient(t, func(body map[string]any) { got = body })

	temp := float32(0.7)
	out, err := client.GenerateText(t.Context(), GenerateOptions{
		Model:        "gemini-test",
		Prompt:       "say hi",
		SystemPrompt: "be terse",
		Temperature:  &temp,
	})
	require.NoError(t, err)
	assert.Equal(t, "hello world", out)

	sys, ok := got["systemInstruction"].(map[string]any)
	require.True(t, ok)
	parts, ok := sys["parts"].([]any)
	require.True(t, ok)
	require.Len(t, parts, 1)
	assert.Equal(t, map[string]any{"text": "be terse"}, parts[0])

	cfg, ok := got["generationConfig"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, 0.7, cfg["temperature"])
}
