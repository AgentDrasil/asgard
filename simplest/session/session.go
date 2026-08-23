package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AgentDrasil/asgard/simplest/types"
)

// Entry type discriminators, matching pi's session entry union.
const (
	TypeSession             = "session"
	TypeMessage             = "message"
	TypeThinkingLevelChange = "thinking_level_change"
	TypeModelChange         = "model_change"
	TypeCompaction          = "compaction"
	TypeBranchSummary       = "branch_summary"
	TypeCustom              = "custom"
	TypeCustomMessage       = "custom_message"
	TypeLabel               = "label"
	TypeSessionInfo         = "session_info"
)

// Compaction/branch summary framing used when projecting entries into
// user messages for the LLM context (copied from pi's messages.ts).
const (
	CompactionSummaryPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"
	CompactionSummarySuffix = "\n</summary>"
	BranchSummaryPrefix     = "The following is a summary of a branch that this conversation came back from:\n\n<summary>\n"
	BranchSummarySuffix     = "</summary>"
)

// Header is the first line of a session file.
type Header struct {
	Type          string `json:"type"` // always "session"
	Version       int    `json:"version,omitempty"`
	ID            string `json:"id"`
	Timestamp     string `json:"timestamp"`
	CWD           string `json:"cwd"`
	ParentSession string `json:"parentSession,omitempty"`
}

// Entry is one session tree node. The JSONL format is a tagged union; Go gets
// a flat struct with all per-type fields optional. Unknown fields of foreign
// entries are dropped when a file is rewritten.
//
// Message, Content and Data keep their raw JSON so pi-authored files round-trip.
type Entry struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	ParentID  *string `json:"parentId"`
	Timestamp string  `json:"timestamp"`

	// TypeMessage: role-enveloped message JSON.
	Message json.RawMessage `json:"message,omitempty"`
	// TypeThinkingLevelChange.
	ThinkingLevel string `json:"thinkingLevel,omitempty"`
	// TypeModelChange.
	Provider string `json:"provider,omitempty"`
	ModelID  string `json:"modelId,omitempty"`
	// TypeCompaction / TypeBranchSummary.
	Summary          string       `json:"summary,omitempty"`
	FirstKeptEntryID string       `json:"firstKeptEntryId,omitempty"`
	TokensBefore     int64        `json:"tokensBefore,omitempty"`
	Usage            *types.Usage `json:"usage,omitempty"`
	FromHook         bool         `json:"fromHook,omitempty"`
	// TypeBranchSummary only.
	FromID string `json:"fromId,omitempty"`
	// TypeCustom / TypeCustomMessage.
	CustomType string          `json:"customType,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	Details    json.RawMessage `json:"details,omitempty"`
	// TypeCustomMessage: string or content-block array.
	Content json.RawMessage `json:"content,omitempty"`
	Display *bool           `json:"display,omitempty"`
	// TypeLabel. Label nil means "cleared".
	TargetID string  `json:"targetId,omitempty"`
	Label    *string `json:"label,omitempty"`
	// TypeSessionInfo.
	Name string `json:"name,omitempty"`
}

// DecodeMessage decodes the entry's role-enveloped message payload.
func (e *Entry) DecodeMessage() (types.Message, error) {
	if e.Type != TypeMessage {
		return nil, fmt.Errorf("session: entry %s is not a message entry", e.ID)
	}
	return types.UnmarshalMessage(e.Message)
}

// ModelRef identifies the provider/model selected by model_change entries or
// the most recent assistant message.
type ModelRef struct {
	Provider string
	ModelID  string
}

// Context is the resolved LLM-facing state of a session path.
type Context struct {
	Messages      []types.Message
	ThinkingLevel types.ThinkingLevel
	Model         *ModelRef
}

// LoadFile parses a session file. Blank and malformed lines are skipped. If
// the first parseable line is not a valid session header, the file is treated
// as empty (matching pi). A missing file yields an empty result.
func LoadFile(path string) (*Header, []*Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var header *Header
	var entries []*Entry
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var probe struct {
			Type string          `json:"type"`
			ID   json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			if header == nil {
				continue // skip leading malformed lines
			}
			continue // skip malformed lines
		}
		if header == nil {
			var id string
			if probe.Type != TypeSession || probe.ID == nil || json.Unmarshal(probe.ID, &id) != nil || id == "" {
				return nil, nil, nil
			}
			var h Header
			if err := json.Unmarshal([]byte(line), &h); err != nil {
				return nil, nil, nil
			}
			header = &h
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // skip malformed lines
		}
		if e.ID == "" {
			continue
		}
		entries = append(entries, &e)
	}
	return header, entries, nil
}

// EncodeCwd encodes a working directory into pi's safe directory name:
// --<path with separators and colons replaced by dashes>--.
func EncodeCwd(cwd string) string {
	resolved, err := filepath.Abs(cwd)
	if err != nil {
		resolved = cwd
	}
	trimmed := strings.TrimLeft(resolved, "/\\")
	replaced := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':':
			return '-'
		}
		return r
	}, trimmed)
	return "--" + replaced + "--"
}

// buildPath walks the parent chain from leaf to root and reverses it.
// A nil leafID selects the last entry; a non-nil pointer to "" yields an
// empty path (reset state).
func buildPath(entries []*Entry, byID map[string]*Entry, leafID *string) []*Entry {
	if leafID != nil && *leafID == "" {
		return nil
	}
	var leaf *Entry
	if leafID != nil {
		leaf = byID[*leafID]
	}
	if leaf == nil && len(entries) > 0 {
		leaf = entries[len(entries)-1]
	}
	var path []*Entry
	for cur := leaf; cur != nil; {
		path = append(path, cur)
		if cur.ParentID == nil {
			break
		}
		cur = byID[*cur.ParentID]
	}
	// reverse
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

func lastCompaction(path []*Entry) *Entry {
	var comp *Entry
	for _, e := range path {
		if e.Type == TypeCompaction {
			comp = e
		}
	}
	return comp
}

// BuildContextEntries returns the compaction-aware entry list along the path
// to leafID: the latest compaction entry itself, the kept entries from
// FirstKeptEntryId up to the compaction, then everything after it.
func BuildContextEntries(entries []*Entry, leafID *string) []*Entry {
	byID := indexEntries(entries)
	path := buildPath(entries, byID, leafID)
	return buildContextEntriesFromPath(path)
}

func buildContextEntriesFromPath(path []*Entry) []*Entry {
	comp := lastCompaction(path)
	if comp == nil {
		return path
	}
	compIdx := -1
	for i, e := range path {
		if e.ID == comp.ID {
			compIdx = i
			break
		}
	}
	if compIdx < 0 {
		return path
	}
	out := []*Entry{comp}
	foundFirstKept := false
	for i := 0; i < compIdx; i++ {
		e := path[i]
		if e.ID == comp.FirstKeptEntryID {
			foundFirstKept = true
		}
		if foundFirstKept {
			out = append(out, e)
		}
	}
	out = append(out, path[compIdx+1:]...)
	return out
}

func indexEntries(entries []*Entry) map[string]*Entry {
	byID := make(map[string]*Entry, len(entries))
	for _, e := range entries {
		byID[e.ID] = e
	}
	return byID
}

func timestampMillis(ts string) int64 {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

func summaryUserMessage(text, ts string) types.Message {
	blocks := []types.AssistantContent{types.TextContent{Type: types.TypeText, Text: text}}
	raw, err := types.MarshalBlocks(blocks)
	if err != nil {
		raw = types.TextOnly(text)
	}
	return &types.UserMessage{Content: raw, Timestamp: timestampMillis(ts)}
}

// SessionEntryToContextMessages projects one entry into LLM messages.
// Plain custom entries do not participate in context.
func SessionEntryToContextMessages(e *Entry) ([]types.Message, error) {
	switch e.Type {
	case TypeMessage:
		msg, err := e.DecodeMessage()
		if err != nil {
			return nil, err
		}
		return []types.Message{msg}, nil
	case TypeCustomMessage:
		content := e.Content
		if len(content) == 0 || string(content) == "null" {
			content = json.RawMessage("[]")
		}
		return []types.Message{&types.UserMessage{Content: content, Timestamp: timestampMillis(e.Timestamp)}}, nil
	case TypeBranchSummary:
		if e.Summary == "" {
			return nil, nil
		}
		return []types.Message{summaryUserMessage(BranchSummaryPrefix+e.Summary+BranchSummarySuffix, e.Timestamp)}, nil
	case TypeCompaction:
		return []types.Message{summaryUserMessage(CompactionSummaryPrefix+e.Summary+CompactionSummarySuffix, e.Timestamp)}, nil
	default:
		return nil, nil
	}
}

// GetSessionContextSettings resolves thinking level and model along the path.
func getSessionContextSettings(path []*Entry) (types.ThinkingLevel, *ModelRef) {
	thinkingLevel := types.ThinkingOff
	var model *ModelRef
	for _, e := range path {
		switch e.Type {
		case TypeThinkingLevelChange:
			thinkingLevel = types.ThinkingLevel(e.ThinkingLevel)
		case TypeModelChange:
			model = &ModelRef{Provider: e.Provider, ModelID: e.ModelID}
		case TypeMessage:
			if model != nil {
				continue
			}
			msg, err := e.DecodeMessage()
			if err != nil || msg.MessageRole() != types.RoleAssistant {
				continue
			}
			am := msg.(*types.AssistantMessage)
			model = &ModelRef{Provider: am.Provider, ModelID: am.Model}
		}
	}
	return thinkingLevel, model
}

// BuildContext resolves the LLM context (messages plus thinking/model
// settings) for the path to leafID. A nil leafID selects the current tail;
// a pointer to "" yields settings only.
func BuildContext(entries []*Entry, leafID *string) (Context, error) {
	return buildContextFromEntries(entries, indexEntries(entries), leafID)
}

func buildContextFromEntries(entries []*Entry, byID map[string]*Entry, leafID *string) (Context, error) {
	path := buildPath(entries, byID, leafID)
	thinkingLevel, model := getSessionContextSettings(path)
	selected := buildContextEntriesFromPath(path)
	messages := make([]types.Message, 0, len(selected))
	for _, e := range selected {
		msgs, err := SessionEntryToContextMessages(e)
		if err != nil {
			return Context{}, err
		}
		messages = append(messages, msgs...)
	}
	return Context{Messages: messages, ThinkingLevel: thinkingLevel, Model: model}, nil
}
