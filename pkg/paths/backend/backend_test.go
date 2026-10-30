package backend

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/AgentDrasil/asgard/pkg/paths"
)

func TestBackendPaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv(paths.EnvRoot, root)

	assert.Equal(t, filepath.Join(root, "data"), DataDir())
	assert.Equal(t, filepath.Join(root, "data", "data.db"), DBFile())
	assert.Equal(t, filepath.Join(root, "data", "avatars"), AvatarsDir())
	assert.Equal(t, filepath.Join(root, "logs"), LogsDir())
}
