package agentwrapper

import (
	"reflect"
	"testing"
)

func TestMatchesModel(t *testing.T) {
	tests := []struct {
		cli   string
		model string
		known string
		want  bool
	}{
		{cli: "opencode", model: "zai-coding-plan/glm-5.3", known: "zai-coding-plan/glm-5.3", want: true},
		{cli: "opencode", model: "zai-coding-plan/glm-5.3/low", known: "zai-coding-plan/glm-5.3", want: true},
		{cli: "opencode", model: "zai-coding-plan/glm-5.3", known: "zai-coding-plan/glm-5.3/low", want: false},
		{cli: "opencode", model: "openrouter/deepseek/deepseek-chat", known: "openrouter/deepseek", want: false},
		{cli: "agy", model: "some-model/low", known: "some-model", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.cli+"/"+tt.model+" vs "+tt.known, func(t *testing.T) {
			if got := MatchesModel(tt.cli, tt.model, tt.known); got != tt.want {
				t.Errorf("MatchesModel(%q, %q, %q) = %v, want %v", tt.cli, tt.model, tt.known, got, tt.want)
			}
		})
	}
}

func TestModelCandidates(t *testing.T) {
	if got := ModelCandidates("opencode", "zai-coding-plan/glm-5.3/low"); !reflect.DeepEqual(got, []string{"zai-coding-plan/glm-5.3/low", "zai-coding-plan/glm-5.3"}) {
		t.Errorf("unexpected candidates: %v", got)
	}
	if got := ModelCandidates("opencode", "zai-coding-plan/glm-5.3"); !reflect.DeepEqual(got, []string{"zai-coding-plan/glm-5.3"}) {
		t.Errorf("unexpected candidates: %v", got)
	}
	if got := ModelCandidates("agy", "some-model/low"); !reflect.DeepEqual(got, []string{"some-model/low"}) {
		t.Errorf("unexpected candidates: %v", got)
	}
}
