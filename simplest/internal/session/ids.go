// Package session implements a JSONL v3 session store.
//
// Sessions are append-only trees stored one JSON object per line. The first
// line is a session header; every following line is an entry with an id and
// parentId forming a tree. The "leaf" pointer tracks the current position:
// appends create children of the leaf, and branching moves the leaf to an
// earlier entry without rewriting history.
//
// Files live under <baseDir>/sessions/<encoded-cwd>/<timestamp>_<uuid>.jsonl.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
	"uuid"
)

// CurrentVersion is the JSONL session format version written by this package.
const CurrentVersion = 3

// nowISO returns the current time as an ISO-8601 millisecond UTC string.
func nowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func fileTimestamp(ts string) string {
	r := strings.NewReplacer(":", "-", ".", "-")
	return r.Replace(ts)
}

// uuidv7 generates a time-ordered RFC 9562 version-7 UUID.
func uuidv7() string {
	return uuid.NewV7().String()
}

// generateEntryID returns a unique short id (8 hex chars). Falls back to a full uuid after repeated collisions.
func generateEntryID(taken map[string]*Entry) string {
	var b [4]byte
	for i := 0; i < 100; i++ {
		if _, err := rand.Read(b[:]); err != nil {
			break
		}
		id := hex.EncodeToString(b[:])
		if _, exists := taken[id]; !exists {
			return id
		}
	}
	return uuidv7()
}
