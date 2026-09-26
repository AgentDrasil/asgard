package simplest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest"
)

func TestSession_ExportedUsage(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "usage", simplest.TypeUsage)

	mgr := simplest.New(t.TempDir())
	sf, err := mgr.Create(t.TempDir(), nil)
	require.NoError(t, err)

	usageData := &simplest.Usage{
		Input:       50,
		Output:      25,
		TotalTokens: 75,
	}
	id, err := sf.AppendUsage(usageData, "external test note")
	require.NoError(t, err)
	assert.NotEmpty(t, id)
}
