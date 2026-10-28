package runs

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/AgentDrasil/asgard/pkg/runnerpb"
)

func TestTable(t *testing.T) {
	t.Parallel()
	tb := NewTable()
	assert.True(t, tb.Begin(&runnerpb.RunRef{RunId: "b"}))
	assert.True(t, tb.Begin(&runnerpb.RunRef{RunId: "a"}))
	assert.False(t, tb.Begin(&runnerpb.RunRef{RunId: "a"}))
	assert.Equal(t, 2, tb.Len())
	assert.Equal(t, "a", tb.Active()[0].GetRunId())
	tb.End("a")
	assert.Equal(t, 1, tb.Len())
}
