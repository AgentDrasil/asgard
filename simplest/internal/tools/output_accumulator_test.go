package tools

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func newTestAccumulator(maxLines, maxBytes int) *outputAccumulator {
	return newOutputAccumulator(TruncationOptions{MaxLines: maxLines, MaxBytes: maxBytes})
}

func TestOutputAccumulatorUnderLimitsNoSpill(t *testing.T) {
	acc := newTestAccumulator(0, 0)
	content := strings.Repeat("hello\n", 10)
	if _, err := acc.Write([]byte(content)); err != nil {
		t.Fatalf("write: %v", err)
	}
	tr := acc.Snapshot()
	if tr.Truncated {
		t.Errorf("Truncated = true, want false")
	}
	if tr.Content != content {
		t.Errorf("content = %q, want exact full output", tr.Content)
	}
	if tr.TotalBytes != len(content) || tr.TotalLines != 10 {
		t.Errorf("totals = (%d bytes, %d lines), want (%d, 10)", tr.TotalBytes, tr.TotalLines, len(content))
	}
	if path := acc.SpillPath(); path != "" {
		t.Errorf("SpillPath = %q, want empty (no temp file)", path)
	}
	acc.Close(false)
}

func TestOutputAccumulatorSpillsExactBytesWhenOverMaxBytes(t *testing.T) {
	acc := newTestAccumulator(0, 1024)
	var want bytes.Buffer
	chunk := bytes.Repeat([]byte("abcdefghij"), 100) // 1000 bytes
	for i := 0; i < 5; i++ {
		if _, err := acc.Write(chunk); err != nil {
			t.Fatalf("write: %v", err)
		}
		want.Write(chunk)
	}
	tr := acc.Snapshot()
	if !tr.Truncated || tr.TruncatedBy != "bytes" {
		t.Errorf("truncated=%v by=%q, want true/bytes", tr.Truncated, tr.TruncatedBy)
	}
	if tr.TotalBytes != want.Len() {
		t.Errorf("TotalBytes = %d, want %d", tr.TotalBytes, want.Len())
	}
	if len(acc.tail) > 2*1024 {
		t.Errorf("retained tail = %d bytes, want <= %d", len(acc.tail), 2*1024)
	}
	path := acc.SpillPath()
	if path == "" {
		t.Fatal("SpillPath empty, want spill file")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spill file: %v", err)
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Errorf("spill file = %d bytes, want exact %d-byte stream", len(got), want.Len())
	}
	acc.Close(false)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("spill file still present after Close: %v", err)
	}
}

func TestOutputAccumulatorMultiLineTruncation(t *testing.T) {
	acc := newTestAccumulator(0, 0)
	var want bytes.Buffer
	for i := 1; i <= 3000; i++ {
		line := []byte(strings.Repeat("x", 10) + "\n")
		want.Write(line)
		if _, err := acc.Write(line); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	tr := acc.Snapshot()
	if !tr.Truncated || tr.TruncatedBy != "lines" {
		t.Errorf("truncated=%v by=%q, want true/lines", tr.Truncated, tr.TruncatedBy)
	}
	if tr.OutputLines != DefaultMaxLines {
		t.Errorf("OutputLines = %d, want %d", tr.OutputLines, DefaultMaxLines)
	}
	if tr.TotalLines != 3000 {
		t.Errorf("TotalLines = %d, want 3000", tr.TotalLines)
	}
	path := acc.SpillPath()
	if path == "" {
		t.Fatal("SpillPath empty, want spill file")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spill file: %v", err)
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Errorf("spill file mismatch: %d bytes, want %d", len(got), want.Len())
	}
	acc.Close(false)
	_ = os.Remove(path)
}

func TestOutputAccumulatorSingleHugeLine(t *testing.T) {
	acc := newTestAccumulator(0, 1024)
	content := bytes.Repeat([]byte("z"), 5000)
	if _, err := acc.Write(content); err != nil {
		t.Fatalf("write: %v", err)
	}
	tr := acc.Snapshot()
	if !tr.Truncated || tr.TruncatedBy != "bytes" {
		t.Errorf("truncated=%v by=%q, want true/bytes", tr.Truncated, tr.TruncatedBy)
	}
	if !tr.LastLinePartial {
		t.Error("LastLinePartial = false, want true for single huge line")
	}
	if tr.TotalBytes != 5000 {
		t.Errorf("TotalBytes = %d, want 5000", tr.TotalBytes)
	}
	if tr.OutputBytes != 1024 {
		t.Errorf("OutputBytes = %d, want %d", tr.OutputBytes, 1024)
	}
	if !strings.HasSuffix(tr.Content, string(content[5000-1024:])) {
		t.Error("content should be the last maxBytes of the line")
	}
	acc.Close(false)
}

func TestOutputAccumulatorSingleHugeLineEndingInNewline(t *testing.T) {
	// A single huge line terminated by a newline: the tail starts mid-line
	// and its only newline is the final byte. Snapshot must keep (and
	// byte-truncate) that line, not drop it entirely.
	acc := newTestAccumulator(0, 1024)
	if _, err := acc.Write(bytes.Repeat([]byte("z"), 5000)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := acc.Write([]byte("\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	tr := acc.Snapshot()
	if !tr.Truncated || tr.TruncatedBy != "bytes" {
		t.Errorf("truncated=%v by=%q, want true/bytes", tr.Truncated, tr.TruncatedBy)
	}
	if tr.Content == "" {
		t.Fatal("Content is empty, want the last maxBytes of the huge line")
	}
	if len(tr.Content) != 1024 {
		t.Errorf("len(Content) = %d, want 1024", len(tr.Content))
	}
	if !tr.LastLinePartial {
		t.Error("LastLinePartial = false, want true")
	}
	if tr.OutputLines != 1 || tr.TotalLines != 1 {
		t.Errorf("lines = (output %d, total %d), want (1, 1)", tr.OutputLines, tr.TotalLines)
	}
	if tr.OutputBytes != 1024 {
		t.Errorf("OutputBytes = %d, want 1024", tr.OutputBytes)
	}
	acc.Close(false)
}

func TestOutputAccumulatorHugeLineFollowedBySmallLines(t *testing.T) {
	// A huge partial first line followed by small lines: the tail drops
	// the huge remnant, so TruncateTail sees only the small lines; the
	// snapshot must still report truncation with TruncatedBy from the
	// global counters.
	acc := newTestAccumulator(0, 1024)
	if _, err := acc.Write(bytes.Repeat([]byte("z"), 5000)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := acc.Write([]byte("\na\nb\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	tr := acc.Snapshot()
	if !tr.Truncated {
		t.Error("Truncated = false, want true")
	}
	if tr.TruncatedBy != "bytes" {
		t.Errorf("TruncatedBy = %q, want bytes", tr.TruncatedBy)
	}
	if tr.Content != "a\nb" {
		t.Errorf("Content = %q, want %q", tr.Content, "a\nb")
	}
	if tr.OutputLines != 2 {
		t.Errorf("OutputLines = %d, want 2", tr.OutputLines)
	}
	if tr.TotalLines != 3 {
		t.Errorf("TotalLines = %d, want 3", tr.TotalLines)
	}
	acc.Close(false)
}

func TestOutputAccumulatorTrailingNewlineLineCount(t *testing.T) {
	acc := newTestAccumulator(0, 0)
	// Trailing newline does not create an extra line; output not ending in
	// \n counts one open line.
	if _, err := acc.Write([]byte("a\nb\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if tr := acc.Snapshot(); tr.TotalLines != 2 {
		t.Errorf("TotalLines = %d, want 2", tr.TotalLines)
	}
	acc.Close(false)

	acc = newTestAccumulator(0, 0)
	if _, err := acc.Write([]byte("a\nb")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if tr := acc.Snapshot(); tr.TotalLines != 2 {
		t.Errorf("TotalLines = %d, want 2 (open line)", tr.TotalLines)
	}
	acc.Close(false)
}
