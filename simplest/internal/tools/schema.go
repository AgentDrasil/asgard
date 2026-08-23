package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// ValidateAgainstSchema validates args against a JSON Schema subset:
// type checks for object/string/number/integer/boolean/array/null, required,
// properties, enum, items, minItems/maxItems, minLength/maxLength,
// minimum/maximum, and additionalProperties:false. It returns the decoded args
// when valid.
func ValidateAgainstSchema(args json.RawMessage, schema json.RawMessage) (map[string]any, error) {
	var sch schemaNode
	if err := json.Unmarshal(schema, &sch); err != nil {
		return nil, fmt.Errorf("invalid tool schema: %w", err)
	}
	var value any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &value); err != nil {
			return nil, fmt.Errorf("invalid JSON arguments: %w", err)
		}
	}
	if err := validateNode(value, &sch, ""); err != nil {
		return nil, err
	}
	obj, ok := value.(map[string]any)
	if !ok {
		if value == nil {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("arguments must be an object")
	}
	return obj, nil
}

type schemaNode struct {
	Type                 any                    `json:"type"`
	Description          string                 `json:"description"`
	Enum                 []any                  `json:"enum"`
	Required             []string               `json:"required"`
	Properties           map[string]*schemaNode `json:"properties"`
	Items                *schemaNode            `json:"items"`
	AdditionalProperties *bool                  `json:"additionalProperties"`
	MinLength            *float64               `json:"minLength"`
	MaxLength            *float64               `json:"maxLength"`
	Minimum              *float64               `json:"minimum"`
	Maximum              *float64               `json:"maximum"`
	MinItems             *float64               `json:"minItems"`
	MaxItems             *float64               `json:"maxItems"`
}

// jsonEqual reports deep JSON-value equality so that enum `1` does not match
// the string "1".
func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb) && ab != nil
}

func validateNode(v any, s *schemaNode, path string) error {
	if s == nil {
		return nil
	}
	if len(s.Enum) > 0 {
		found := false
		for _, e := range s.Enum {
			if jsonEqual(e, v) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s must be one of: %s", label(path), joinEnums(s.Enum))
		}
	}
	types := expandTypes(s.Type)
	if len(types) > 0 && !matchesAnyType(v, types) {
		return fmt.Errorf("%s must be of type %s", label(path), strings.Join(types, ", "))
	}
	switch tv := v.(type) {
	case string:
		if s.MinLength != nil && float64(utf8.RuneCountInString(tv)) < *s.MinLength {
			return fmt.Errorf("%s must be at least %g characters", label(path), *s.MinLength)
		}
		if s.MaxLength != nil && float64(utf8.RuneCountInString(tv)) > *s.MaxLength {
			return fmt.Errorf("%s must be at most %g characters", label(path), *s.MaxLength)
		}
	case float64:
		if s.Minimum != nil && tv < *s.Minimum {
			return fmt.Errorf("%s must be >= %g", label(path), *s.Minimum)
		}
		if s.Maximum != nil && tv > *s.Maximum {
			return fmt.Errorf("%s must be <= %g", label(path), *s.Maximum)
		}
	case []any:
		if s.MinItems != nil && float64(len(tv)) < *s.MinItems {
			return fmt.Errorf("%s must have at least %g items", label(path), *s.MinItems)
		}
		if s.MaxItems != nil && float64(len(tv)) > *s.MaxItems {
			return fmt.Errorf("%s must have at most %g items", label(path), *s.MaxItems)
		}
		for i, item := range tv {
			if err := validateNode(item, s.Items, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, req := range s.Required {
			if _, ok := tv[req]; !ok {
				return fmt.Errorf("missing required argument: %s", joinPath(path, req))
			}
		}
		for key, val := range tv {
			prop, known := s.Properties[key]
			if !known {
				if s.AdditionalProperties != nil && !*s.AdditionalProperties {
					return fmt.Errorf("unknown argument: %s", joinPath(path, key))
				}
				continue
			}
			if err := validateNode(val, prop, joinPath(path, key)); err != nil {
				return err
			}
		}
	}
	return nil
}

func expandTypes(t any) []string {
	switch v := t.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func matchesAnyType(v any, types []string) bool {
	for _, t := range types {
		if matchesType(v, t) {
			return true
		}
	}
	return false
}

func matchesType(v any, t string) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "number":
		f, ok := v.(float64)
		return ok && !math.IsNaN(f) && !math.IsInf(f, 0)
	case "integer":
		f, ok := v.(float64)
		return ok && f == math.Trunc(f)
	default:
		return true // unknown types are not enforced
	}
}

func label(path string) string {
	if path == "" {
		return "arguments"
	}
	return path
}

func joinPath(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}

func joinEnums(enums []any) string {
	parts := make([]string, len(enums))
	for i, e := range enums {
		b, _ := json.Marshal(e)
		parts[i] = string(b)
	}
	return strings.Join(parts, ", ")
}
