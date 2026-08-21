package workflow

import (
	"fmt"
	"reflect"
)

// ToAnySlice converts a slice of any type T to []any. If slice is empty or nil, returns nil.
func ToAnySlice[T any](s []T) []any {
	if len(s) == 0 {
		return nil
	}
	res := make([]any, len(s))
	for i, v := range s {
		res[i] = v
	}
	return res
}

// SanitizeMetadataValue recursively converts values (especially typed slices like []string)
// into standard JSON-serializable types:
// nil, bool, int, float, string, []any, map[string]any.
func SanitizeMetadataValue(v any) any {
	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, string:
		return val
	case []string:
		res := make([]any, len(val))
		for i, s := range val {
			res[i] = s
		}
		return res
	case []any:
		res := make([]any, len(val))
		for i, item := range val {
			res[i] = SanitizeMetadataValue(item)
		}
		return res
	case map[string]any:
		res := make(map[string]any, len(val))
		for k, item := range val {
			res[k] = SanitizeMetadataValue(item)
		}
		return res
	case map[string]string:
		res := make(map[string]any, len(val))
		for k, item := range val {
			res[k] = item
		}
		return res
	default:
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.Slice, reflect.Array:
			res := make([]any, rv.Len())
			for i := 0; i < rv.Len(); i++ {
				res[i] = SanitizeMetadataValue(rv.Index(i).Interface())
			}
			return res
		case reflect.Map:
			res := make(map[string]any, rv.Len())
			for _, key := range rv.MapKeys() {
				res[fmt.Sprint(key.Interface())] = SanitizeMetadataValue(rv.MapIndex(key).Interface())
			}
			return res
		default:
			return val
		}
	}
}

// SanitizeMetadata recursively ensures that all map entries adhere to JSON-serializable types.
func SanitizeMetadata(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}

	res := make(map[string]any, len(m))
	for k, v := range m {
		res[k] = SanitizeMetadataValue(v)
	}
	return res
}
