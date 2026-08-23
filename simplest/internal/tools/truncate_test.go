package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFormatSize(t *testing.T) {
	tests := []struct {
		size int
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{1023, "1023B"},
		{1024, "1.0KB"},
		{2048, "2.0KB"},
		{51200, "50.0KB"},
		{1048576, "1.0MB"},
		{1572864, "1.5MB"},
	}
	for _, tt := range tests {
		if got := FormatSize(tt.size); got != tt.want {
			t.Errorf("FormatSize(%d) = %q, want %q", tt.size, got, tt.want)
		}
	}
}

func TestTruncateLine(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		maxChars  int
		wantText  string
		truncated bool
	}{
		{"short line unchanged", "hello", 10, "hello", false},
		{"exactly at limit", strings.Repeat("a", 500), 500, strings.Repeat("a", 500), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, trunc := truncateLine(tt.line, tt.maxChars)
			if got != tt.wantText || trunc != tt.truncated {
				t.Errorf("truncateLine(%q chars=%d) = (%d runes, %v), want (%q, %v)",
					tt.line[:minInt(20, len(tt.line))], tt.maxChars,
					utf8.RuneCountInString(got), trunc, tt.wantText[:minInt(20, len(tt.wantText))], tt.truncated)
			}
		})
	}

	long := strings.Repeat("x", 600)
	got, trunc := truncateLine(long, GrepMaxLineLength)
	if !trunc {
		t.Fatal("expected long line to be truncated")
	}
	if !strings.HasSuffix(got, "... [truncated]") {
		t.Errorf("truncated line should end with suffix, got suffix %q", got[len(got)-30:])
	}
	if utf8.RuneCountInString(got) != GrepMaxLineLength+len("... [truncated]") {
		t.Errorf("unexpected truncated length %d", utf8.RuneCountInString(got))
	}
	if !strings.HasPrefix(got, strings.Repeat("x", GrepMaxLineLength)) {
		t.Error("truncated line should start with the first 500 chars")
	}
}

func TestTruncateHead(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		opts  TruncationOptions
		check func(t *testing.T, r TruncationResult)
	}{
		{
			name: "under limits returns content untouched",
			in:   "a\nb\nc\n",
			opts: TruncationOptions{},
			check: func(t *testing.T, r TruncationResult) {
				if r.Truncated || r.Content != "a\nb\nc\n" {
					t.Errorf("unexpected result %+v", r)
				}
				if r.TotalLines != 3 || r.OutputLines != 3 {
					t.Errorf("line counts wrong: total=%d out=%d", r.TotalLines, r.OutputLines)
				}
				if r.MaxLines != DefaultMaxLines || r.MaxBytes != DefaultMaxBytes {
					t.Errorf("defaults not applied: maxLines=%d maxBytes=%d", r.MaxLines, r.MaxBytes)
				}
			},
		},
		{
			name: "line limit hit",
			in:   strings.Repeat("l\n", 3000),
			opts: TruncationOptions{},
			check: func(t *testing.T, r TruncationResult) {
				if !r.Truncated || r.TruncatedBy != "lines" {
					t.Errorf("expected lines truncation, got %+v", r)
				}
				if r.TotalLines != DefaultMaxLines+1000 {
					t.Errorf("TotalLines = %d, want %d", r.TotalLines, DefaultMaxLines+1000)
				}
				if r.OutputLines != DefaultMaxLines {
					t.Errorf("OutputLines = %d, want %d", r.OutputLines, DefaultMaxLines)
				}
				lines := strings.Split(strings.TrimSuffix(r.Content, "\n"), "\n")
				if len(lines) != DefaultMaxLines {
					t.Errorf("content has %d lines, want %d", len(lines), DefaultMaxLines)
				}
			},
		},
		{
			name: "byte limit hit before line limit",
			in:   strings.Repeat(strings.Repeat("a", 1000)+"\n", 100),
			opts: TruncationOptions{},
			check: func(t *testing.T, r TruncationResult) {
				if !r.Truncated || r.TruncatedBy != "bytes" {
					t.Errorf("expected bytes truncation, got %+v", r)
				}
				if r.TotalLines != 100 || r.TotalBytes != 100100 {
					t.Errorf("totals wrong: %d lines %d bytes", r.TotalLines, r.TotalBytes)
				}
				if r.OutputBytes > DefaultMaxBytes {
					t.Errorf("output exceeds byte limit: %d", r.OutputBytes)
				}
				if r.OutputLines != 51 {
					t.Errorf("OutputLines = %d, want 51", r.OutputLines)
				}
			},
		},
		{
			name: "first line exceeds byte limit",
			in:   strings.Repeat("z", 60000) + "\nsecond\n",
			opts: TruncationOptions{},
			check: func(t *testing.T, r TruncationResult) {
				if !r.Truncated || !r.FirstLineExceedsLimit {
					t.Errorf("expected FirstLineExceedsLimit, got %+v", r)
				}
				if r.TruncatedBy != "bytes" {
					t.Errorf("TruncatedBy = %q, want bytes", r.TruncatedBy)
				}
				if r.Content != "" {
					t.Errorf("Content should be empty, got %d bytes", len(r.Content))
				}
			},
		},
		{
			name: "custom limits respected",
			in:   "one\ntwo\nthree\n",
			opts: TruncationOptions{MaxLines: 2},
			check: func(t *testing.T, r TruncationResult) {
				if !r.Truncated || r.TruncatedBy != "lines" {
					t.Fatalf("expected lines truncation, got %+v", r)
				}
				if r.Content != "one\ntwo" || r.OutputLines != 2 {
					t.Errorf("unexpected output %q (%d lines)", r.Content, r.OutputLines)
				}
				if r.MaxLines != 2 {
					t.Errorf("MaxLines = %d, want 2", r.MaxLines)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.check(t, TruncateHead(tt.in, tt.opts)) })
	}
}

func TestTruncateTail(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		opts  TruncationOptions
		check func(t *testing.T, r TruncationResult)
	}{
		{
			name: "under limits returns content untouched",
			in:   "a\nb\nc",
			opts: TruncationOptions{},
			check: func(t *testing.T, r TruncationResult) {
				if r.Truncated || r.Content != "a\nb\nc" {
					t.Errorf("unexpected result %+v", r)
				}
				if r.LastLinePartial {
					t.Error("LastLinePartial should be false")
				}
			},
		},
		{
			name: "line limit keeps last lines",
			in:   strings.Repeat("l\n", 3000),
			opts: TruncationOptions{},
			check: func(t *testing.T, r TruncationResult) {
				if !r.Truncated || r.TruncatedBy != "lines" {
					t.Errorf("expected lines truncation, got %+v", r)
				}
				if r.OutputLines != DefaultMaxLines {
					t.Errorf("OutputLines = %d, want %d", r.OutputLines, DefaultMaxLines)
				}
				content := strings.Join(strings.Split(strings.TrimSuffix(r.Content, "\n"), "\n"), "\n")
				if !strings.HasPrefix(content, "l\n") || !strings.HasSuffix(content, "\nl") {
					t.Error("tail content should be a contiguous block of kept lines")
				}
			},
		},
		{
			name: "single line longer than byte limit yields partial last line",
			in:   strings.Repeat("a", 60000),
			opts: TruncationOptions{},
			check: func(t *testing.T, r TruncationResult) {
				if !r.Truncated || r.TruncatedBy != "bytes" {
					t.Errorf("expected bytes truncation, got %+v", r)
				}
				if !r.LastLinePartial {
					t.Error("LastLinePartial should be true")
				}
				if r.OutputBytes != DefaultMaxBytes {
					t.Errorf("OutputBytes = %d, want %d", r.OutputBytes, DefaultMaxBytes)
				}
				if r.Content != strings.Repeat("a", DefaultMaxBytes) {
					t.Error("partial line should keep the last maxBytes bytes")
				}
			},
		},
		{
			name: "byte limit keeps last complete lines",
			in:   strings.Repeat(strings.Repeat("b", 1000)+"\n", 100),
			opts: TruncationOptions{},
			check: func(t *testing.T, r TruncationResult) {
				if !r.Truncated || r.TruncatedBy != "bytes" {
					t.Errorf("expected bytes truncation, got %+v", r)
				}
				if r.LastLinePartial {
					t.Error("LastLinePartial should be false when last line fits")
				}
				if r.OutputBytes > DefaultMaxBytes {
					t.Errorf("output exceeds limit: %d", r.OutputBytes)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { tt.check(t, TruncateTail(tt.in, tt.opts)) })
	}
}

func TestSplitLinesForCounting(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"a\n", 1},
		{"a\nb", 2},
		{"a\nb\n", 2},
		{"\n\n", 2},
	}
	for _, tt := range tests {
		if got := len(splitLinesForCounting(tt.in)); got != tt.want {
			t.Errorf("splitLinesForCounting(%q) = %d lines, want %d", tt.in, got, tt.want)
		}
	}
}
