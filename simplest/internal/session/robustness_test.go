package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// --- permissions ---

func TestSession_Rewrite_PreservesFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are a no-op on Windows")
	}
	t.Parallel()

	dir := t.TempDir()
	m := New(dir)
	sf, err := m.Create(t.TempDir(), nil)
	require.NoError(t, err)
	_, err = sf.AppendMessage(testUser("hello"))
	require.NoError(t, err)
	require.NoError(t, sf.Close())

	info, err := os.Stat(sf.Path())
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "new session files must be 0644")

	// A later rewrite must not silently strip the mode back to 0600
	// (os.CreateTemp default). Open leaves flushed=true, so exercise
	// rewriteLocked directly to guarantee the atomic-rewrite path runs.
	require.NoError(t, os.Chmod(sf.Path(), 0o600))
	sf2, err := m.Open(sf.Path())
	require.NoError(t, err)
	sf2.mu.Lock()
	err = sf2.rewriteLocked()
	sf2.mu.Unlock()
	require.NoError(t, err)
	require.NoError(t, sf2.Close())

	info, err = os.Stat(sf2.Path())
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "existing file mode must survive atomic rewrite")

	// Reverse case: a 0640 mode is preserved too.
	require.NoError(t, os.Chmod(sf2.Path(), 0o640))
	sf3, err := m.Open(sf2.Path())
	require.NoError(t, err)
	sf3.mu.Lock()
	err = sf3.rewriteLocked()
	sf3.mu.Unlock()
	require.NoError(t, err)
	require.NoError(t, sf3.Close())
	info, err = os.Stat(sf3.Path())
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm(), "existing file mode must survive atomic rewrite")
}

// --- migration ---

const migrateV1Header = `{"type":"session","id":"v1sess","timestamp":"2025-01-01T00:00:00Z","cwd":"/proj"}`

func TestSession_MigrateV1(t *testing.T) {
	t.Parallel()

	// v1: no header version, entries without id/parentId, and a compaction
	// using the legacy firstKeptEntryIndex (the index includes the header at
	// index 0, so index 2 targets the second entry).
	path := writeSessionJSONL(t,
		migrateV1Header,
		`{"type":"message","timestamp":"2025-01-01T00:00:01Z","message":{"role":"user","content":"hello"}}`,
		`{"type":"message","timestamp":"2025-01-01T00:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"compaction","timestamp":"2025-01-01T00:00:03Z","summary":"sum","tokensBefore":100,"firstKeptEntryIndex":2}`,
	)

	m := New(t.TempDir())
	sf, err := m.Open(path)
	require.NoError(t, err)
	entries := sf.Entries()
	require.Len(t, entries, 3)

	// Fresh unique ids.
	seen := map[string]bool{}
	for _, e := range entries {
		require.NotEmpty(t, e.ID)
		require.False(t, seen[e.ID], "ids must be unique")
		seen[e.ID] = true
	}
	// Parent chain: first entry root, each next parent = previous id.
	require.Nil(t, entries[0].ParentID)
	require.NotNil(t, entries[1].ParentID)
	require.Equal(t, entries[0].ID, *entries[1].ParentID)
	require.NotNil(t, entries[2].ParentID)
	require.Equal(t, entries[1].ID, *entries[2].ParentID)

	// Compaction index conversion: index 2 → entries[1] (header at index 0).
	require.Equal(t, entries[1].ID, entries[2].FirstKeptEntryID)
	require.Nil(t, entries[2].FirstKeptEntryIndex)

	require.Equal(t, CurrentVersion, sf.Header().Version)
	require.NoError(t, sf.Close())

	// The upgrade must be durable on disk.
	header, entries2, err := LoadFile(path)
	require.NoError(t, err)
	require.NotNil(t, header)
	require.Equal(t, CurrentVersion, header.Version)
	require.Len(t, entries2, 3)
	for i := range entries2 {
		require.Equal(t, entries[i].ID, entries2[i].ID)
		require.Nil(t, entries2[i].FirstKeptEntryIndex)
	}
}

func TestSession_MigrateV2HookMessage(t *testing.T) {
	t.Parallel()

	path := writeSessionJSONL(t,
		`{"type":"session","version":2,"id":"v2sess","timestamp":"2025-01-01T00:00:00Z","cwd":"/proj"}`,
		`{"type":"message","id":"m1","parentId":null,"timestamp":"2025-01-01T00:00:01Z","message":{"role":"user","content":"hi"}}`,
		`{"type":"message","id":"m2","parentId":"m1","timestamp":"2025-01-01T00:00:02Z","message":{"role":"hookMessage","content":"hook says","customField":{"x":1}}}`,
	)

	m := New(t.TempDir())
	sf, err := m.Open(path)
	require.NoError(t, err)
	require.NoError(t, sf.Close())

	header, entries, err := LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, CurrentVersion, header.Version)
	require.Len(t, entries, 2)
	// ids/parents from the v2 file are untouched.
	require.Equal(t, "m1", entries[0].ID)
	require.Equal(t, "m2", entries[1].ID)

	// The legacy hookMessage role became custom; every other field survives.
	msg, err := entries[1].DecodeMessage()
	require.NoError(t, err)
	require.IsType(t, &types.ForeignMessage{}, msg)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(entries[1].Message, &raw))
	require.JSONEq(t, `"custom"`, string(raw["role"]))
	require.JSONEq(t, `"hook says"`, string(raw["content"]))
	require.JSONEq(t, `{"x":1}`, string(raw["customField"]))
}

func TestSession_MigrateV2VersionOnly(t *testing.T) {
	t.Parallel()

	// A v2 file with ordinary entries (no hookMessage) must still be
	// re-stamped to version 3 and persisted.
	path := writeSessionJSONL(t,
		`{"type":"session","version":2,"id":"v2plain","timestamp":"2025-01-01T00:00:00Z","cwd":"/proj"}`,
		`{"type":"message","id":"m1","parentId":null,"timestamp":"2025-01-01T00:00:01Z","message":{"role":"user","content":"hi"}}`,
	)

	m := New(t.TempDir())
	sf, err := m.Open(path)
	require.NoError(t, err)
	require.Equal(t, CurrentVersion, sf.Header().Version)
	require.NoError(t, sf.Close())

	header, entries, err := LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, CurrentVersion, header.Version, "v2 file must be persisted as v3")
	require.Len(t, entries, 1)
	require.Equal(t, "m1", entries[0].ID, "ordinary entries must be untouched")
}

func TestSession_MigrateV1HeaderOnly(t *testing.T) {
	t.Parallel()

	// A v1 file with only a header must still be upgraded to v3 on disk.
	path := writeSessionJSONL(t, migrateV1Header)

	m := New(t.TempDir())
	sf, err := m.Open(path)
	require.NoError(t, err)
	require.Equal(t, CurrentVersion, sf.Header().Version)
	require.Empty(t, sf.Entries())
	require.NoError(t, sf.Close())

	header, entries, err := LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, CurrentVersion, header.Version, "header-only v1 file must be persisted as v3")
	require.Empty(t, entries)
}

func TestSession_V3NotRewritten(t *testing.T) {
	t.Parallel()

	path := writeSessionJSONL(t,
		`{"type":"session","version":3,"id":"v3sess","timestamp":"2025-01-01T00:00:00Z","cwd":"/proj"}`,
		`{"type":"message","id":"m1","parentId":null,"timestamp":"2025-01-01T00:00:01Z","message":{"role":"user","content":"hi"}}`,
	)
	before, err := os.Stat(path)
	require.NoError(t, err)

	m := New(t.TempDir())
	sf, err := m.Open(path)
	require.NoError(t, err)
	require.NoError(t, sf.Close())

	after, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, before.ModTime(), after.ModTime(), "a v3 session must not be rewritten on Open")
}

func TestSession_MalformedFilesUnchangedByMigration(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	m := New(dir)

	// Not a session: first parseable line is not a header.
	notSession := filepath.Join(dir, "not.jsonl")
	require.NoError(t, os.WriteFile(notSession, []byte("{\"type\":\"message\",\"id\":\"x\"}\n"), 0o644))
	_, err := m.Open(notSession)
	require.Error(t, err, "non-empty non-session files must still be rejected")

	// Garbage file: LoadFile keeps treating it as empty.
	garbage := filepath.Join(dir, "garbage.jsonl")
	require.NoError(t, os.WriteFile(garbage, []byte("not json at all\n"), 0o644))
	header, entries, err := LoadFile(garbage)
	require.NoError(t, err)
	require.Nil(t, header)
	require.Empty(t, entries)
}

// --- bounded discovery ---

func TestFindMostRecent_BoundedDiscovery(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cwd := t.TempDir()

	// Valid header with a corrupt body: the old full parse skipped this file.
	good := filepath.Join(dir, "good.jsonl")
	body := `{"type":"session","version":3,"id":"good","timestamp":"2025-01-01T00:00:00Z","cwd":"` + cwd + `"}` + "\n" +
		"this line is { not json\n" +
		"\x00\xff garbage bytes\n"
	require.NoError(t, os.WriteFile(good, []byte(body), 0o644))

	path, err := FindMostRecent(dir, cwd)
	require.NoError(t, err)
	require.Equal(t, good, path, "a valid header with a corrupt body must still be discovered")

	// Non-session files are skipped.
	notSession := filepath.Join(dir, "znot.jsonl")
	require.NoError(t, os.WriteFile(notSession, []byte("{\"type\":\"message\",\"id\":\"x\"}\n"), 0o644))
	path, err = FindMostRecent(dir, cwd)
	require.NoError(t, err)
	require.Equal(t, good, path)

	// cwd filtering still works.
	other := filepath.Join(dir, "zother.jsonl")
	require.NoError(t, os.WriteFile(other,
		[]byte(`{"type":"session","version":3,"id":"other","timestamp":"2025-01-02T00:00:00Z","cwd":"/elsewhere"}`+"\n"),
		0o644))
	path, err = FindMostRecent(dir, cwd)
	require.NoError(t, err)
	require.Equal(t, good, path)
	path, err = FindMostRecent(dir, "/elsewhere")
	require.NoError(t, err)
	require.Equal(t, other, path)

	// A header beyond the scan bound (first line longer than 1 MiB) is not
	// found: discovery never reads the whole file.
	huge := filepath.Join(dir, "huge.jsonl")
	var sb strings.Builder
	sb.WriteString("{\"garbage\":\"")
	for i := 0; i < headerScanLimit; i++ {
		sb.WriteByte('x')
	}
	sb.WriteString("\"}\n")
	sb.WriteString(`{"type":"session","version":3,"id":"huge","timestamp":"2025-01-03T00:00:00Z","cwd":"` + cwd + `"}` + "\n")
	require.NoError(t, os.WriteFile(huge, []byte(sb.String()), 0o644))
	path, err = FindMostRecent(dir, cwd)
	require.NoError(t, err)
	require.Equal(t, good, path, "a header beyond the scan bound must not be discovered")

	// Total-bytes bound: many short malformed lines totaling > 1 MiB followed
	// by a valid header must also stay undiscovered (the reader is bounded,
	// not just per-line token length).
	many := filepath.Join(dir, "many.jsonl")
	var mb strings.Builder
	for i := 0; i < 25000; i++ {
		mb.WriteString("short malformed line that is not json at all\n")
	}
	mb.WriteString(`{"type":"session","version":3,"id":"many","timestamp":"2025-01-04T00:00:00Z","cwd":"` + cwd + `"}` + "\n")
	require.NoError(t, os.WriteFile(many, []byte(mb.String()), 0o644))
	require.Greater(t, len(mb.String()), headerScanLimit)
	h, err := readSessionHeader(many)
	require.NoError(t, err)
	require.Nil(t, h, "a header past the 1 MiB byte bound must not be discovered")
	path, err = FindMostRecent(dir, cwd)
	require.NoError(t, err)
	require.Equal(t, good, path, "many-small-lines file must be skipped by discovery")
}

func TestReadSessionHeader_Basics(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Missing file errors.
	_, err := readSessionHeader(filepath.Join(dir, "missing.jsonl"))
	require.Error(t, err)

	// Valid header is returned.
	path := filepath.Join(dir, "a.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(migrateV1Header+"\nrest\n"), 0o644))
	h, err := readSessionHeader(path)
	require.NoError(t, err)
	require.NotNil(t, h)
	require.Equal(t, "v1sess", h.ID)

	// Leading malformed lines are skipped.
	path = filepath.Join(dir, "b.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("junk\n\n"+migrateV1Header+"\n"), 0o644))
	h, err = readSessionHeader(path)
	require.NoError(t, err)
	require.NotNil(t, h)
	require.Equal(t, "v1sess", h.ID)

	// Not a session: nil header, nil error.
	path = filepath.Join(dir, "c.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("{\"type\":\"message\",\"id\":\"x\"}\n"), 0o644))
	h, err = readSessionHeader(path)
	require.NoError(t, err)
	require.Nil(t, h)

	// Empty file: not a session.
	path = filepath.Join(dir, "d.jsonl")
	require.NoError(t, os.WriteFile(path, nil, 0o644))
	h, err = readSessionHeader(path)
	require.NoError(t, err)
	require.Nil(t, h)

	// A huge first line (longer than the scan bound) hides the header.
	path = filepath.Join(dir, "e.jsonl")
	var sb strings.Builder
	sb.WriteString("{\"garbage\":\"")
	for i := 0; i < headerScanLimit*2; i++ {
		sb.WriteByte('x')
	}
	sb.WriteString("\"}\n")
	sb.WriteString(migrateV1Header + "\n")
	require.NoError(t, os.WriteFile(path, []byte(sb.String()), 0o644))
	h, err = readSessionHeader(path)
	require.NoError(t, err)
	require.Nil(t, h)
}

func TestMigrateEntries_NoChangeReportsFalse(t *testing.T) {
	t.Parallel()

	header := &Header{Type: TypeSession, Version: CurrentVersion, ID: "s", Timestamp: time.Now().UTC().Format(time.RFC3339)}
	entries := []*Entry{{Type: TypeMessage, ID: "m1", Message: json.RawMessage(`{"role":"user","content":"hi"}`)}}
	require.False(t, migrateEntries(header, entries))
	require.Equal(t, CurrentVersion, header.Version)
}
