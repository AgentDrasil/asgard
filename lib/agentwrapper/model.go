package agentwrapper

import (
	"github.com/AgentDrasil/asgard/lib/agentwrapper/opencode"
)

// MatchesModel reports whether the requested model matches a known model
// name, accounting for opencode variant suffixes (e.g. "provider/model/low"
// matches the known model "provider/model").
func MatchesModel(cli, model, known string) bool {
	for _, name := range ModelCandidates(cli, model) {
		if name == known {
			return true
		}
	}
	return false
}

// ModelCandidates returns the model names that the requested model string
// should be matched against, most specific first. For opencode, a model with
// a variant suffix also matches its base model name.
func ModelCandidates(cli, model string) []string {
	names := []string{model}
	if cli == "opencode" {
		if base, variant := opencode.SplitModelVariant(model); variant != "" && base != model {
			names = append(names, base)
		}
	}
	return names
}
