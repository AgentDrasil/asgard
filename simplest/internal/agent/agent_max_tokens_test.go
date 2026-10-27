package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// --- integration-style tests through fakeProvider.options capture ---

// G2: an explicitly set Request.MaxTokens is clamped to the remaining context
// window. Model.MaxTokens is deliberately NOT used as a default cap
// (divergence from pi buildBaseOptions; see clampRequestMaxTokens).

func TestMaxTokensNilStaysNilDespiteModelMaxTokens(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{responses: []*types.AssistantMessage{textMsg("done")}}
	req := baseRequest(fp)
	req.Model.MaxTokens = 8192
	_, end := collect(t, Run(context.Background(), req))
	require.NotNil(t, end)
	require.Len(t, fp.options, 1)
	assert.Nil(t, fp.options[0].MaxTokens, "nil Request.MaxTokens must not default to Model.MaxTokens")
}

func TestMaxTokensExplicitSmallUnchanged(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{responses: []*types.AssistantMessage{textMsg("done")}}
	req := baseRequest(fp)
	mt := int64(1000)
	req.MaxTokens = &mt
	_, end := collect(t, Run(context.Background(), req))
	require.NotNil(t, end)
	require.Len(t, fp.options, 1)
	require.NotNil(t, fp.options[0].MaxTokens)
	assert.Equal(t, int64(1000), *fp.options[0].MaxTokens)
}

func TestMaxTokensClampedToOneForNearWindowTranscript(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{responses: []*types.AssistantMessage{textMsg("done")}}
	req := baseRequest(fp)
	req.Model.ContextWindow = 10000
	big := make([]byte, 40000)
	for i := range big {
		big[i] = 'a'
	}
	req.Messages = []types.Message{&types.UserMessage{Content: types.TextOnly(string(big)), Timestamp: 1}}
	mt := int64(8192)
	req.MaxTokens = &mt
	_, end := collect(t, Run(context.Background(), req))
	require.NotNil(t, end)
	require.Len(t, fp.options, 1)
	require.NotNil(t, fp.options[0].MaxTokens)
	// ceil(40000/3.5)=11429 message tokens + 1 for the 3-char system prompt;
	// 10000 - 11430 - 4096 < 0, so the floor of 1 applies.
	assert.Equal(t, int64(1), *fp.options[0].MaxTokens)
}

func TestMaxTokensClampedWithSystemPromptAndTools(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{responses: []*types.AssistantMessage{textMsg("done")}}
	req := baseRequest(fp, newRecordingTool("t"))
	req.Model.ContextWindow = 10000
	sys := make([]byte, 3500)
	for i := range sys {
		sys[i] = 's'
	}
	req.SystemPrompt = string(sys)
	mt := int64(5000)
	req.MaxTokens = &mt
	_, end := collect(t, Run(context.Background(), req))
	require.NotNil(t, end)
	require.Len(t, fp.options, 1)
	require.NotNil(t, fp.options[0].MaxTokens)
	// Estimated: system(3500) + tool name(1)+desc(4)+params(53) = 3558 chars
	// -> ceil(3558/3.5) = 1017; user "go" -> 1; total 1018.
	// available = 10000 - 1018 - 4096 = 4886.
	assert.Less(t, *fp.options[0].MaxTokens, int64(5000))
	assert.Equal(t, int64(4886), *fp.options[0].MaxTokens)
}

func TestMaxTokensContextWindowZeroUnchanged(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{responses: []*types.AssistantMessage{textMsg("done")}}
	req := baseRequest(fp)
	req.Model.ContextWindow = 0
	mt := int64(1234)
	req.MaxTokens = &mt
	_, end := collect(t, Run(context.Background(), req))
	require.NotNil(t, end)
	require.Len(t, fp.options, 1)
	require.NotNil(t, fp.options[0].MaxTokens)
	assert.Equal(t, int64(1234), *fp.options[0].MaxTokens)
}

func TestMaxTokensExplicitZeroKept(t *testing.T) {
	t.Parallel()
	fp := &fakeProvider{responses: []*types.AssistantMessage{textMsg("done")}}
	req := baseRequest(fp)
	mt := int64(0)
	req.MaxTokens = &mt
	_, end := collect(t, Run(context.Background(), req))
	require.NotNil(t, end)
	require.Len(t, fp.options, 1)
	require.NotNil(t, fp.options[0].MaxTokens, "explicit 0 keeps the existing pointer (documented behavior)")
	assert.Equal(t, int64(0), *fp.options[0].MaxTokens)
}

// --- estimator unit tests ---

func TestEstimateRequestContextTokensPrefixTimestampRule(t *testing.T) {
	t.Parallel()
	// Assistant timestamped before a later-inserted prefix message: its usage
	// is rejected and the estimator falls back to the full transcript plus
	// system prompt and tools.
	stale := &types.AssistantMessage{
		Content:    []types.AssistantContent{types.TextContent{Type: "text", Text: "hey"}},
		StopReason: types.StopStop,
		Usage:      types.Usage{TotalTokens: 100},
		Timestamp:  500,
	}
	msgs := []types.Message{
		&types.UserMessage{Content: types.TextOnly("go"), Timestamp: 1000},
		stale,
	}
	// Full estimate: system "sys"(3 chars)->1, user "go"(2)->1, assistant
	// "hey"(3)->1.
	assert.Equal(t, int64(3), estimateRequestContextTokens(msgs, "sys", nil))

	// A qualifying assistant's usage is preferred and system/tools are NOT
	// counted again (usage already includes them): 100 + user "go"(1).
	fresh := &types.AssistantMessage{
		Content:    []types.AssistantContent{types.TextContent{Type: "text", Text: "hey"}},
		StopReason: types.StopStop,
		Usage:      types.Usage{TotalTokens: 100},
		Timestamp:  500,
	}
	msgs = []types.Message{
		&types.UserMessage{Content: types.TextOnly("u"), Timestamp: 100},
		fresh,
		&types.UserMessage{Content: types.TextOnly("go"), Timestamp: 1000},
	}
	assert.Equal(t, int64(101), estimateRequestContextTokens(msgs, "sys", nil))
}

func TestEstimateRequestContextTokensImagesAndToolCallsAt35(t *testing.T) {
	t.Parallel()
	imgRaw, err := json.Marshal([]types.AssistantContent{
		types.ImageContent{Type: "image", Data: "Zm9v", MimeType: "image/png"},
	})
	require.NoError(t, err)
	user := &types.UserMessage{Content: imgRaw, Timestamp: 1}
	// 4800 chars / 3.5 -> ceil = 1372.
	assert.Equal(t, int64(1372), estimateRequestContextTokens([]types.Message{user}, "", nil))

	call := call("c1", "ab", `{"x":1}`)
	am := toolCallMsg(call)
	// name "ab"(2) + args(7) = 9 chars / 3.5 -> ceil = 3.
	assert.Equal(t, int64(3), estimateRequestContextTokens([]types.Message{am}, "", nil))

	// System prompt + wire tool definitions count when there is no usage.
	tools := []types.ToolDef{{Name: "t", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}}
	// 3 + 1 + 1 + 17 = 22 chars -> ceil(22/3.5) = 7.
	assert.Equal(t, int64(7), estimateRequestContextTokens(nil, "sys", tools))
}
