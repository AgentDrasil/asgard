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
	"encoding/binary"
	"encoding/hex"
	"strings"
	"time"
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
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], ((uint64(time.Now().UnixMilli()))&((1<<48)-1))<<16)
	if _, err := rand.Read(b[6:]); err != nil {
		panic("session: crypto/rand unavailable: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return formatUUID(b)
}

func formatUUID(b [16]byte) string {
	var sb strings.Builder
	sb.Grow(36)
	h := hex.EncodeToString(b[:])
	for i, c := range h {
		if i == 8 || i == 12 || i == 16 || i == 20 {
			sb.WriteByte('-')
		}
		sb.WriteRune(c)
	}
	return sb.String()
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
