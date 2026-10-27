package tools

import (
	"bytes"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// This file is a port of pi's bash output sanitization
// (packages/coding-agent/src/core/bash-executor.ts with the v1.1.0
// chunk-boundary fix 27c7b6ff4, plus utils/ansi.ts and the
// sanitizeBinaryOutput helper from utils/shell.ts).

// Longest unfinished escape sequence held back across writes. Longer ones are
// processed as-is so pending state stays bounded (pi MAX_PENDING_ANSI_LENGTH).
const maxPendingANSILength = 256

// ansiSequenceRe matches complete ANSI sequences: OSC (ESC ] ... BEL|ESC \|0x9C)
// and CSI-style sequences with optional ?/;/: params (pi ansiRegex).
var ansiSequenceRe = regexp.MustCompile(
	`\x1b\][\s\S]*?(?:\x07|\x1b\\|\x9c)|[\x{009b}\x{001b}][[\]()#;?]*(?:\d{1,4}(?:[;:]\d{0,4})*)?[\dA-PR-TZcf-nq-uy=><~]`)

// controlRe removes binary/control garbage and carriage returns (pi
// sanitizeBinaryOutput plus the \r drop in bash-executor). Tab and newline
// are preserved.
var controlRe = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F\r\x{FFF9}-\x{FFFB}]`)

// utf8BOM is the UTF-8 encoding of U+FEFF.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Port of pi 005af57d8: TextDecoder strips one leading BOM; i.e. exactly one
// leading U+FEFF at the very start of the decoded stream is removed, before
// ANSI/control sanitization. A BOM after any other decoded byte (text or an
// ANSI escape) is kept, and a BOM split across writes is still recognized.

// sanitizingWriter is an io.Writer that strips ANSI escape sequences, control
// bytes, and carriage returns from command output before it reaches the
// accumulator, handling escape sequences and UTF-8 runes split across writes.
type sanitizingWriter struct {
	dst     io.Writer
	pending []byte // held-back incomplete UTF-8 tail + incomplete ANSI sequence
	started bool   // whether any non-BOM bytes have been decoded from the stream
}

func newSanitizingWriter(dst io.Writer) *sanitizingWriter {
	return &sanitizingWriter{dst: dst}
}

// Write sanitizes one output chunk. It must only be called from the single
// goroutine draining the command's combined output pipe.
func (w *sanitizingWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	// Copy: pending must not alias buf, which is rebuilt below.
	buf := append(make([]byte, 0, len(w.pending)+len(p)), w.pending...)
	buf = append(buf, p...)
	w.pending = w.pending[:0]

	// Strip a single leading BOM from the first decoded bytes. A combined
	// buffer that is still a proper BOM prefix (held back below as an
	// incomplete rune) waits for the next write. Once anything other than a
	// BOM prefix has been decoded, the stream is "started" and BOMs are kept.
	if !w.started {
		if bytes.HasPrefix(buf, utf8BOM) {
			buf = buf[len(utf8BOM):]
			w.started = true
		} else if !bytes.HasPrefix(utf8BOM, buf) {
			w.started = true
		}
	}

	// Streaming UTF-8 decode: hold back a trailing incomplete rune, decode
	// invalid bytes as U+FFFD (TextDecoder semantics).
	runeTail := []byte{}
	if n := incompleteRuneTail(buf); n > 0 {
		runeTail = buf[len(buf)-n:]
		buf = buf[:len(buf)-n]
	}
	complete, pendingAnsi := splitIncompleteANSI(decodeRunes(buf))
	// Stream order is [ANSI prefix][rune tail]: the incomplete sequence was
	// cut from the prefix that precedes the rune tail.
	w.pending = append(append(w.pending, pendingAnsi...), runeTail...)

	_, err := io.WriteString(w.dst, sanitizeANSI(complete))
	return len(p), err
}

// Flush drains any held-back bytes/escape sequence. It must be called once
// the command has ended (including timeout/abort/non-zero exit) so output
// produced before termination is still returned.
func (w *sanitizingWriter) Flush() {
	if len(w.pending) == 0 {
		return
	}
	pending := w.pending
	w.pending = nil
	// A trailing incomplete rune gets a single U+FFFD (TextDecoder final
	// flush), not one per byte.
	if n := incompleteRuneTail(pending); n > 0 {
		pending = pending[:len(pending)-n]
		decoded := decodeRunes(pending)
		decoded += string(utf8.RuneError)
		_, _ = io.WriteString(w.dst, sanitizeANSI(decoded))
		return
	}
	_, _ = io.WriteString(w.dst, sanitizeANSI(decodeRunes(pending)))
}

// decodeRunes decodes b, replacing each invalid byte with U+FFFD
// (TextDecoder semantics for streamed input).
func decodeRunes(b []byte) string {
	var sb strings.Builder
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			sb.WriteRune(utf8.RuneError)
		} else {
			sb.Write(b[i : i+size])
		}
		i += size
	}
	return sb.String()
}

// sanitizeANSI strips complete ANSI sequences, control/binary garbage, and
// carriage returns from a chunk that contains no incomplete trailing sequence.
func sanitizeANSI(s string) string {
	return controlRe.ReplaceAllLiteralString(ansiSequenceRe.ReplaceAllLiteralString(s, ""), "")
}

// splitIncompleteANSI splits streamed text into the part that is safe to
// sanitize now and a trailing unfinished OSC/CSI sequence (possibly just a
// lone ESC) that must be prepended to the next chunk.
//
// Port of pi 27c7b6ff4: pi detects the unfinished suffix with a
// negative-lookahead regex, which Go's RE2 cannot express; this scanner walks
// the tail of the chunk instead. Anything longer than maxPendingANSILength is
// not held back, so pending state stays bounded.
func splitIncompleteANSI(s string) (complete, pending string) {
	if !strings.ContainsRune(s, 0x1B) && !strings.ContainsRune(s, 0x9B) {
		return s, ""
	}

	n := len(s)
	split := -1

	// Unfinished OSC: like pi, hold from the leftmost ESC ] that has no
	// terminator (BEL, ESC \, or 0x9C) between it and the end of the chunk.
	// A terminator closes every earlier candidate, so scanning forward and
	// resetting on each terminator leaves oscStart at that leftmost start.
	oscStart := -1
	for i := 0; i < n; {
		switch {
		case s[i] == 0x07 || s[i] == 0x9C:
			oscStart = -1
			i++
		case i+1 < n && s[i] == 0x1B && s[i+1] == ']':
			// Only the leftmost start after the last terminator matters;
			// a later ESC ] inside the body is just body content.
			if oscStart < 0 {
				oscStart = i
			}
			i += 2
		case i+1 < n && s[i] == 0x1B && s[i+1] == '\\':
			oscStart = -1
			i += 2
		case s[i] == 0x1B:
			// ESC not followed by \ is part of an OSC body (pi's
			// \x1B(?!\\) alternative); skip the next byte with it.
			i += 2
		default:
			i++
		}
	}
	if oscStart >= 0 {
		split = oscStart
	}

	// Unfinished CSI (or a lone ESC): ESC|0x9B, optional intermediates and
	// digit/;/: params, but no final byte before the end.
	csiStart := trailingCSIStart(s)
	if csiStart >= 0 && (split < 0 || csiStart < split) {
		split = csiStart
	}

	if split < 0 {
		return s, ""
	}
	if n-split > maxPendingANSILength {
		return s, "" // longer than pi's holding window: process as-is
	}
	return s[:split], s[split:]
}

// trailingCSIStart returns the index where a trailing, still-unfinished CSI
// sequence begins, or -1. It consumes trailing params (digits/;/:), then
// intermediate bytes ([\]()#;?), then requires an ESC/0x9B introducer.
func trailingCSIStart(s string) int {
	n := len(s)
	i := n
	for i > 0 && (s[i-1] >= '0' && s[i-1] <= '9' || s[i-1] == ';' || s[i-1] == ':') {
		i--
	}
	params := s[i:n]
	if !validCSIParams(params) {
		return -1
	}
	for i > 0 && strings.IndexByte("[]()#;?", s[i-1]) >= 0 {
		i--
	}
	if i == 0 || (s[i-1] != 0x1B && s[i-1] != 0x9B) {
		return -1
	}
	return i - 1
}

// validCSIParams reports whether params matches pi's
// \d{1,4}([;:]\d{0,4})* parameter grammar (empty is fine too).
func validCSIParams(params string) bool {
	i := 0
	digits := 0
	for i < len(params) && params[i] >= '0' && params[i] <= '9' {
		i++
		digits++
	}
	if digits > 4 {
		return false
	}
	for i < len(params) {
		if params[i] != ';' && params[i] != ':' {
			return false
		}
		i++
		digits = 0
		for i < len(params) && params[i] >= '0' && params[i] <= '9' {
			i++
			digits++
		}
		if digits > 4 {
			return false
		}
	}
	return true
}

// incompleteRuneTail returns the number of trailing bytes of b that form a
// valid prefix of a multi-byte UTF-8 sequence (1-3 bytes), so a rune split
// across os/exec write chunks is held back instead of decoding early.
func incompleteRuneTail(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-3; i-- {
		c := b[i]
		var size int
		switch {
		case c >= 0xC2 && c <= 0xDF:
			size = 2
		case c >= 0xE0 && c <= 0xEF:
			size = 3
		case c >= 0xF0 && c <= 0xF4:
			size = 4
		default:
			continue // ASCII or stray continuation byte: keep scanning back
		}
		if len(b)-i < size && isContinuation(b[i+1:]) {
			return len(b) - i
		}
		return 0 // complete (or invalid) sequence start: nothing to hold
	}
	return 0
}

func isContinuation(b []byte) bool {
	for _, c := range b {
		if c&0xC0 != 0x80 {
			return false
		}
	}
	return true
}
