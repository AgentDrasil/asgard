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

func TestSession_ExportedContextEdit(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "context_edit", simplest.TypeContextEdit)

	mgr := simplest.New(t.TempDir())
	sf, err := mgr.Create(t.TempDir(), nil)
	require.NoError(t, err)

	u1, err := sf.AppendMessage(&simplest.UserMessage{
		Content:   simplest.TextOnly("hello"),
		Timestamp: 1000,
	})
	require.NoError(t, err)

	rep := "hello world"
	editID, err := sf.AppendContextEdit(u1, &rep)
	require.NoError(t, err)
	assert.NotEmpty(t, editID)

	ctx, err := sf.BuildContext("")
	require.NoError(t, err)
	require.Len(t, ctx.Messages, 1)
	userMsg, ok := ctx.Messages[0].(*simplest.UserMessage)
	require.True(t, ok)
	assert.Contains(t, string(userMsg.Content), rep)
}
