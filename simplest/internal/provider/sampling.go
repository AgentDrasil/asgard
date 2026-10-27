package provider

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"google.golang.org/genai"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// effectiveSamplingLevel resolves the requested thinking level like upstream
// resolveSamplingParams: normalize case, use the level when supported, otherwise
// clamp UPWARD then DOWNWARD through OrderedThinkingLevels, finally off.
func effectiveSamplingLevel(model *types.Model, opts *types.StreamOptions) types.ThinkingLevel {
	level := types.ThinkingOff
	if opts != nil && opts.ThinkingLevel != "" {
		level = opts.ThinkingLevel
	}
	idx := canonicalThinkingIndex(level)
	if idx >= 0 {
		level = types.OrderedThinkingLevels[idx]
	}
	if model == nil {
		return types.ThinkingOff
	}
	if model.SupportsThinkingLevel(level) {
		return level
	}
	start := idx
	if start < 0 {
		start = 0 // unknown levels start from off and search upward
	}
	for i := start; i < len(types.OrderedThinkingLevels); i++ {
		if cand := types.OrderedThinkingLevels[i]; model.SupportsThinkingLevel(cand) {
			return cand
		}
	}
	for i := start - 1; i >= 0; i-- {
		if cand := types.OrderedThinkingLevels[i]; model.SupportsThinkingLevel(cand) {
			return cand
		}
	}
	return types.ThinkingOff
}

func canonicalThinkingIndex(level types.ThinkingLevel) int {
	for i, l := range types.OrderedThinkingLevels {
		if strings.EqualFold(string(l), string(level)) {
			return i
		}
	}
	return -1
}

// mergeSamplingParams applies model defaults, then the model's per-level
// override for the effective thinking level, then per-request overrides.
func mergeSamplingParams(model *types.Model, opts *types.StreamOptions) map[string]any {
	return mergeSamplingParamsWith(model, opts, nil)
}

// mergeGeminiSamplingParams is mergeSamplingParams with Gemini alias keys
// canonicalized per layer so mixed spellings resolve deterministically.
func mergeGeminiSamplingParams(model *types.Model, opts *types.StreamOptions) map[string]any {
	return mergeSamplingParamsWith(model, opts, canonicalizeGeminiSamplingParams)
}

func mergeSamplingParamsWith(model *types.Model, opts *types.StreamOptions, canonicalize func(types.ModelSamplingParams) map[string]any) map[string]any {
	var out map[string]any
	add := func(params types.ModelSamplingParams) {
		if canonicalize != nil {
			for k, v := range canonicalize(params) {
				if out == nil {
					out = make(map[string]any, len(params))
				}
				out[k] = v
			}
			return
		}
		for k, v := range params {
			if out == nil {
				out = make(map[string]any, len(params))
			}
			out[k] = v
		}
	}
	if model != nil {
		add(model.SamplingParams)
		if model.SamplingParamsByThinkingLevel != nil {
			add(model.SamplingParamsByThinkingLevel[effectiveSamplingLevel(model, opts)])
		}
	}
	if opts != nil {
		add(opts.SamplingParams)
	}
	return out
}

// canonicalizeGeminiSamplingParams maps alias spellings (top_p, presence_penalty,
// …) to their genai config names within one layer. When both spellings are
// present, the canonical/camelCase key wins deterministically.
func canonicalizeGeminiSamplingParams(params types.ModelSamplingParams) map[string]any {
	out := make(map[string]any, len(params))
	for k, v := range params {
		canonical := canonicalGeminiSamplingKey(k)
		if canonical != k {
			if _, present := params[canonical]; present {
				continue
			}
		}
		out[canonical] = v
	}
	return out
}

func canonicalGeminiSamplingKey(key string) string {
	switch key {
	case "top_p":
		return "topP"
	case "top_k":
		return "topK"
	case "presence_penalty":
		return "presencePenalty"
	case "frequency_penalty":
		return "frequencyPenalty"
	case "candidate_count":
		return "candidateCount"
	case "max_output_tokens":
		return "maxOutputTokens"
	default:
		return key
	}
}

// mergeSamplingJSON overlays sampling params as raw JSON values on an already
// marshaled request body. Arbitrary keys are allowed and known keys replace the
// struct-encoded values.
func mergeSamplingJSON(body []byte, params map[string]any) ([]byte, error) {
	if len(params) == 0 {
		return body, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	for k, v := range params {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		obj[k] = raw
	}
	return json.Marshal(obj)
}

// applyGeminiSamplingParams maps known sampling keys onto the genai config.
// Unknown keys are ignored (the genai SDK has no generic escape hatch).
func applyGeminiSamplingParams(config *genai.GenerateContentConfig, params map[string]any) {
	for k, v := range params {
		switch k {
		case "temperature":
			if f, ok := samplingFloat32(v); ok {
				config.Temperature = &f
			}
		case "topP", "top_p":
			if f, ok := samplingFloat32(v); ok {
				config.TopP = &f
			}
		case "topK", "top_k":
			if f, ok := samplingFloat32(v); ok {
				config.TopK = &f
			}
		case "seed":
			if n, ok := samplingInt32(v); ok {
				config.Seed = &n
			}
		case "presencePenalty", "presence_penalty":
			if f, ok := samplingFloat32(v); ok {
				config.PresencePenalty = &f
			}
		case "frequencyPenalty", "frequency_penalty":
			if f, ok := samplingFloat32(v); ok {
				config.FrequencyPenalty = &f
			}
		case "candidateCount", "candidate_count":
			if n, ok := samplingInt32(v); ok {
				config.CandidateCount = n
			}
		case "maxOutputTokens", "max_output_tokens":
			if n, ok := samplingInt32(v); ok {
				config.MaxOutputTokens = n
			}
		}
	}
}

func samplingFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func samplingFloat32(v any) (float32, bool) {
	f, ok := samplingFloat64(v)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > math.MaxFloat32 {
		return 0, false
	}
	out := float32(f)
	if f != 0 && out == 0 {
		return 0, false // underflow
	}
	return out, true
}

func samplingInt32(v any) (int32, bool) {
	f, ok := samplingFloat64(v)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) {
		return 0, false
	}
	if f < math.MinInt32 || f > math.MaxInt32 {
		return 0, false
	}
	return int32(f), true
}
