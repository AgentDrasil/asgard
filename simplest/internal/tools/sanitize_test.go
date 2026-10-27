package tools

import (
	"bytes"
	"strings"
	"testing"
)

func sanitizeAll(t *testing.T, chunks []string) string {
	t.Helper()
	var buf bytes.Buffer
	w := newSanitizingWriter(&buf)
	for _, c := range chunks {
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	w.Flush()
	return buf.String()
}

func TestSanitizeStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		chunk string
		want  string
	}{
		{"sgr", "\x1b[31mred\x1b[0m", "red"},
		{"sgr bright", "\x1b[1;32mgreen\x1b[0m", "green"},
		{"osc title BEL", "\x1b]0;my title\x07echo hi", "echo hi"},
		{"osc hyperlink ESC \\", "\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\", "link"},
		// A raw 0x9C byte is not valid UTF-8, so like pi running over bytes
		// (TextDecoder) it decodes to U+FFFD instead of acting as ST. The
		// OSC never terminates here; the ESC introducer is instead removed
		// by the control/binary pass, not by OSC handling.
		{"osc raw 0x9C byte", "\x1b]2;t\x9ctail", "\ufffdtail"},
		{"csi with ?", "\x1b[?25lblink\x1b[?25h", "blink"},
		{"csi with ; and :", "\x1b[38;2;1:2mrgb\x1b[m", "rgb"},
		{"lone ESC removed", "a\x1bb", "ab"},
		{"no escapes", "plain text", "plain text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeAll(t, []string{tt.chunk}); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizeSplitSequences(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   string
	}{
		{"SGR split after params", []string{"\x1b[3", "1mred\x1b[0m"}, "red"},
		{"SGR split inside ESC", []string{"\x1b", "[31mred"}, "red"},
		{"SGR split inside ESC twice", []string{"a\x1b", "[3", "1mb"}, "ab"},
		{"OSC split before BEL", []string{"\x1b]0;tit", "le\x07body"}, "body"},
		{"OSC split inside ESC \\", []string{"\x1b]8;;u\x1b", "\\link"}, "link"},
		{"trailing lone ESC completed next chunk", []string{"x\x1b", "[0mok"}, "xok"},
		{"trailing lone ESC at end flushed", []string{"x\x1b"}, "x"},
		// "\x1b[31" is a complete CSI per pi's regex (params "3", final "1").
		{"CSI only, no final byte", []string{"\x1b[31"}, ""},
		// Two unterminated OSCs in one chunk: pi holds from the leftmost
		// one; the next chunk's BEL terminates both.
		{"two unterminated OSCs", []string{"\x1b]a\x1b]b", "\x07tail"}, "tail"},
		// Pending ANSI precedes the held rune tail in stream order even when
		// both are cut from the same chunk.
		{"pending ANSI + split rune order", []string{"\x1b[\xe4\xbd", "\xa0"}, "[你"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeAll(t, tt.chunks); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizeIncompleteSequenceNotHeldForever(t *testing.T) {
	// An unfinished sequence longer than pi's 256-byte holding window is
	// processed as-is instead of being held back indefinitely.
	long := "\x1b]0;" + strings.Repeat("x", 300)
	var buf bytes.Buffer
	w := newSanitizingWriter(&buf)
	if _, err := w.Write([]byte(long)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(w.pending) != 0 {
		t.Errorf("pending = %d bytes, want 0 (must not hold >%d)", len(w.pending), maxPendingANSILength)
	}
	// The over-long sequence is dropped by the control/binary pass or the
	// ANSI regex leaves only printable leftovers; nothing is held.
	w.Flush()
}

func TestSanitizeControlAndCarriageReturns(t *testing.T) {
	tests := []struct {
		name  string
		chunk string
		want  string
	}{
		{"CR removed", "a\rb", "ab"},
		{"CRLF becomes LF", "a\r\nb\n", "a\nb\n"},
		{"progress-style CR updates", "10%\r20%\r100%\ndone\n", "10%20%100%\ndone\n"},
		{"tab and newline preserved", "a\tb\nc\n", "a\tb\nc\n"},
		{"NUL and control bytes removed", "a\x00b\x01c\x02d", "abcd"},
		{"misc controls removed", "x\x08\x0B\x0C\x0E\x1Fy", "xy"},
		{"invalid UTF-8 replaced", "a\xffb", "a\ufffdb"},
		{"CJK text preserved", "こんにちは", "こんにちは"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeAll(t, []string{tt.chunk}); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizeSplitMultibyteRune(t *testing.T) {
	// A multi-byte rune split across writes must decode, not become U+FFFD.
	if got := sanitizeAll(t, []string{"h\xc3", "\xa4llo"}); got != "hällo" {
		t.Errorf("got %q, want %q", got, "hällo")
	}
	// 3-byte rune (CJK) split three ways.
	if got := sanitizeAll(t, []string{"\xe4\xbd", "\xa0"}); got != "你" {
		t.Errorf("got %q, want %q", got, "你")
	}
	// Truncated rune at end of stream flushes as U+FFFD.
	if got := sanitizeAll(t, []string{"ok\xe4\xbd"}); got != "ok\ufffd" {
		t.Errorf("got %q, want %q", got, "ok\ufffd")
	}
}

func TestSanitizeLeadingBOM(t *testing.T) {
	// Port of pi 005af57d8: TextDecoder strips exactly one leading U+FEFF
	// from the decoded stream; a BOM later in the stream is kept.
	bom := "\xef\xbb\xbf"
	tests := []struct {
		name   string
		chunks []string
		want   string
	}{
		{"leading BOM stripped", []string{bom + "hello"}, "hello"},
		{"BOM split 2+1", []string{"\xef\xbb", "\xbfhello"}, "hello"},
		{"BOM split 1+2", []string{"\xef", "\xbb\xbfhello"}, "hello"},
		{"BOM only yields empty", []string{bom}, ""},
		{"empty first write then BOM", []string{"", bom + "hi"}, "hi"},
		{"BOM after text preserved", []string{"hi" + bom + "x"}, "hi" + bom + "x"},
		{"BOM after ANSI preserved", []string{"\x1b[31m", bom + "x"}, bom + "x"},
		{"leading BOM then ANSI stripped", []string{bom + "\x1b[31mred\x1b[0m"}, "red"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := sanitizeAll(t, tt.chunks); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizeCombinedChunkWithSplitANSIAndRune(t *testing.T) {
	// Sequence split and rune split in the same write pair, with output
	// between them.
	if got := sanitizeAll(t, []string{"\x1b[3", "1m\xe4\xbd", "\xa0\x1b[0m"}); got != "你" {
		t.Errorf("got %q, want %q", got, "你")
	}
}
