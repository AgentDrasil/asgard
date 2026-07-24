package opencode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePromptOutput(t *testing.T) {
	output := `{"type":"step_start","timestamp":1780371838546,"sessionID":"ses_17991b726ffezSLOoMYnmU961P","part":{"id":"prt_e866e564e001HoQMkc21Wc9UDa","messageID":"msg_e866e4995001ONI66mwvx24fnl","sessionID":"ses_17991b726ffezSLOoMYnmU961P","snapshot":"90fbf8588f2b75446d6d498d93af0dd2ba73943d","type":"step-start"}}
{"type":"tool_use","timestamp":1780371840625,"sessionID":"ses_17991b726ffezSLOoMYnmU961P","part":{"type":"tool","tool":"bash","callID":"call_function_lrjqtlkptlfh_1","state":{"status":"completed","input":{"command":"head -5 go.mod 2>/dev/null || cat README.md 2>/dev/null | head -20","description":"Check go version in go.mod"},"output":"module github.com/AgentDrasil/agent-wrapper\n\ngo 1.26.3\n\nrequire (\n","metadata":{"output":"module github.com/AgentDrasil/agent-wrapper\n\ngo 1.26.3\n\nrequire (\n","exit":0,"description":"Check go version in go.mod","truncated":false},"title":"Check go version in go.mod","time":{"start":1780371840602,"end":1780371840616}},"id":"prt_e866e5c75001PyVUE0OOJyggm7","sessionID":"ses_17991b726ffezSLOoMYnmU961P","messageID":"msg_e866e4995001ONI66mwvx24fnl"}}
{"type":"step_finish","timestamp":1780371840662,"sessionID":"ses_17991b726ffezSLOoMYnmU961P","part":{"id":"prt_e866e5e8e001FCiwQVU5eh55v1","reason":"tool-calls","snapshot":"90fbf8588f2b75446d6d498d93af0dd2ba73943d","messageID":"msg_e866e4995001ONI66mwvx24fnl","sessionID":"ses_17991b726ffezSLOoMYnmU961P","type":"step-finish","tokens":{"total":8533,"input":14,"output":90,"reasoning":0,"cache":{"write":0,"read":8429}},"cost":0}}
{"type":"step_start","timestamp":1780371842556,"sessionID":"ses_17991b726ffezSLOoMYnmU961P","part":{"id":"prt_e866e65f9001dPyEjOu50OitSQ","messageID":"msg_e866e5ebd001mVG88x5OTCaHvA","sessionID":"ses_17991b726ffezSLOoMYnmU961P","snapshot":"90fbf8588f2b75446d6d498d93af0dd2ba73943d","type":"step-start"}}
{"type":"text","timestamp":1780371842907,"sessionID":"ses_17991b726ffezSLOoMYnmU961P","part":{"id":"prt_e866e6753001kl4XC8gNYIqSvO","messageID":"msg_e866e5ebd001mVG88x5OTCaHvA","sessionID":"ses_17991b726ffezSLOoMYnmU961P","type":"text","text":"Go 1.26.3","time":{"start":1780371842899,"end":1780371842904}}}
{"type":"step_finish","timestamp":1780371842964,"sessionID":"ses_17991b726ffezSLOoMYnmU961P","part":{"id":"prt_e866e678e0011vL0QFHJPSg1QK","reason":"stop","snapshot":"90fbf8588f2b75446d6d498d93af0dd2ba73943d","messageID":"msg_e866e5ebd001mVG88x5OTCaHvA","sessionID":"ses_17991b726ffezSLOoMYnmU961P","type":"step-finish","tokens":{"total":8591,"input":51,"output":21,"reasoning":0,"cache":{"write":0,"read":8519}},"cost":0}}`

	var sessionID string
	var inputTokens int
	var totalTokens int
	var targetMessageID string
	textMap := make(map[string]*strings.Builder)

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		var opl opencodeLine
		err := json.Unmarshal([]byte(trimmed), &opl)
		require.NoError(t, err)

		if opl.SessionID != "" {
			sessionID = opl.SessionID
		}

		if opl.Type == "text" && opl.Part.MessageID != "" {
			builder, exists := textMap[opl.Part.MessageID]
			if !exists {
				builder = &strings.Builder{}
				textMap[opl.Part.MessageID] = builder
			}
			builder.WriteString(opl.Part.Text)
		}

		if opl.Type == "step_finish" {
			if opl.Part.Tokens.Input > 0 {
				inputTokens = opl.Part.Tokens.Input
			}
			if opl.Part.Tokens.Total > 0 {
				totalTokens = opl.Part.Tokens.Total
			}
			if opl.Part.Reason == "stop" && opl.Part.MessageID != "" {
				targetMessageID = opl.Part.MessageID
			}
		}
	}

	assert.Equal(t, "ses_17991b726ffezSLOoMYnmU961P", sessionID)
	assert.Equal(t, 51, inputTokens)
	assert.Equal(t, 8591, totalTokens)
	assert.Equal(t, "msg_e866e5ebd001mVG88x5OTCaHvA", targetMessageID)

	var lastContent string
	if targetMessageID != "" {
		if builder, exists := textMap[targetMessageID]; exists {
			lastContent = builder.String()
		}
	}
	assert.Equal(t, "Go 1.26.3", lastContent)
}

func TestClassifyLineAndContent(t *testing.T) {
	toolUseLine := `{"type":"tool_use","timestamp":1784810278338,"sessionID":"ses_0710485c4ffeJikUP3wUorQ5Ob","part":{"type":"tool","tool":"bash","callID":"tool-d3dd738fa0d740039f234b487570c782","state":{"status":"completed","input":{"command":"go version"},"output":"go version go1.26.5-X:nodwarf5 linux/amd64\n"},"id":"prt_f8efb8d82001Uqnb9s1Xamiiis"}}`

	var opl opencodeLine
	err := json.Unmarshal([]byte(toolUseLine), &opl)
	require.NoError(t, err)

	entryType := classifyLine(&opl)
	assert.Equal(t, "tool_call", entryType)
	assert.Equal(t, "bash", opl.Part.Tool)
	assert.Equal(t, "go version go1.26.5-X:nodwarf5 linux/amd64\n", opl.Part.State.Output)
}
