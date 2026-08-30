package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRemapSandboxPath(t *testing.T) {
	assert.Equal(t, "main.go", RemapSandboxPath("main.go"))
	assert.Equal(t, "/tmp/test.md", RemapSandboxPath("/tmp/test.md"))
	assert.Equal(t, "/tmp/dir/file.go", RemapSandboxPath("/tmp/dir/file.go"))
	assert.Equal(t, "src/app.ts", RemapSandboxPath("src/app.ts"))
	assert.Equal(t, "", RemapSandboxPath(""))
	assert.Equal(t, "/tmp/space.txt", RemapSandboxPath("  /tmp/space.txt  "))
}

func TestSplitModelVariant(t *testing.T) {
	tests := []struct {
		input       string
		wantBase    string
		wantVariant string
	}{
		{"zai-coding-plan/glm-5.3/low", "zai-coding-plan/glm-5.3", "low"},
		{"opencode/claude-sonnet-4-6/high", "opencode/claude-sonnet-4-6", "high"},
		{"zai-coding-plan/glm-5.3", "zai-coding-plan/glm-5.3", ""},
		{"deepseek-chat", "deepseek-chat", ""},
		{"openrouter/deepseek/deepseek-chat", "openrouter/deepseek/deepseek-chat", ""},
		{"zai-coding-plan/glm-5.3/garbage", "zai-coding-plan/glm-5.3/garbage", ""},
		{"", "", ""},
		{"claude-3-5-sonnet/MINIMAL", "claude-3-5-sonnet", "minimal"},
		{"glm-5.3/max", "glm-5.3", "max"},
		{"deepseek-v4-flash/xhigh", "deepseek-v4-flash", "xhigh"},
	}

	for _, tt := range tests {
		base, variant := SplitModelVariant(tt.input)
		assert.Equal(t, tt.wantBase, base, "input: %s", tt.input)
		assert.Equal(t, tt.wantVariant, variant, "input: %s", tt.input)
	}
}

func TestLookupContextWindow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		model     string
		wantLimit int
		wantKnown bool
	}{
		// AGY Models
		{"agy claude opus 4.6 thinking", "claude-opus-4-6-thinking", 256000, true},
		{"agy claude sonnet 4.6", "claude-sonnet-4-6", 256000, true},
		{"agy gemini 3.1 pro high", "gemini-3.1-pro-high", 1048576, true},
		{"agy gemini 3.1 pro low", "gemini-3.1-pro-low", 1048576, true},
		{"agy gemini 3.7 flash high", "gemini-3.7-flash-high", 1048576, true},
		{"agy gemini 3.7 flash low", "gemini-3.7-flash-low", 1048576, true},
		{"agy gemini 3.7 flash medium", "gemini-3.7-flash-medium", 1048576, true},

		// OpenCode Models
		{"opencode big pickle", "opencode/big-pickle", 200000, true},
		{"opencode ling 3.0 flash fin free", "opencode/ling-3.0-flash-fin-free", 262144, true},
		{"opencode mimo v2.5 free", "opencode/mimo-v2.5-free", 1048576, true},
		{"opencode muse spark 1.2 contributor free", "opencode/muse-spark-1.2-contributor-free", 1048576, true},
		{"opencode nemotron 3 ultra free", "opencode/nemotron-3-ultra-free", 262144, true},
		{"opencode nemotron 3.5 lightning free", "opencode/nemotron-3.5-lightning-free", 262144, true},
		{"zai glm-5.3 base", "zai-coding-plan/glm-5.3", 1048576, true},
		{"zai glm-5.3 with slash variant", "zai-coding-plan/glm-5.3/low", 1048576, true},
		{"zai glm-5.3-flash base", "zai-coding-plan/glm-5.3-flash", 1048576, true},

		// Unlisted / Bare names without provider prefix (Soft-pass fallback to 1M default)
		{"bare big pickle without prefix", "big-pickle", DefaultContextWindow, false},
		{"bare glm-5.3 without prefix", "glm-5.3", DefaultContextWindow, false},
		{"claude opus 4.6 without thinking", "claude-opus-4-6", DefaultContextWindow, false},
		{"unlisted claude 3.7", "claude-3-7-sonnet", DefaultContextWindow, false},
		{"unknown gpt-4o", "gpt-4o", DefaultContextWindow, false},
		{"unknown deepseek", "deepseek-chat", DefaultContextWindow, false},
		{"unknown custom model", "custom-provider/unknown-model-xyz", DefaultContextWindow, false},
		{"empty model string", "", DefaultContextWindow, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotLimit, gotKnown := LookupContextWindow(tt.model)
			assert.Equal(t, tt.wantLimit, gotLimit, "limit mismatch for model %s", tt.model)
			assert.Equal(t, tt.wantKnown, gotKnown, "known mismatch for model %s", tt.model)

			gotHelper := GetModelContextWindow(tt.model)
			assert.Equal(t, tt.wantLimit, gotHelper, "GetModelContextWindow mismatch for model %s", tt.model)
		})
	}
}
