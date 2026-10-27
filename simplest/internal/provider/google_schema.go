package provider

import (
	"encoding/json"
)

// Port of pi 7314819f0 / a7b94a03f: Gemini ignores const; send a single-value enum,
// preserving literal data by rewriting only schema-bearing positions.

// schemaNameMaps are JSON Schema keywords whose values map names to subschemas,
// so their keys are schema names, not keywords.
var schemaNameMaps = map[string]bool{
	"properties":        true,
	"patternProperties": true,
	"$defs":             true,
	"definitions":       true,
	"dependentSchemas":  true,
	"dependencies":      true,
}

// schemaSubschemas are keywords whose values are subschemas (or arrays of
// subschemas), not literal data.
var schemaSubschemas = map[string]bool{
	"additionalProperties":  true,
	"unevaluatedProperties": true,
	"propertyNames":         true,
	"items":                 true,
	"prefixItems":           true,
	"additionalItems":       true,
	"unevaluatedItems":      true,
	"contains":              true,
	"allOf":                 true,
	"anyOf":                 true,
	"oneOf":                 true,
	"not":                   true,
	"if":                    true,
	"then":                  true,
	"else":                  true,
	"contentSchema":         true,
}

// rewriteConstAsEnum returns a copy of schema in which every string or number
// `const` at a schema-bearing position is replaced by an equivalent
// single-value `enum`. Gemini honors enum but silently ignores const in
// parametersJsonSchema. Only schema positions are traversed (via
// schemaNameMaps and schemaSubschemas); literal data such as `default`,
// `examples`, `enum` elements, and `x-*` annotations is preserved verbatim
// (shared, not copied). Schemas that already declare an enum, and
// boolean/null consts, are left untouched. The input is never mutated.
func rewriteConstAsEnum(schema any) any {
	return rewriteConstValue(schema, false)
}

func rewriteConstValue(schema any, isNameMap bool) any {
	if isNameMap {
		// Each named value is itself a schema; the names are not keywords.
		if named, ok := schema.(map[string]any); ok {
			out := make(map[string]any, len(named))
			for name, sub := range named {
				out[name] = rewriteConstValue(sub, false)
			}
			return out
		}
		return schema
	}
	switch v := schema.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			switch {
			case schemaNameMaps[k]:
				out[k] = rewriteConstValue(val, true)
			case schemaSubschemas[k]:
				out[k] = rewriteConstValue(val, false)
			default:
				out[k] = val
			}
		}
		if cv, ok := out["const"]; ok {
			if _, hasEnum := out["enum"]; !hasEnum {
				switch cv.(type) {
				case string, float64, json.Number:
					delete(out, "const")
					out["enum"] = []any{cv}
				}
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, el := range v {
			out[i] = rewriteConstValue(el, false)
		}
		return out
	default:
		return schema
	}
}
