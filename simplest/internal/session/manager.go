package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// Manager creates and discovers session files under an agent directory.
type Manager struct {
	BaseDir string
}

// New returns a Manager rooted at baseDir (e.g. ~/.simplest).
func New(baseDir string) *Manager {
	return &Manager{BaseDir: baseDir}
}

// DefaultBaseDir returns the simplest directory ~/.simplest.
func DefaultBaseDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".simplest")
}

// SessionsDir returns <baseDir>/sessions.
func (m *Manager) SessionsDir() string {
	return filepath.Join(m.BaseDir, "sessions")
}

// SessionDir returns (creating if needed) the encoded per-cwd directory under
// the sessions root.
func (m *Manager) SessionDir(cwd string) (string, error) {
	dir := filepath.Join(m.SessionsDir(), EncodeCwd(cwd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// CreateOptions tunes new-session creation.
type CreateOptions struct {
	// ID overrides the generated session uuid; must be non-empty and contain
	// only alphanumerics, '-', '_' and '.', starting/ending alphanumeric.
	ID string
	// ParentSession records a forked-from session path in the header.
	ParentSession string
}

// AssertValidSessionID validates caller-provided session ids.
func AssertValidSessionID(id string) error {
	if id == "" {
		return fmt.Errorf("session id must be non-empty")
	}
	if len(id) > 1 {
		mid := id[1 : len(id)-1]
		for _, r := range mid {
			ok := r == '-' || r == '_' || r == '.' ||
				(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
			if !ok {
				return fmt.Errorf("session id contains invalid character %q", r)
			}
		}
	}
	first, last := id[0], id[len(id)-1]
	alnum := func(b byte) bool {
		return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
	}
	if !alnum(first) || !alnum(last) {
		return fmt.Errorf("session id must start and end with an alphanumeric character")
	}
	return nil
}

// SessionFile is one open session: an append-only entry tree with a movable
// leaf pointer and optional JSONL persistence. All methods are safe for
// concurrent use.
type SessionFile struct {
	mu      sync.Mutex
	cwd     string
	dir     string
	path    string
	persist bool

	header       Header
	entries      []*Entry
	byID         map[string]*Entry
	labelsByID   map[string]string
	labelTsByID  map[string]string
	leafID       *string // nil = auto (last entry); &"" = reset
	hasAssistant bool
	flushed      bool
}

func newCore(cwd, id, parentSession string) *SessionFile {
	ts := nowISO()
	sf := &SessionFile{
		cwd: cwd,
		header: Header{
			Type:          TypeSession,
			Version:       CurrentVersion,
			ID:            id,
			Timestamp:     ts,
			CWD:           cwd,
			ParentSession: parentSession,
		},
		byID:        map[string]*Entry{},
		labelsByID:  map[string]string{},
		labelTsByID: map[string]string{},
	}
	return sf
}

// Create starts a new persisted session for cwd under the manager's sessions
// directory. The file is created lazily on the first assistant message
// (deferred flush).
func (m *Manager) Create(cwd string, opts *CreateOptions) (*SessionFile, error) {
	id := ""
	var parent string
	if opts != nil {
		if opts.ID != "" {
			if err := AssertValidSessionID(opts.ID); err != nil {
				return nil, err
			}
			id = opts.ID
		}
		parent = opts.ParentSession
	}
	if id == "" {
		id = uuidv7()
	}
	dir, err := m.SessionDir(cwd)
	if err != nil {
		return nil, err
	}
	sf := newCore(cwd, id, parent)
	sf.dir = dir
	sf.persist = true
	sf.path = filepath.Join(dir, fileTimestamp(sf.header.Timestamp)+"_"+id+".jsonl")
	return sf, nil
}

// InMemory starts a session that is never written to disk.
func (m *Manager) InMemory(cwd string) *SessionFile {
	return newCore(cwd, uuidv7(), "")
}

// Open loads an existing session file, or initializes a fresh session bound
// to that path when the file is missing or empty. A non-empty file whose
// first parseable line is not a valid header is rejected.
func (m *Manager) Open(path string) (*SessionFile, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	header, entries, err := LoadFile(path)
	if err != nil {
		return nil, err
	}
	if header == nil {
		if info, statErr := os.Stat(path); statErr == nil && info.Size() > 0 {
			return nil, fmt.Errorf("session: not a valid session file: %s", path)
		}
		sf := newCore(mustGetwd(), uuidv7(), "")
		sf.dir = filepath.Dir(path)
		sf.persist = true
		sf.path = path
		return sf, nil
	}
	cwd := header.CWD
	if cwd == "" {
		cwd = filepath.Dir(path)
	}
	sf := &SessionFile{
		cwd:     cwd,
		dir:     filepath.Dir(path),
		path:    path,
		persist: true,
		header:  *header,
		entries: entries,
	}
	sf.buildIndex()
	sf.flushed = true
	return sf, nil
}

// ContinueRecent opens the most recently modified session for cwd, or creates
// a new one when none exists.
func (m *Manager) ContinueRecent(cwd string) (*SessionFile, error) {
	dir, err := m.SessionDir(cwd)
	if err != nil {
		return nil, err
	}
	recent, _ := FindMostRecent(dir, cwd)
	if recent != "" {
		return m.Open(recent)
	}
	return m.Create(cwd, nil)
}

func (sf *SessionFile) buildIndex() {
	sf.byID = make(map[string]*Entry, len(sf.entries))
	sf.labelsByID = map[string]string{}
	sf.labelTsByID = map[string]string{}
	for _, e := range sf.entries {
		sf.byID[e.ID] = e
		if e.Type == TypeMessage && messageRole(e.Message) == string(types.RoleAssistant) {
			sf.hasAssistant = true
		}
		if e.Type == TypeLabel {
			if e.Label != nil && *e.Label != "" {
				sf.labelsByID[e.TargetID] = *e.Label
				sf.labelTsByID[e.TargetID] = e.Timestamp
			} else {
				delete(sf.labelsByID, e.TargetID)
				delete(sf.labelTsByID, e.TargetID)
			}
		}
	}
	if len(sf.entries) > 0 {
		last := sf.entries[len(sf.entries)-1].ID
		sf.leafID = &last
	} else {
		sf.leafID = nil
	}
}

// messageRole peeks at the role field of an enveloped message without a full
// decode; returns "" on failure.
func messageRole(raw json.RawMessage) string {
	var head types.MessageHead
	if err := json.Unmarshal(raw, &head); err != nil {
		return ""
	}
	return string(head.Role)
}

// Header returns a copy of the session header.
func (sf *SessionFile) Header() Header {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.header
}

// Path returns the backing file path ("" for in-memory sessions).
func (sf *SessionFile) Path() string { return sf.path }

// CWD returns the working directory recorded for this session.
func (sf *SessionFile) CWD() string { return sf.cwd }

// Entries returns a copy of all entries (header excluded), oldest first.
func (sf *SessionFile) Entries() []*Entry {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	out := make([]*Entry, len(sf.entries))
	copy(out, sf.entries)
	return out
}

// GetEntry looks up an entry by id.
func (sf *SessionFile) GetEntry(id string) *Entry {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.byID[id]
}

// GetLeafID returns the current leaf id and whether it is set. After ResetLeaf
// or before any append, ok is false.
func (sf *SessionFile) GetLeafID() (string, bool) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if sf.leafID == nil || *sf.leafID == "" {
		return "", false
	}
	return *sf.leafID, true
}

// AppendEntry appends a fully-formed entry as a child of the current leaf,
// filling in ID, ParentID and Timestamp when empty. Returns the entry id.
func (sf *SessionFile) AppendEntry(e *Entry) (string, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.appendAutoParentLocked(e)
}

// appendAutoParentLocked parents e to the current leaf when no parent is set.
func (sf *SessionFile) appendAutoParentLocked(e *Entry) (string, error) {
	if e.ParentID == nil {
		if sf.leafID != nil && *sf.leafID != "" {
			parent := *sf.leafID
			e.ParentID = &parent
		}
	}
	return sf.appendLocked(e)
}

func (sf *SessionFile) appendLocked(e *Entry) (string, error) {
	origID := e.ID
	origTs := e.Timestamp
	origParent := e.ParentID
	if e.ID == "" {
		e.ID = generateEntryID(sf.byID)
	}
	if e.Timestamp == "" {
		e.Timestamp = nowISO()
	}

	prevLeaf := sf.leafID
	prevHasAssistant := sf.hasAssistant
	var prevLabelVal *string
	var prevLabelTsVal string
	hadLabel := false

	if e.Type == TypeLabel {
		if l, ok := sf.labelsByID[e.TargetID]; ok {
			hadLabel = true
			lCopy := l
			prevLabelVal = &lCopy
			prevLabelTsVal = sf.labelTsByID[e.TargetID]
		}
		if e.Label != nil && *e.Label != "" {
			sf.labelsByID[e.TargetID] = *e.Label
			sf.labelTsByID[e.TargetID] = e.Timestamp
		} else {
			delete(sf.labelsByID, e.TargetID)
			delete(sf.labelTsByID, e.TargetID)
		}
	}

	sf.entries = append(sf.entries, e)
	sf.byID[e.ID] = e
	leaf := e.ID
	sf.leafID = &leaf
	if e.Type == TypeMessage && messageRole(e.Message) == string(types.RoleAssistant) {
		sf.hasAssistant = true
	}

	if err := sf.persistLocked(e); err != nil {
		// Roll back in-memory state on persistence failure.
		sf.entries = sf.entries[:len(sf.entries)-1]
		delete(sf.byID, e.ID)
		sf.leafID = prevLeaf
		sf.hasAssistant = prevHasAssistant
		if e.Type == TypeLabel {
			if hadLabel {
				sf.labelsByID[e.TargetID] = *prevLabelVal
				sf.labelTsByID[e.TargetID] = prevLabelTsVal
			} else {
				delete(sf.labelsByID, e.TargetID)
				delete(sf.labelTsByID, e.TargetID)
			}
		}
		e.ID = origID
		e.Timestamp = origTs
		e.ParentID = origParent
		return "", err
	}
	return e.ID, nil
}

// AppendMessage appends a message entry and advances the leaf. Compaction and
// branch summaries should instead use AppendCompaction / AppendBranchSummary
// so they stay first-class session entries.
func (sf *SessionFile) AppendMessage(msg types.Message) (string, error) {
	raw, err := types.MarshalMessage(msg)
	if err != nil {
		return "", err
	}
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.appendAutoParentLocked(&Entry{Type: TypeMessage, Message: raw})
}

// AppendThinkingLevelChange records a thinking-level change.
func (sf *SessionFile) AppendThinkingLevelChange(level types.ThinkingLevel) (string, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.appendAutoParentLocked(&Entry{Type: TypeThinkingLevelChange, ThinkingLevel: string(level)})
}

// AppendModelChange records a provider/model switch.
func (sf *SessionFile) AppendModelChange(provider, modelID string) (string, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.appendAutoParentLocked(&Entry{Type: TypeModelChange, Provider: provider, ModelID: modelID})
}

// AppendCompaction records a compaction covering everything before
// firstKeptEntryID.
func (sf *SessionFile) AppendCompaction(summary, firstKeptEntryID string, tokensBefore int64, usage *types.Usage, fromHook bool) (string, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.appendAutoParentLocked(&Entry{
		Type:             TypeCompaction,
		Summary:          summary,
		FirstKeptEntryID: firstKeptEntryID,
		TokensBefore:     tokensBefore,
		Usage:            usage,
		FromHook:         fromHook,
	})
}

// AppendBranchSummary starts a branch at branchFromID (nil = fresh root) and
// appends a summary of the abandoned path.
func (sf *SessionFile) AppendBranchSummary(branchFromID *string, summary string, usage *types.Usage, fromHook bool) (string, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	fromID := "root"
	if sf.leafID != nil && *sf.leafID != "" {
		fromID = *sf.leafID
	}
	e := &Entry{Type: TypeBranchSummary, FromID: fromID, Summary: summary, Usage: usage, FromHook: fromHook}
	if branchFromID != nil {
		if _, ok := sf.byID[*branchFromID]; !ok {
			return "", fmt.Errorf("session: entry %s not found", *branchFromID)
		}
		parent := *branchFromID
		e.ParentID = &parent
	}
	// Branch summaries parent explicitly (branchFromID or fresh root);
	// never inherit the current leaf.
	return sf.appendLocked(e)
}

// AppendCustom stores extension state that does not enter LLM context.
func (sf *SessionFile) AppendCustom(customType string, data json.RawMessage) (string, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.appendAutoParentLocked(&Entry{Type: TypeCustom, CustomType: customType, Data: data})
}

// AppendCustomMessage stores an extension message that DOES enter LLM context
// as a user message.
func (sf *SessionFile) AppendCustomMessage(customType string, content json.RawMessage, display bool, details json.RawMessage) (string, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.appendAutoParentLocked(&Entry{
		Type:       TypeCustomMessage,
		CustomType: customType,
		Content:    content,
		Details:    details,
		Display:    &display,
	})
}

// AppendLabel sets (or clears, with label "") a bookmark on targetID.
func (sf *SessionFile) AppendLabel(targetID, label string) (string, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	e := &Entry{Type: TypeLabel, TargetID: targetID}
	if label != "" {
		l := label
		e.Label = &l
	}
	return sf.appendAutoParentLocked(e)
}

// AppendSessionInfo records a display name for the session.
func (sf *SessionFile) AppendSessionInfo(name string) (string, error) {
	r := strings.NewReplacer("\r", " ", "\n", " ")
	name = strings.TrimSpace(r.Replace(name))
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.appendAutoParentLocked(&Entry{Type: TypeSessionInfo, Name: name})
}

// SessionName returns the latest session_info display name, if any.
func (sf *SessionFile) SessionName() string {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	for i := len(sf.entries) - 1; i >= 0; i-- {
		if sf.entries[i].Type == TypeSessionInfo {
			if sf.entries[i].Name != "" {
				return sf.entries[i].Name
			}
			return ""
		}
	}
	return ""
}

// Branch moves the leaf pointer to fromID so subsequent appends fork history.
func (sf *SessionFile) Branch(fromID string) error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if _, ok := sf.byID[fromID]; !ok {
		return fmt.Errorf("session: entry %s not found", fromID)
	}
	leaf := fromID
	sf.leafID = &leaf
	return nil
}

// ResetLeaf clears the leaf pointer so the next append starts a new root.
func (sf *SessionFile) ResetLeaf() {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	empty := ""
	sf.leafID = &empty
}

// GetBranch walks from fromID ("", meaning current leaf) to root.
func (sf *SessionFile) GetBranch(fromID string) []*Entry {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	var leaf *string
	if fromID != "" {
		leaf = &fromID
	} else if sf.leafID != nil {
		leaf = sf.leafID
	}
	return buildPath(sf.entries, sf.byID, leaf)
}

// BuildContext resolves the LLM-facing context along the current path
// (fromID "" means current leaf; use ResetLeaf semantics via "" only when
// the leaf was explicitly reset).
func (sf *SessionFile) BuildContext(fromID string) (Context, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	var leaf *string
	if fromID != "" {
		leaf = &fromID
	} else if sf.leafID != nil {
		leaf = sf.leafID
	}
	return buildContextFromEntries(sf.entries, sf.byID, leaf)
}

// Label returns the resolved label for an entry, if any.
func (sf *SessionFile) Label(id string) string {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	return sf.labelsByID[id]
}

// Flush writes any pending entries to disk immediately (normally deferred to
// the first assistant message).
func (sf *SessionFile) Flush() error {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	if !sf.persist || sf.path == "" || sf.flushed {
		return nil
	}
	return sf.rewriteLocked()
}

func (sf *SessionFile) persistLocked(entry *Entry) error {
	if !sf.persist || sf.path == "" {
		return nil
	}
	if !sf.hasAssistant {
		// Deferred: buffer in memory until the first assistant message lands.
		return nil
	}
	if !sf.flushed {
		return sf.rewriteLocked()
	}
	f, err := os.OpenFile(sf.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("session: persist %s: %w", sf.path, err)
	}
	defer func() { _ = f.Close() }()
	if err := writeEntryLine(f, entry); err != nil {
		return fmt.Errorf("session: persist %s: %w", sf.path, err)
	}
	return nil
}

func writeEntryLine(f *os.File, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = f.Write(b)
	return err
}

func (sf *SessionFile) rewriteLocked() error {
	var buf bytes.Buffer
	hb, err := json.Marshal(sf.header)
	if err != nil {
		return err
	}
	buf.Write(hb)
	buf.WriteByte('\n')
	for _, e := range sf.entries {
		eb, err := json.Marshal(e)
		if err != nil {
			return err
		}
		buf.Write(eb)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(sf.path, buf.Bytes(), 0o644); err != nil {
		return err
	}
	sf.flushed = true
	return nil
}

// FindMostRecent returns the most recently modified .jsonl session in dir,
func FindMostRecent(dir, cwd string) (string, error) {
	resolvedCwd := ""
	if cwd != "" {
		resolvedCwd, _ = filepath.Abs(cwd)
	}
	dirents, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	type candidate struct {
		path  string
		mtime int64
	}
	var best candidate
	for _, de := range dirents {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, de.Name())
		header, _, err := LoadFile(path)
		if err != nil || header == nil {
			continue
		}
		if resolvedCwd != "" {
			hcwd, _ := filepath.Abs(header.CWD)
			if header.CWD == "" || hcwd != resolvedCwd {
				continue
			}
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		if best.path == "" || info.ModTime().UnixNano() > best.mtime {
			best = candidate{path: path, mtime: info.ModTime().UnixNano()}
		}
	}
	return best.path, nil
}

// List summarizes all sessions in dir, newest first.
type SessionSummary struct {
	Path         string
	ID           string
	CWD          string
	Name         string
	CreatedAt    time.Time
	ModifiedAt   time.Time
	MessageCount int
	FirstMessage string
}

// List enumerates sessions in dir sorted by modification time (newest first).
func List(dir string) ([]SessionSummary, error) {
	dirents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []SessionSummary
	for _, de := range dirents {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, de.Name())
		header, entries, err := LoadFile(path)
		if err != nil || header == nil {
			continue
		}
		sum := SessionSummary{
			Path:      path,
			ID:        header.ID,
			CWD:       header.CWD,
			CreatedAt: parseTimeOrZero(header.Timestamp),
		}
		info, err := de.Info()
		if err == nil {
			sum.ModifiedAt = info.ModTime()
		}
		for _, e := range entries {
			switch e.Type {
			case TypeSessionInfo:
				sum.Name = e.Name
			case TypeMessage:
				sum.MessageCount++
				msg, err := e.DecodeMessage()
				if err != nil {
					continue
				}
				var text string
				switch m := msg.(type) {
				case *types.UserMessage:
					blocks, _ := types.DecodeUserContent(m.Content)
					text = types.StringContentOf(blocks)
					if sum.FirstMessage == "" && text != "" {
						sum.FirstMessage = text
					}
				default:
					continue
				}
			}
		}
		out = append(out, sum)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModifiedAt.After(out[j].ModifiedAt) })
	return out, nil
}

func parseTimeOrZero(ts string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}
	}
	return t
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}
