package session

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// headerScanLimit bounds how much of a session file readSessionHeader reads
// when discovering sessions. Only the head of the file (the first JSONL line)
// is needed; large bodies must never be parsed during directory scans.
const headerScanLimit = 1 << 20 // 1 MiB

// readSessionHeader parses only the head of a session file and returns the
// first parseable entry when it is a valid session header (type "session"
// with a non-empty id). Blank and malformed lines at the head of the file are
// skipped, mirroring LoadFile. A file whose first parseable line is not a
// session header yields (nil, nil). Reading is bounded: at most
// headerScanLimit bytes are scanned, so a header buried deeper than that (or a
// pathologically long first line) is treated as not-a-session.
//
// Mirrors pi's readSessionHeaderForDiscovery (session-manager.ts, v0.99.2):
// discovery must not parse whole session bodies.
func readSessionHeader(path string) (*Header, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	// Bound total bytes read, not just token length: a file full of short
	// malformed lines must not be scanned to EOF during discovery.
	limited := io.LimitReader(f, headerScanLimit)
	sc := bufio.NewScanner(limited)
	sc.Buffer(make([]byte, 64*1024), headerScanLimit)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var probe struct {
			Type string          `json:"type"`
			ID   json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			continue // skip malformed lines
		}
		var id string
		if probe.Type != TypeSession || probe.ID == nil || json.Unmarshal(probe.ID, &id) != nil || id == "" {
			return nil, nil // not a session file
		}
		var h Header
		if err := json.Unmarshal([]byte(line), &h); err != nil {
			return nil, nil
		}
		return &h, nil
	}
	// EOF, a read error, or a first line longer than the scan bound: no
	// verifiable header within the bound.
	return nil, nil
}

// migrateEntries upgrades legacy v1/v2 session entries to the current v3
// format in place, mirroring upstream pi's migrateToCurrentVersion
// (session-manager.ts, v0.99.2):
//
//   - v1 → v2: entries carry no ids or parentId chain; each entry gets a fresh
//     unique id, entries are chained in file order (first entry's parent is
//     nil, each following entry's parent is the previous entry's id), and v1
//     compaction "firstKeptEntryIndex" (an index that includes the header at
//     index 0) is converted to a "firstKeptEntryId" pointing at the
//     referenced entry (index 0 refers to the header and yields no target).
//   - v2 → v3: message entries whose raw role is the legacy "hookMessage" are
//     rewritten to "custom" with every other field preserved.
//
// header.Version is treated as 1 when absent. It reports whether anything
// changed; callers persist the file when it did.
func migrateEntries(header *Header, entries []*Entry) bool {
	if header == nil {
		return false
	}
	version := header.Version
	if version == 0 {
		version = 1
	}
	// The version upgrade itself counts as a change (upstream rewrites
	// whenever version < CURRENT_SESSION_VERSION), so header-only legacy
	// files are re-stamped to the current version too.
	changed := version < CurrentVersion
	if version < 2 {
		taken := make(map[string]*Entry, len(entries))
		for _, e := range entries {
			if e.ID != "" {
				taken[e.ID] = e
			}
		}
		prevID := ""
		for i, e := range entries {
			if e.ID == "" {
				e.ID = generateEntryID(taken)
				taken[e.ID] = e
				changed = true
			}
			if i == 0 {
				if e.ParentID != nil {
					e.ParentID = nil
					changed = true
				}
			} else {
				if e.ParentID == nil || *e.ParentID != prevID {
					parent := prevID
					e.ParentID = &parent
				}
				changed = true
			}
			prevID = e.ID
			if e.Type == TypeCompaction && e.FirstKeptEntryIndex != nil {
				idx := *e.FirstKeptEntryIndex
				e.FirstKeptEntryIndex = nil
				// v1 indexes include the header at index 0; index 0 refers to
				// the header itself and maps to no entry (retain-none).
				if idx >= 1 && idx <= len(entries) {
					e.FirstKeptEntryID = entries[idx-1].ID
				}
				changed = true
			}
		}
	}
	if version < 3 {
		for _, e := range entries {
			if e.Type != TypeMessage || !isHookMessageRole(e.Message) {
				continue
			}
			e.Message = rewriteMessageRole(e.Message, "custom")
			changed = true
		}
	}
	if changed {
		header.Version = CurrentVersion
	}
	return changed
}

// isHookMessageRole reports whether the enveloped message's raw role is the
// legacy "hookMessage".
func isHookMessageRole(raw json.RawMessage) bool {
	return messageRole(raw) == "hookMessage"
}

// rewriteMessageRole returns a copy of the enveloped message raw JSON with its
// "role" field replaced. All other field values are preserved, though JSON key
// order and whitespace may be normalized by the re-marshal.
func rewriteMessageRole(raw json.RawMessage, role string) json.RawMessage {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return raw
	}
	encoded, err := json.Marshal(role)
	if err != nil {
		return raw
	}
	fields["role"] = encoded
	out, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return out
}
