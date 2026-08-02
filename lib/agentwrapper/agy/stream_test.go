package agy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"
)

// realNDJSON is captured verbatim from a real agy invocation:
//
//	agy --dangerously-skip-permissions --output-format stream-json \
//	    --add-dir /home/user/src/AgentDrasil/asgard \
//	    -p "what is the go version used in current project and what is the go version in path"
const realNDJSON = `{"event":"init","conversation_id":"57659af8-fee7-4694-8913-6ad09e91234a","init":{"cwd":"/home/user/src/AgentDrasil/asgard","tools":["run_command"],"permission_mode":"always-proceed"}}
{"event":"step_update","step_update":{"conversation_id":"57659af8-fee7-4694-8913-6ad09e91234a","step_index":0,"state":"DONE","step_type":"user_input"}}
{"event":"step_update","step_update":{"conversation_id":"57659af8-fee7-4694-8913-6ad09e91234a","step_index":1,"state":"DONE","step_type":"unknown","duration_seconds":0.000419037}}
{"event":"step_update","step_update":{"conversation_id":"57659af8-fee7-4694-8913-6ad09e91234a","step_index":2,"state":"DONE","step_type":"agent_response","duration_seconds":0.858929275,"usage":{"input_tokens":10534,"output_tokens":124,"thinking_tokens":0,"cache_read_tokens":8143,"total_tokens":10658}}}
{"event":"step_update","step_update":{"conversation_id":"57659af8-fee7-4694-8913-6ad09e91234a","step_index":3,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"go version"}}}}
{"event":"step_update","step_update":{"conversation_id":"57659af8-fee7-4694-8913-6ad09e91234a","step_index":3,"state":"DONE","step_type":"tool","tool_name":"run_command","duration_seconds":0.0144757,"tool_info":{"name":"run_command","parameters":{"CommandLine":"go version"},"output":"go version go1.26.5-X:nodwarf5 linux/amd64\r\n"}}}
{"event":"step_update","step_update":{"conversation_id":"57659af8-fee7-4694-8913-6ad09e91234a","step_index":4,"state":"ACTIVE","step_type":"tool","tool_name":"view_file","tool_info":{"name":"view_file","parameters":{"AbsolutePath":"/home/user/src/AgentDrasil/asgard/go.mod"}}}}
{"event":"step_update","step_update":{"conversation_id":"57659af8-fee7-4694-8913-6ad09e91234a","step_index":4,"state":"DONE","step_type":"tool","tool_name":"view_file","duration_seconds":0.067443493,"tool_info":{"name":"view_file","parameters":{"AbsolutePath":"/home/user/src/AgentDrasil/asgard/go.mod"},"output":"75 lines, 3057 bytes"}}}
{"event":"step_update","step_update":{"conversation_id":"57659af8-fee7-4694-8913-6ad09e91234a","step_index":5,"state":"DONE","step_type":"checkpoint","duration_seconds":0.568097745,"usage":{"input_tokens":109,"output_tokens":4,"thinking_tokens":0,"cache_read_tokens":0,"total_tokens":113}}}
{"event":"step_update","step_update":{"conversation_id":"57659af8-fee7-4694-8913-6ad09e91234a","step_index":6,"state":"ACTIVE","step_type":"agent_response","text_delta":"Here are the Go versions requested:\n\n- **Current Project Go Version** (` + "`go.mod`" + ` line 3): **` + "`1.26.4`" + `** ([go.mod](file:///home/user/src/AgentDrasil/asgard/go.mod#L3))\n- *"}}
{"event":"step_update","step_update":{"conversation_id":"57659af8-fee7-4694-8913-6ad09e91234a","step_index":6,"state":"DONE","step_type":"agent_response","text_delta":"*System PATH Go Version**: **` + "`1.26.5`" + `** (` + "`go version go1.26.5-X:nodwarf5 linux/amd64`" + `)\n","duration_seconds":0.990927867,"usage":{"input_tokens":4555,"output_tokens":103,"thinking_tokens":0,"cache_read_tokens":16282,"total_tokens":4658}}}
{"event":"result","result":{"conversation_id":"57659af8-fee7-4694-8913-6ad09e91234a","status":"SUCCESS","response":"Here are the Go versions requested:\n\n- **Current Project Go Version** (` + "`go.mod`" + ` line 3): **` + "`1.26.4`" + `** ([go.mod](file:///home/user/src/AgentDrasil/asgard/go.mod#L3))\n- **System PATH Go Version**: **` + "`1.26.5`" + `** (` + "`go version go1.26.5-X:nodwarf5 linux/amd64`" + `)\n","duration_seconds":1.876298049,"num_turns":1,"usage":{"input_tokens":15198,"output_tokens":231,"thinking_tokens":0,"cache_read_tokens":24425,"total_tokens":15429}}}`

// wantResponse is the expected final response text (= result.response).
// It must equal the concatenation of text_deltas from step 6 ACTIVE + DONE,
// and must NOT contain any tool output.
const wantResponse = "Here are the Go versions requested:\n\n" +
	"- **Current Project Go Version** (`go.mod` line 3): **`1.26.4`** ([go.mod](file:///home/user/src/AgentDrasil/asgard/go.mod#L3))\n" +
	"- **System PATH Go Version**: **`1.26.5`** (`go version go1.26.5-X:nodwarf5 linux/amd64`)\n"

type streamCall struct {
	stepIndex int
	source    string
	entryType string
	content   string
}

func collectCalls(ndjson string) (sessionID, lastContent string, inputTokens int, calls []streamCall) {
	cb := types.ReportFunc(func(si int, src, et, content string, _ map[string]any) {
		calls = append(calls, streamCall{si, src, et, content})
	})
	sessionID, lastContent, inputTokens = parseStream(strings.NewReader(ndjson), cb)
	return
}

// TestParseStream_Replay feeds the exact NDJSON from a real agy run into
// parseStream and verifies every output field.
func TestParseStream_Replay(t *testing.T) {
	sessionID, lastContent, inputTokens, calls := collectCalls(realNDJSON)

	// session ID comes from the "init" event
	assert.Equal(t, "57659af8-fee7-4694-8913-6ad09e91234a", sessionID)

	// inputTokens comes from result.usage
	assert.Equal(t, 15198, inputTokens)

	// lastContent is result.response — NOT any tool output
	assert.Equal(t, wantResponse, lastContent)

	// Expected callbacks:
	//   step 2 agent_response DONE has no text_delta → no callback
	//   step 3 tool run_command: ACTIVE → tool_call, DONE → tool_result
	//   step 4 tool view_file:   ACTIVE → tool_call, DONE → tool_result
	//   step 5 checkpoint → ignored
	//   step 6 agent_response: ACTIVE+DONE accumulate → one agent_response callback
	require.Len(t, calls, 5)

	assert.Equal(t, streamCall{3, "TOOL", "tool_call", "> go version"}, calls[0])
	assert.Equal(t, streamCall{3, "TOOL", "tool_result", "> go version\n\ngo version go1.26.5-X:nodwarf5 linux/amd64\r\n"}, calls[1])
	assert.Equal(t, streamCall{4, "TOOL", "tool_call", "> /home/user/src/AgentDrasil/asgard/go.mod"}, calls[2])
	assert.Equal(t, streamCall{4, "TOOL", "tool_result", "> /home/user/src/AgentDrasil/asgard/go.mod\n\n75 lines, 3057 bytes"}, calls[3])
	assert.Equal(t, streamCall{6, "MODEL", "agent_response", wantResponse}, calls[4])
}

// TestParseStream_NilCallback verifies that parseStream still returns the
// correct sessionID and lastContent when no callback is registered.
func TestParseStream_NilCallback(t *testing.T) {
	sessionID, lastContent, inputTokens := parseStream(strings.NewReader(realNDJSON), nil)
	assert.Equal(t, "57659af8-fee7-4694-8913-6ad09e91234a", sessionID)
	assert.Equal(t, wantResponse, lastContent)
	assert.Equal(t, 15198, inputTokens)
}

// TestParseStream_ToolFormatting covers the content string built for every
// tool branch: with params, no params, with output, with error, empty output.
func TestParseStream_ToolFormatting(t *testing.T) {
	cases := []struct {
		name      string
		ndjson    string
		wantCalls []streamCall
	}{
		{
			name:   "tool_call with parameters",
			ndjson: `{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"go version"}}}}`,
			wantCalls: []streamCall{
				{1, "TOOL", "tool_call", "> go version"},
			},
		},
		{
			name:   "tool_call without parameters",
			ndjson: `{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"tool","tool_name":"list_dir","tool_info":{"name":"list_dir","parameters":{}}}}`,
			wantCalls: []streamCall{
				{1, "TOOL", "tool_call", "> list_dir"},
			},
		},
		{
			name:   "tool_result with output",
			ndjson: `{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","parameters":{"CommandLine":"go version"},"output":"go version go1.26.0"}}}`,
			wantCalls: []streamCall{
				{2, "TOOL", "tool_result", "> go version\n\ngo version go1.26.0"},
			},
		},
		{
			name:   "tool_result with error",
			ndjson: `{"event":"step_update","step_update":{"step_index":3,"state":"ERROR","step_type":"tool","tool_name":"list_dir","tool_info":{"name":"list_dir","error":{"type":"TOOL_ERROR","message":"Permission denied"}}}}`,
			wantCalls: []streamCall{
				{3, "TOOL", "tool_result", "> list_dir\n\nPermission denied"},
			},
		},
		{
			name:   "tool_result with empty output",
			ndjson: `{"event":"step_update","step_update":{"step_index":4,"state":"DONE","step_type":"tool","tool_name":"run_command","tool_info":{"name":"run_command","output":""}}}`,
			wantCalls: []streamCall{
				{4, "TOOL", "tool_result", "> run_command"},
			},
		},
		{
			name: "agent_response ACTIVE+DONE accumulates text_delta",
			ndjson: `{"event":"step_update","step_update":{"step_index":5,"state":"ACTIVE","step_type":"agent_response","text_delta":"Hello "}}
{"event":"step_update","step_update":{"step_index":5,"state":"DONE","step_type":"agent_response","text_delta":"world!\n"}}`,
			wantCalls: []streamCall{
				{5, "MODEL", "agent_response", "Hello world!\n"},
			},
		},
		{
			name:      "agent_response DONE with no text_delta fires no callback",
			ndjson:    `{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"agent_response","duration_seconds":0.86}}`,
			wantCalls: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, calls := collectCalls(tc.ndjson)
			assert.Equal(t, tc.wantCalls, calls)
		})
	}
}
