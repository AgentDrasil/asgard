package runnerfs

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/AgentDrasil/asgard/llms"
)

func TestQuotaProtoRoundTripKeepsAccount(t *testing.T) {
	in := map[string][]llms.ModelUsage{
		"claude": {
			{Model: "m1", Remaining: 0.5, Account: "work"},
			{Model: "m1", Remaining: 0.9, Account: "home"},
		},
		"opencode": {{Model: "m2", Remaining: 1}},
	}
	wire := QuotaToProto(in)
	assert.Equal(t, "work", wire[0].GetAccount())
	assert.Equal(t, "home", wire[1].GetAccount())
	assert.Equal(t, "", wire[2].GetAccount())
	assert.Equal(t, in, QuotaFromProto(wire))
}
