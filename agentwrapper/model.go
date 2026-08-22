package agentwrapper

import (
	"github.com/AgentDrasil/asgard/agentwrapper/agy"
	"github.com/AgentDrasil/asgard/agentwrapper/opencode"
)

// MatchesModel reports whether the requested model matches a known model
// name, accounting for opencode/agy variant suffixes (e.g. "provider/model/low"
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
// should be matched against, most specific first. For opencode and agy, a model with
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
	}
	return names
}
