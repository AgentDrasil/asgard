// Package common implements the AW_AGENTS.md contract shared between the
// Asgard host and the aw dispatcher running inside the sandbox.
//
// The host renders one contract file per agent run (agent identity metadata in
// a YAML frontmatter followed by the prompt body) and bind-mounts it read-only
// at /session/AW_AGENTS.md (path overridable via the AW_AGENTS_PATH env var).
// The aw dispatcher loads the contract and translates it for each target CLI,
// so the sandbox layer never needs to know any CLI-specific configuration
// paths.
package common

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// EnvVar names the environment variable that points at the mounted
	// contract file inside the sandbox.
	EnvVar = "AW_AGENTS_PATH"

	// DefaultPath is the canonical mount path of the contract file inside
	// the agent sandbox.
	DefaultPath = "/session/AW_AGENTS.md"

	// Frontmatter delimiter used to fence the metadata block.
	frontmatterDelimiter = "---"
)

// ManagedMarker tags files written by the aw dispatcher so pre-existing
// user content is never silently destroyed: user content is restored after
// the run instead.
const ManagedMarker = "<!-- managed by aw (agentwrapper) -->"

// Contract is the parsed representation of an AW_AGENTS.md file.
type Contract struct {
	// AgentID identifies the Asgard agent (agentspec.AgentConfig.ID).
	AgentID string

	// AgentName is the human-readable agent name.
	AgentName string

	// ToolAccess declares the agent's tool set ("full" or "doc-only").
	ToolAccess string

	// Team is the agent's team name; non-empty means the agent operates in
	// a multi-agent team and should receive the peer-collaboration header.
	Team string

	// Body is the CLI-agnostic prompt body: global language rules followed
	// by the agent's own AGENTS.md content (when present).
	Body string
}

// IsEmpty reports whether the contract carries no metadata and no body.
func (c *Contract) IsEmpty() bool {
	return c == nil || (c.AgentID == "" && c.AgentName == "" && c.ToolAccess == "" && c.Team == "" && strings.TrimSpace(c.Body) == "")
}

// Path returns the contract file location: $AW_AGENTS_PATH when set,
// otherwise DefaultPath.
func Path() string {
	if p := strings.TrimSpace(os.Getenv(EnvVar)); p != "" {
		return p
	}
	return DefaultPath
}

// Load reads and parses the contract file. It returns (nil, false) when the
// file is missing, unreadable, or empty, so callers can fall back to their
// CLI-native defaults when running outside the Asgard sandbox.
func Load() (*Contract, bool) {
	data, err := os.ReadFile(Path())
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return Parse(data)
}

// Parse parses raw contract file content. It returns (nil, false) when the
// content carries neither metadata nor a body.
//
// The frontmatter block is optional: content without a leading "---" fence
// is treated as body-only. An unterminated fence is likewise treated as body
// (the whole input, trimmed), so a malformed header can never silently drop
// the agent instructions. Unknown metadata keys are ignored.
func Parse(data []byte) (*Contract, bool) {
	c := &Contract{}
	c.Body = parseFrontmatter(string(data), c)
	if c.IsEmpty() {
		return nil, false
	}
	return c, true
}

// parseFrontmatter consumes an optional leading "---"-fenced metadata block,
// filling c from "key: value" lines, and returns the remaining body.
func parseFrontmatter(content string, c *Contract) string {
	rest := strings.TrimLeft(content, "\r\n\t ")
	if !strings.HasPrefix(rest, frontmatterDelimiter+"\n") && !strings.HasPrefix(rest, frontmatterDelimiter+"\r\n") {
		return strings.TrimSpace(content)
	}
	lines := strings.Split(rest, "\n")
	// lines[0] is the opening delimiter; find the closing one.
	bodyStart := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r\t ") == frontmatterDelimiter {
			bodyStart = i + 1
			break
		}
	}
	if bodyStart < 0 {
		// Unterminated frontmatter: treat the whole input as body.
		return strings.TrimSpace(content)
	}
	for _, line := range lines[1 : bodyStart-1] {
		key, value, ok := splitMetaLine(line)
		if !ok {
			continue
		}
		switch key {
		case "agent_id":
			c.AgentID = value
		case "agent_name":
			c.AgentName = value
		case "tool_access":
			c.ToolAccess = value
		case "team":
			c.Team = value
		}
	}
	return strings.TrimSpace(strings.Join(lines[bodyStart:], "\n"))
}

// splitMetaLine splits a "key: value" metadata line.
func splitMetaLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(line[:idx])
	value := strings.TrimSpace(line[idx+1:])
	value = strings.Trim(value, `"'`)
	return key, value, key != ""
}

// Render serializes the contract: a YAML frontmatter block with the metadata
// fields (omitted when empty) followed by the prompt body. Returns an empty
// string when the contract is empty.
func Render(c *Contract) string {
	if c.IsEmpty() {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(frontmatterDelimiter + "\n")
	writeMetaLine(&sb, "agent_id", c.AgentID)
	writeMetaLine(&sb, "agent_name", c.AgentName)
	writeMetaLine(&sb, "tool_access", c.ToolAccess)
	writeMetaLine(&sb, "team", c.Team)
	sb.WriteString(frontmatterDelimiter + "\n")
	if body := strings.TrimSpace(c.Body); body != "" {
		sb.WriteString("\n")
		sb.WriteString(body)
		sb.WriteString("\n")
	}
	return sb.String()
}

func writeMetaLine(sb *strings.Builder, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	sb.WriteString(key + ": " + YAMLQuote(value) + "\n")
}

// YAMLQuote wraps s in double quotes when it contains characters that would
// otherwise change the YAML meaning (mapping/document indicators, quotes,
// or whitespace).
func YAMLQuote(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, ":#{}[]&*!|>%@`,\"' \t") {
		return fmt.Sprintf("%q", s)
	}
	return s
}

// ComposePrompt assembles the final CLI system prompt from the contract body
// and the CLI-specific protocol headers: header, then the peer header (only
// included when team is non-empty), then the contract body.
func ComposePrompt(header, peerHeader, team, body string) string {
	var parts []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	add(header)
	if strings.TrimSpace(team) != "" {
		add(peerHeader)
	}
	add(body)
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n")
}

// SanitizeAgentID converts an agent ID into a value that is safe to use as a
// file name segment for CLI-native agent definitions (e.g. opencode agent
// markdown files). Returns "asgard_agent" when nothing usable remains.
func SanitizeAgentID(id string) string {
	var sb strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(id)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			sb.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && sb.Len() > 0 {
				sb.WriteRune('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(sb.String(), "-")
	if out == "" {
		return "asgard_agent"
	}
	return out
}

// AgentFileName returns the base file name (with extension) for a CLI-native
// agent definition derived from agentID.
func AgentFileName(agentID string) string {
	return SanitizeAgentID(agentID) + ".md"
}

// UserHomeDir returns the current user's home directory, or "" on failure.
func UserHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// HomeJoin joins home with rel when home is non-empty.
func HomeJoin(home, rel string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, rel)
}
