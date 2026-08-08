package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRemapSandboxPath(t *testing.T) {
	assert.Equal(t, "main.go", RemapSandboxPath("main.go"))
	assert.Equal(t, ".tmp/test.md", RemapSandboxPath("/tmp/test.md"))
	assert.Equal(t, ".tmp/dir/file.go", RemapSandboxPath("/tmp/dir/file.go"))
	assert.Equal(t, "src/app.ts", RemapSandboxPath("src/app.ts"))
	assert.Equal(t, "", RemapSandboxPath(""))
	assert.Equal(t, ".tmp/space.txt", RemapSandboxPath("  /tmp/space.txt  "))
}
