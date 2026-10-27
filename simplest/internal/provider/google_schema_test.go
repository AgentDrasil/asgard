package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// --- rewriteConstAsEnum ---

func TestRewriteConstAsEnum(t *testing.T) {
	t.Parallel()

	t.Run("top-level string const", func(t *testing.T) {
		t.Parallel()
		in := mustJSONMap(t, `{"type":"object","const":"abc"}`)
		out := rewriteConstAsEnum(in).(map[string]any)
		assert.Equal(t, []any{"abc"}, out["enum"])
		assert.NotContains(t, out, "const")
	})

	t.Run("top-level numeric const", func(t *testing.T) {
		t.Parallel()
		in := mustJSONMap(t, `{"type":"integer","const":42}`)
		out := rewriteConstAsEnum(in).(map[string]any)
		assert.Equal(t, []any{float64(42)}, out["enum"])
		assert.NotContains(t, out, "const")
	})

	t.Run("nested properties", func(t *testing.T) {
		t.Parallel()
		in := mustJSONMap(t, `{"type":"object","properties":{"mode":{"type":"string","const":"fast"}}}`)
		out := rewriteConstAsEnum(in).(map[string]any)
		props := out["properties"].(map[string]any)
		mode := props["mode"].(map[string]any)
		assert.Equal(t, []any{"fast"}, mode["enum"])
		assert.NotContains(t, mode, "const")
	})

	t.Run("array items", func(t *testing.T) {
		t.Parallel()
		in := mustJSONMap(t, `{"type":"array","items":{"type":"number","const":7}}`)
		out := rewriteConstAsEnum(in).(map[string]any)
		items := out["items"].(map[string]any)
		assert.Equal(t, []any{float64(7)}, items["enum"])
		assert.NotContains(t, items, "const")
	})

	t.Run("defs", func(t *testing.T) {
		t.Parallel()
		in := mustJSONMap(t, `{"$defs":{"level":{"type":"string","const":"high"}},"type":"object"}`)
		out := rewriteConstAsEnum(in).(map[string]any)
		def := out["$defs"].(map[string]any)["level"].(map[string]any)
		assert.Equal(t, []any{"high"}, def["enum"])
		assert.NotContains(t, def, "const")
	})

	t.Run("anyOf", func(t *testing.T) {
		t.Parallel()
		in := mustJSONMap(t, `{"anyOf":[{"type":"string","const":"a"},{"type":"string","const":"b"}]}`)
		out := rewriteConstAsEnum(in).(map[string]any)
		anyOf := out["anyOf"].([]any)
		assert.Equal(t, []any{"a"}, anyOf[0].(map[string]any)["enum"])
		assert.Equal(t, []any{"b"}, anyOf[1].(map[string]any)["enum"])
	})

	t.Run("existing enum preserved and const untouched", func(t *testing.T) {
		t.Parallel()
		in := mustJSONMap(t, `{"const":"a","enum":["a","b"]}`)
		out := rewriteConstAsEnum(in).(map[string]any)
		require.Contains(t, out, "const")
		assert.Equal(t, "a", out["const"])
		assert.Equal(t, []any{"a", "b"}, out["enum"])
	})

	t.Run("boolean and null const left alone", func(t *testing.T) {
		t.Parallel()
		in := mustJSONMap(t, `{"oneOf":[{"const":true},{"const":null}]}`)
		out := rewriteConstAsEnum(in).(map[string]any)
		oneOf := out["oneOf"].([]any)
		assert.Equal(t, true, oneOf[0].(map[string]any)["const"])
		assert.Nil(t, oneOf[1].(map[string]any)["const"])
		assert.NotContains(t, oneOf[0].(map[string]any), "enum")
		assert.NotContains(t, oneOf[1].(map[string]any), "enum")
	})

	t.Run("property named const is not misinterpreted", func(t *testing.T) {
		t.Parallel()
		in := mustJSONMap(t, `{"type":"object","properties":{"const":{"type":"string"}}}`)
		out := rewriteConstAsEnum(in).(map[string]any)
		props := out["properties"].(map[string]any)
		require.Contains(t, props, "const")
		sub := props["const"].(map[string]any)
		assert.Equal(t, "string", sub["type"])
		assert.NotContains(t, props, "enum")
		assert.NotContains(t, sub, "enum")
	})

	t.Run("literal data preserved", func(t *testing.T) {
		t.Parallel()
		// Literal values under default/examples/enum elements/x-* must not be
		// rewritten even when they contain their own const keys.
		literal := map[string]any{
			"const":      "literal-data",
			"properties": map[string]any{"nested": map[string]any{"const": float64(1)}},
		}
		in := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{
					"type":     "object",
					"default":  literal,
					"examples": []any{literal},
					"enum":     []any{literal},
					"x-custom": literal,
				},
			},
		}
		out := rewriteConstAsEnum(in).(map[string]any)
		value := out["properties"].(map[string]any)["value"].(map[string]any)
		assert.Equal(t, literal, value["default"], "default is literal data")
		assert.Equal(t, []any{literal}, value["examples"], "examples is literal data")
		assert.Equal(t, []any{literal}, value["enum"], "enum elements are literal data")
		assert.Equal(t, literal, value["x-custom"], "x-* is literal data")
	})

	t.Run("name maps recurse into each named schema", func(t *testing.T) {
		t.Parallel()
		named := `{"const":{"const":"y"},"properties":{"const":"z"}}`
		for _, key := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas", "dependencies"} {
			t.Run(key, func(t *testing.T) {
				t.Parallel()
				in := mustJSONMap(t, `{"type":"object","`+key+`":`+named+`}`)
				out := rewriteConstAsEnum(in).(map[string]any)
				m := out[key].(map[string]any)
				// Key "const" is a schema *name* here: its subschema is rewritten,
				// the entry itself is kept.
				require.Contains(t, m, "const")
				assert.Equal(t, map[string]any{"enum": []any{"y"}}, m["const"])
				assert.Equal(t, map[string]any{"enum": []any{"z"}}, m["properties"])
			})
		}
	})

	t.Run("subschema keywords recurse", func(t *testing.T) {
		t.Parallel()
		in := mustJSONMap(t, `{
			"type": "object",
			"additionalProperties": {"const": "x"},
			"unevaluatedProperties": false,
			"propertyNames": {"const": "name"},
			"allOf": [{"if": {"const": "x"}, "then": {"const": "y"}, "else": {"not": {"const": "z"}}}],
			"properties": {
				"list": {
					"type": "array",
					"items": [{"const": 1}],
					"prefixItems": [{"const": 2}],
					"additionalItems": {"const": 3},
					"contains": {"const": 4},
					"unevaluatedItems": {"const": 5}
				}
			},
			"contentSchema": {"const": "text"},
			"dependencies": {"list": ["other"]}
		}`)
		out := rewriteConstAsEnum(in).(map[string]any)
		assert.Equal(t, map[string]any{"enum": []any{"x"}}, out["additionalProperties"])
		assert.Equal(t, false, out["unevaluatedProperties"], "boolean schema is not touched")
		assert.Equal(t, map[string]any{"enum": []any{"name"}}, out["propertyNames"])
		allOf := out["allOf"].([]any)
		cond := allOf[0].(map[string]any)
		assert.Equal(t, []any{"x"}, cond["if"].(map[string]any)["enum"])
		assert.Equal(t, []any{"y"}, cond["then"].(map[string]any)["enum"])
		assert.Equal(t, []any{"z"}, cond["else"].(map[string]any)["not"].(map[string]any)["enum"])
		list := out["properties"].(map[string]any)["list"].(map[string]any)
		assert.Equal(t, []any{map[string]any{"enum": []any{float64(1)}}}, list["items"])
		assert.Equal(t, []any{map[string]any{"enum": []any{float64(2)}}}, list["prefixItems"])
		assert.Equal(t, map[string]any{"enum": []any{float64(3)}}, list["additionalItems"])
		assert.Equal(t, map[string]any{"enum": []any{float64(4)}}, list["contains"])
		assert.Equal(t, map[string]any{"enum": []any{float64(5)}}, list["unevaluatedItems"])
		assert.Equal(t, map[string]any{"enum": []any{"text"}}, out["contentSchema"])
		// Non-schema array value under dependencies is literal data.
		assert.Equal(t, map[string]any{"list": []any{"other"}}, out["dependencies"])
	})

	t.Run("input map not mutated", func(t *testing.T) {
		t.Parallel()
		in := mustJSONMap(t, `{"type":"object","properties":{"mode":{"type":"string","const":"fast"}},"items":[{"const":1}]}`)
		before := mustJSONMap(t, `{"type":"object","properties":{"mode":{"type":"string","const":"fast"}},"items":[{"const":1}]}`)
		_ = rewriteConstAsEnum(in)
		assert.Equal(t, before, in)
	})

	t.Run("scalars pass through", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "x", rewriteConstAsEnum("x"))
		assert.Nil(t, rewriteConstAsEnum(nil))
	})
}

func mustJSONMap(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &m))
	return m
}

// --- integration: const rewritten in the outgoing Gemini request ---

func TestGeminiRewritesConstAsEnumInRequest(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	srv := sseServer(t, []string{
		`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`,
	}, &captured, nil, "")
	t.Cleanup(srv.Close)

	cx := simpleContext()
	cx.Tools = []types.ToolDef{
		{
			Name:        "pick",
			Description: "pick something",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"mode":   {"type": "string", "const": "fast"},
					"answer": {"type": "integer", "const": 42},
					"hint":   {"type": "string", "default": {"const": "keep-me"}}
				}
			}`),
		},
	}
	p := NewGemini("g-key")
	m := gModel(srv.URL)

	_, done, errEv := drain(p.Stream(context.Background(), m, cx, nil))
	require.Nil(t, errEv)
	require.NotNil(t, done)

	tools, ok := captured["tools"].([]any)
	require.True(t, ok, "tools missing in request: %v", captured)
	decls, ok := tools[0].(map[string]any)["functionDeclarations"].([]any)
	require.True(t, ok, "functionDeclarations missing: %v", tools)
	require.Len(t, decls, 1)
	params, ok := decls[0].(map[string]any)["parametersJsonSchema"].(map[string]any)
	require.True(t, ok, "parametersJsonSchema missing: %v", decls[0])
	props, ok := params["properties"].(map[string]any)
	require.True(t, ok, "properties missing: %v", params)

	mode := props["mode"].(map[string]any)
	assert.Equal(t, []any{"fast"}, mode["enum"], "string const must become enum")
	assert.NotContains(t, mode, "const")

	answer := props["answer"].(map[string]any)
	assert.Equal(t, []any{float64(42)}, answer["enum"], "numeric const must become enum")
	assert.NotContains(t, answer, "const")

	hint := props["hint"].(map[string]any)
	assert.Equal(t, map[string]any{"const": "keep-me"}, hint["default"], "literal default must be preserved")
}
