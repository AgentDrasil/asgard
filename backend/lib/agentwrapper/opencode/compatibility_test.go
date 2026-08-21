package opencode

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/agentwrapper/types"
)

func skipIfNotE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("E2E_TEST") != "true" {
		t.Skip("Skipping compatibility test; set E2E_TEST=true to run it.")
	}
}

func TestCompatibility_StreamParserGoVersion(t *testing.T) {
	skipIfNotE2E(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	type reportedCall struct {
		stepIndex int
		source    string
		entryType string
		content   string
		metadata  map[string]any
	}

	var calls []reportedCall
	cb := func(stepIndex int, source, entryType, content string, metadata map[string]any) {
		calls = append(calls, reportedCall{stepIndex, source, entryType, content, metadata})
	}

	prompt := "what is the go version in path, and go version in current project"
	res, err := Prompt(ctx, prompt, types.PromptOptions{
		ReportCallback: cb,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, res.SessionID)
	assert.NotEmpty(t, res.LastContent)

	// Check that stream events were captured
	require.NotEmpty(t, calls, "expected stream events in ReportCallback")

	// Expect agent run command `go version` or bash execution
	var ranGoVersion bool
	for _, call := range calls {
		if strings.Contains(strings.ToLower(call.content), "go version") {
			ranGoVersion = true
			break
		}
	}
	assert.True(t, ranGoVersion, "expected agent to run command 'go version' or emit 'go version' tool call")

	// Expect response to contain go version information
	lowerLast := strings.ToLower(res.LastContent)
	assert.Contains(t, lowerLast, "go")
}
