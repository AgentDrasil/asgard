package common

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderParseRoundTrip(t *testing.T) {
	t.Parallel()

	original := &Contract{
		AgentID:    "intend",
		AgentName:  "Intend Planner",
		ToolAccess: "doc-only",
		Team:       "core-team",
		Body:       "## Language Preferences\n\nrule body here",
	}

	parsed, ok := Parse([]byte(Render(original)))
	require.True(t, ok)
	assert.Equal(t, original.AgentID, parsed.AgentID)
	assert.Equal(t, original.AgentName, parsed.AgentName)
	assert.Equal(t, original.ToolAccess, parsed.ToolAccess)
	assert.Equal(t, original.Team, parsed.Team)
	assert.Equal(t, original.Body, parsed.Body)
}

func TestRenderEmpty(t *testing.T) {
	t.Parallel()

	assert.Empty(t, Render(&Contract{}))
	_, ok := Parse([]byte(""))
	assert.False(t, ok)
	_, ok = Parse([]byte("---\nagent_id: x\n---\n"))
	assert.True(t, ok) // metadata-only contract is valid
}

func TestRenderOmitsEmptyFields(t *testing.T) {
	t.Parallel()

	out := Render(&Contract{AgentID: "intend", Body: "body"})
	assert.Contains(t, out, "agent_id: intend")
	assert.NotContains(t, out, "agent_name")
	assert.NotContains(t, out, "tool_access")
	assert.NotContains(t, out, "team")
	assert.Contains(t, out, "\nbody")
}

func TestParseWithoutFrontmatter(t *testing.T) {
	t.Parallel()

	parsed, ok := Parse([]byte("just a body\n"))
	require.True(t, ok)
	assert.Empty(t, parsed.AgentID)
	assert.Equal(t, "just a body", parsed.Body)
}

func TestParseQuotedValues(t *testing.T) {
	t.Parallel()

	content := "---\nagent_name: \"Name: with colon\"\nteam: 'quoted team'\n---\nbody"
	parsed, ok := Parse([]byte(content))
	require.True(t, ok)
	assert.Equal(t, "Name: with colon", parsed.AgentName)
	assert.Equal(t, "quoted team", parsed.Team)
}

func TestParseUnterminatedFrontmatter(t *testing.T) {
	t.Parallel()

	parsed, ok := Parse([]byte("---\nagent_id: x\nbody without close"))
	require.True(t, ok)
	assert.Empty(t, parsed.AgentID)
	assert.Contains(t, parsed.Body, "agent_id: x")
}

func TestLoadMissing(t *testing.T) {
	t.Setenv(EnvVar, filepath.Join(t.TempDir(), "absent.md"))
	_, ok := Load()
	assert.False(t, ok)
}

func TestLoadPresent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AW_AGENTS.md")
	require.NoError(t, os.WriteFile(path, []byte(Render(&Contract{AgentID: "intend", Body: "hello"})), 0644))
	t.Setenv(EnvVar, path)

	loaded, ok := Load()
	require.True(t, ok)
	assert.Equal(t, "intend", loaded.AgentID)
	assert.Equal(t, "hello", loaded.Body)
}

func TestPathEnvOverride(t *testing.T) {
	t.Setenv(EnvVar, "/custom/path.md")
	assert.Equal(t, "/custom/path.md", Path())
	t.Setenv(EnvVar, "")
	assert.Equal(t, DefaultPath, Path())
}

func TestComposePrompt(t *testing.T) {
	t.Parallel()

	// No team: peer header dropped.
	got := ComposePrompt("HEADER", "PEER", "", "BODY")
	assert.Equal(t, "HEADER\n\nBODY", got)

	// Team set: peer header included.
	got = ComposePrompt("HEADER", "PEER", "squad", "BODY")
	assert.Equal(t, "HEADER\n\nPEER\n\nBODY", got)

	// Empty pieces collapse.
	got = ComposePrompt("", "", "squad", "BODY")
	assert.Equal(t, "BODY", got)
}

func TestYAMLQuote(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "plain", YAMLQuote("plain"))
	assert.Equal(t, "intend", YAMLQuote("intend"))
	assert.Equal(t, `""`, YAMLQuote(""))
	assert.Equal(t, `"with space"`, YAMLQuote("with space"))
	assert.Equal(t, `"**/*.md"`, YAMLQuote("**/*.md"))
	assert.Equal(t, `"git status*"`, YAMLQuote("git status*"))
	assert.Equal(t, `"a: b"`, YAMLQuote("a: b"))
	assert.Equal(t, `"say \"hi\""`, YAMLQuote(`say "hi"`))
}

func TestSanitizeAgentID(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "agent_father", SanitizeAgentID("Agent_Father"))
	assert.Equal(t, "intend", SanitizeAgentID("intend"))
	assert.Equal(t, "asgard_agent", SanitizeAgentID("///"))
	assert.Equal(t, "asgard_agent", SanitizeAgentID(""))
	assert.Equal(t, "a-b", SanitizeAgentID("a  b"))
}
