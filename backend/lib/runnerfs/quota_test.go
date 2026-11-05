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

func TestQuotaStatusEntries(t *testing.T) {
	in := map[string][]llms.ModelUsage{
		"agy":    {{Status: llms.QuotaStatusNoCredentials, Message: "Authentication required"}},
		"claude": {{Model: "opus", Remaining: 0.5}, {Account: "w", Status: llms.QuotaStatusError, Message: "boom"}},
	}
	wire := QuotaToProto(in)
	assert.Equal(t, in, QuotaFromProtoWithStatus(wire))
	// Plain consumers never see a model-less entry as a 0% model.
	assert.Equal(t, map[string][]llms.ModelUsage{"claude": {{Model: "opus", Remaining: 0.5}}}, QuotaFromProto(wire))
}
