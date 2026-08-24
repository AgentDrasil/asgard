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
