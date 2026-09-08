package agentwrapper

import (
	"github.com/AgentDrasil/asgard/agentwrapper/agy"
	"github.com/AgentDrasil/asgard/agentwrapper/opencode"
	"github.com/AgentDrasil/asgard/agentwrapper/simplest"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
	simplestcfg "github.com/AgentDrasil/asgard/simplest"
)

// MatchesModel reports whether the requested model matches a known model
// name, accounting for opencode/agy/simplest variant suffixes (e.g. "provider/model/low"
// or "gemini-3.7-flash-low" matches the known model "provider/model" or "gemini-3.7-flash").
func MatchesModel(cli, model, known string) bool {
	for _, name := range ModelCandidates(cli, model) {
		if name == known {
			return true
		}
	}
	return false
}

// HasModelVariant reports whether the model string carries a recognized
// variant (thinking level) suffix for the given CLI.
func HasModelVariant(cli, model string) bool {
	return len(ModelCandidates(cli, model)) > 1
}

// ModelCandidates returns the model names that the requested model string
// should be matched against, most specific first. For opencode, agy, and simplest, a model with
// a variant suffix also matches its base model name.
func ModelCandidates(cli, model string) []string {
	names := []string{model}
	switch cli {
	case "opencode":
		if base, variant := opencode.SplitModelVariant(model); variant != "" && base != model {
			names = append(names, base)
		}
	case "agy":
		if base, variant := agy.SplitModelVariant(model); variant != "" && base != model {
			names = append(names, base)
		}
	case "simplest":
		if base, variant := simplest.SplitModelVariant(model); variant != "" && base != model {
			names = append(names, base)
		}
	}
	return names
}

// LookupContextWindow delegates directly to types.LookupContextWindow.
func LookupContextWindow(model string) (limit int, known bool) {
	return types.LookupContextWindow(model)
}

// IsKnownModel reports whether the model is cataloged for the given CLI,
// accounting for variant suffixes (e.g. "zai-coding-plan/glm-5.3/low" or
// "gemini-3.7-flash-low"). For simplest, models defined in the user's
// simplest config (provider/id or bare id) are also considered known,
// because the simplest runtime uses model.ContextWindow from that config
// instead of the hardcoded table.
func IsKnownModel(cli, model string) bool {
	for _, candidate := range ModelCandidates(cli, model) {
		if _, known := types.LookupContextWindow(candidate); known {
			return true
		}
	}
	if cli == "simplest" && isSimplestConfiguredModel(cli, model) {
		return true
	}
	return false
}

// GetModelContextWindow resolves the context window for a model given the CLI type.
// It iterates through ModelCandidates(cli, model) and returns the first known limit.
// For simplest, the user's simplest config is consulted (same source as
// `aw models`) so custom models pick up their configured contextWindow
// without hardcoding. If none are known, it returns types.DefaultContextWindow (1M).
func GetModelContextWindow(cli, model string) int {
	for _, candidate := range ModelCandidates(cli, model) {
		if limit, known := types.LookupContextWindow(candidate); known {
			return limit
		}
	}
	if cli == "simplest" {
		if limit, ok := simplestContextWindow(cli, model); ok {
			return limit
		}
	}
	return types.DefaultContextWindow
}

// isSimplestConfiguredModel reports whether the model resolves against the
// user's simplest config (which carries the authoritative ContextWindow for
// simplest runs). Each variant-stripped candidate is tried so that e.g.
// "deepseek/deepseek-v4-pro/high" matches config id "deepseek-v4-pro".
func isSimplestConfiguredModel(cli, model string) bool {
	for _, candidate := range ModelCandidates(cli, model) {
		if _, _, err := simplestcfg.ResolveModelAndProvider(candidate); err == nil {
			return true
		}
	}
	return false
}

// simplestContextWindow returns the configured contextWindow from the user's
// simplest config (same source as `aw models` / Models()). It tries each
// variant-stripped candidate and reports ok=false when the model is not
// configured or has no positive contextWindow.
func simplestContextWindow(cli, model string) (int, bool) {
	for _, candidate := range ModelCandidates(cli, model) {
		m, _, err := simplestcfg.ResolveModelAndProvider(candidate)
		if err != nil || m == nil {
			continue
		}
		if m.ContextWindow > 0 {
			return int(m.ContextWindow), true
		}
	}
	return 0, false
}
