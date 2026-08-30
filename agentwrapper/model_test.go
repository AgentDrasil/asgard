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
		{cli: "opencode", model: "zai-coding-plan/glm-5.3/max", known: "zai-coding-plan/glm-5.3", want: true},
		{cli: "opencode", model: "zai-coding-plan/glm-5.3", known: "zai-coding-plan/glm-5.3/low", want: false},
		{cli: "opencode", model: "openrouter/deepseek/deepseek-chat", known: "openrouter/deepseek", want: false},
		{cli: "agy", model: "gemini-3.7-flash-low", known: "gemini-3.7-flash", want: true},
		{cli: "agy", model: "gemini-3.7-flash/low", known: "gemini-3.7-flash", want: true},
		{cli: "agy", model: "gemini-3.7-flash", known: "gemini-3.7-flash", want: true},
		{cli: "simplest", model: "zai-coding-plan/glm-5.3/low", known: "zai-coding-plan/glm-5.3", want: true},
		{cli: "simplest", model: "zai-coding-plan/glm-5.3/max", known: "zai-coding-plan/glm-5.3", want: true},
		{cli: "simplest", model: "gemini/gemini-3.5-flash-lite/low", known: "gemini/gemini-3.5-flash-lite", want: true},
		{cli: "simplest", model: "gemini/gemini-3.7-flash/high", known: "gemini/gemini-3.7-flash", want: true},
		{cli: "simplest", model: "gemini-3.7-flash/high", known: "gemini-3.7-flash", want: true},
		{cli: "simplest", model: "gemini-3.7-flash", known: "gemini-3.7-flash", want: true},
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
	if got := ModelCandidates("agy", "gemini-3.7-flash-low"); !reflect.DeepEqual(got, []string{"gemini-3.7-flash-low", "gemini-3.7-flash"}) {
		t.Errorf("unexpected candidates: %v", got)
	}
	if got := ModelCandidates("agy", "gemini-3.7-flash/low"); !reflect.DeepEqual(got, []string{"gemini-3.7-flash/low", "gemini-3.7-flash"}) {
		t.Errorf("unexpected candidates: %v", got)
	}
	if got := ModelCandidates("simplest", "gemini/gemini-3.5-flash-lite/low"); !reflect.DeepEqual(got, []string{"gemini/gemini-3.5-flash-lite/low", "gemini/gemini-3.5-flash-lite"}) {
		t.Errorf("unexpected candidates: %v", got)
	}
	if got := ModelCandidates("simplest", "zai-coding-plan/glm-5.3/max"); !reflect.DeepEqual(got, []string{"zai-coding-plan/glm-5.3/max", "zai-coding-plan/glm-5.3"}) {
		t.Errorf("unexpected candidates: %v", got)
	}
	if got := ModelCandidates("simplest", "gemini-3.7-flash/high"); !reflect.DeepEqual(got, []string{"gemini-3.7-flash/high", "gemini-3.7-flash"}) {
		t.Errorf("unexpected candidates: %v", got)
	}
}

func TestLookupContextWindow(t *testing.T) {
	limit, known := LookupContextWindow("claude-opus-4-6-thinking")
	if !known || limit != 256000 {
		t.Errorf("LookupContextWindow(claude-opus-4-6-thinking) = (%d, %v), want (256000, true)", limit, known)
	}

	limit, known = LookupContextWindow("unknown-xyz")
	if known || limit != 1048576 {
		t.Errorf("LookupContextWindow(unknown-xyz) = (%d, %v), want (1048576, false)", limit, known)
	}
}

func TestGetModelContextWindow(t *testing.T) {
	tests := []struct {
		cli   string
		model string
		want  int
	}{
		{"opencode", "zai-coding-plan/glm-5.3/low", 1048576},
		{"agy", "claude-opus-4-6-thinking", 256000},
		{"agy", "gemini-3.7-flash-low", 1048576},
		{"simplest", "custom/unknown-model", 1048576},
	}

	for _, tt := range tests {
		got := GetModelContextWindow(tt.cli, tt.model)
		if got != tt.want {
			t.Errorf("GetModelContextWindow(%q, %q) = %d, want %d", tt.cli, tt.model, got, tt.want)
		}
	}
}
