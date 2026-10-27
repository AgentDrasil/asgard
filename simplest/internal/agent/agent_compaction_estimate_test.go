package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

func mustUserContent(t *testing.T, blocks ...types.AssistantContent) json.RawMessage {
	t.Helper()
	raw, err := types.MarshalBlocks(blocks)
	require.NoError(t, err)
	return raw
}

func textBlock(s string) types.TextContent { return types.TextContent{Type: types.TypeText, Text: s} }

func imageBlock() types.ImageContent {
	return types.ImageContent{Type: types.TypeImage, Data: "img", MimeType: "image/png"}
}

func TestEstimateMessageTokensImagesAndText(t *testing.T) {
	t.Parallel()
	// 8 text chars + 4800 image chars = 4808 chars -> ceil(4808/4) = 1202.
	u := &types.UserMessage{Content: mustUserContent(t, textBlock("12345678"), imageBlock())}
	assert.EqualValues(t, 1202, estimateMessageTokens(u))

	tr := &types.ToolResultMessage{Content: mustUserContent(t, textBlock("ab"), imageBlock())}
	// 2 + 4800 = 4802 chars -> 1201 tokens (ceil).
	assert.EqualValues(t, 1201, estimateMessageTokens(tr))
}

func TestEstimateMessageTokensAssistantToolCall(t *testing.T) {
	t.Parallel()
	args, err := json.Marshal(map[string]int{"a": 1})
	require.NoError(t, err)
	am := &types.AssistantMessage{
		Content: []types.AssistantContent{
			textBlock("hi"),                                 // 2
			types.ThinkingContent{Thinking: "th"},           // 2
			types.ToolCall{Name: "mytool", Arguments: args}, // len("mytool")=6 + len(args)
		},
	}
	chars := 2 + 2 + len("mytool") + len(args)
	want := int64((chars + 3) / 4)
	assert.EqualValues(t, want, estimateMessageTokens(am))
}

func TestEstimateMessageTokensPerMessageCeiling(t *testing.T) {
	t.Parallel()
	// 5 chars -> 2 tokens, not 1. Two such messages -> 4 total, not 2.
	m1 := &types.UserMessage{Content: mustUserContent(t, textBlock("12345"))}
	m2 := &types.UserMessage{Content: mustUserContent(t, textBlock("12345"))}
	assert.EqualValues(t, 2, estimateMessageTokens(m1))
	assert.EqualValues(t, 4, estimateMessageTokens(m1)+estimateMessageTokens(m2))
}

func TestEstimateContextTokensPrefersTotalTokens(t *testing.T) {
	t.Parallel()
	valid := &types.AssistantMessage{
		StopReason: types.StopStop,
		Usage: types.Usage{
			Input: 100, Output: 50, CacheRead: 30, CacheWrite: 20,
			TotalTokens: 9999,
		},
	}
	msgs := []types.Message{
		&types.UserMessage{Content: mustUserContent(t, textBlock("earlier"))},
		valid,
		&types.UserMessage{Content: mustUserContent(t, textBlock("tail"))},
	}
	tokens, usage := estimateContextTokens(msgs)
	require.NotNil(t, usage)
	assert.EqualValues(t, 9999, usage.TotalTokens)
	// 9999 + ceil(4/4)=1 for the trailing user message.
	assert.EqualValues(t, 10000, tokens)
}

func TestEstimateContextTokensSkipsAbortedAndError(t *testing.T) {
	t.Parallel()
	aborted := &types.AssistantMessage{
		StopReason: types.StopAborted,
		Usage:      types.Usage{Input: 1000, Output: 100, TotalTokens: 1100},
	}
	errored := &types.AssistantMessage{
		StopReason: types.StopError,
		Usage:      types.Usage{Input: 2000, Output: 100, TotalTokens: 2100},
	}
	valid := &types.AssistantMessage{
		StopReason: types.StopStop,
		Usage:      types.Usage{Input: 10, Output: 5, TotalTokens: 15},
	}
	msgs := []types.Message{
		&types.UserMessage{Content: mustUserContent(t, textBlock("first"))},
		valid,
		aborted,
		errored,
		&types.UserMessage{Content: mustUserContent(t, textBlock("tail"))},
	}
	tokens, usage := estimateContextTokens(msgs)
	require.NotNil(t, usage)
	assert.EqualValues(t, 15, usage.TotalTokens)
	// valid at index 1: usage 15 + estimate of aborted + errored + trailing user
	// (aborted/errored are empty-content assistant messages -> 0 tokens) + 1.
	assert.EqualValues(t, 16, tokens)
}

func TestEstimateContextTokensZeroUsageAssistantNotUsed(t *testing.T) {
	t.Parallel()
	zero := &types.AssistantMessage{StopReason: types.StopStop}
	valid := &types.AssistantMessage{
		StopReason: types.StopStop,
		Usage:      types.Usage{Input: 10, Output: 6},
	}
	msgs := []types.Message{valid, zero, &types.UserMessage{Content: mustUserContent(t, textBlock("tail"))}}
	tokens, usage := estimateContextTokens(msgs)
	require.NotNil(t, usage)
	// Falls back to component sum: 16; tail "tail" = 4 chars -> 1.
	assert.EqualValues(t, 17, tokens)
}

func TestEstimateContextTokensNoUsageEstimatesWholeTranscript(t *testing.T) {
	t.Parallel()
	msgs := []types.Message{
		&types.UserMessage{Content: mustUserContent(t, textBlock("12345"))}, // 2 tokens
		&types.UserMessage{Content: mustUserContent(t, textBlock("12345"))}, // 2 tokens
	}
	tokens, usage := estimateContextTokens(msgs)
	assert.Nil(t, usage)
	assert.EqualValues(t, 4, tokens)
}

func TestCompactionCutUsesPerMessageEstimates(t *testing.T) {
	t.Parallel()
	// Each message is 5 chars -> 2 tokens (per-message ceiling).
	// keepRecent=3: walking back, msg3 (2) < 3, +msg2 (4) >= 3 -> cut at index 1.
	// A single chars/4 division over the sum would need 12+ chars, differing.
	mk := func() *types.UserMessage {
		return &types.UserMessage{Content: mustUserContent(t, textBlock("12345"))}
	}
	msgs := []types.Message{mk(), mk(), mk()}
	cut := compactionCut(msgs, 3)
	assert.Equal(t, 1, cut)
}

func TestCompactionCutMovesToUserBoundary(t *testing.T) {
	t.Parallel()
	mk := func() *types.UserMessage {
		return &types.UserMessage{Content: mustUserContent(t, textBlock("12345678"))} // 2 tokens
	}
	msgs := []types.Message{
		mk(), // user
		&types.AssistantMessage{Content: []types.AssistantContent{textBlock("xx")}}, // 1 token
		mk(), // user
	}
	cut := compactionCut(msgs, 2)
	// accumulated >= 2 at index 2; boundary already a user message.
	assert.Equal(t, 2, cut)
}
