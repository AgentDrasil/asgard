// Package tools is a Go port of pi's seven built-in coding-agent tools
// (read, bash, edit, write, grep, find, ls) plus a tool registry with a
// JSON-Schema-subset argument validator.
package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Limits shared by the truncating tools.
const (
	// DefaultMaxLines is the default line limit for head/tail truncation.
	DefaultMaxLines = 2000
	// DefaultMaxBytes is the default byte limit (50KB) for truncation.
	DefaultMaxBytes = 50 * 1024
	// GrepMaxLineLength is the max number of characters shown per grep match line.
	GrepMaxLineLength = 500
)

// TruncationResult describes the outcome of a head or tail truncation.
type TruncationResult struct {
	Content               string `json:"content"`
	Truncated             bool   `json:"truncated"`
	TruncatedBy           string `json:"truncatedBy"` // "lines", "bytes", or ""
	TotalLines            int    `json:"totalLines"`
	TotalBytes            int    `json:"totalBytes"`
	OutputLines           int    `json:"outputLines"`
	OutputBytes           int    `json:"outputBytes"`
	LastLinePartial       bool   `json:"lastLinePartial"`
	FirstLineExceedsLimit bool   `json:"firstLineExceedsLimit"`
	MaxLines              int    `json:"maxLines"`
	MaxBytes              int    `json:"maxBytes"`
}

// TruncationOptions overrides the default limits; zero means default.
type TruncationOptions struct {
	MaxLines int
	MaxBytes int
}

// FormatSize renders a byte count as a human-readable size string.
func FormatSize(size int) string {
	if size < 1024 {
		return fmt.Sprintf("%dB", size)
	} else if size < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(size)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(size)/(1024*1024))
}

func splitLinesForCounting(content string) []string {
	if len(content) == 0 {
		return nil
	}
	lines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// TruncateHead keeps the first N complete lines/bytes of content.
func TruncateHead(content string, opts TruncationOptions) TruncationResult {
	maxLines := opts.MaxLines
	if maxLines == 0 {
		maxLines = DefaultMaxLines
	}
	maxBytes := opts.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}

	totalBytes := len(content)
	lines := splitLinesForCounting(content)
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return TruncationResult{
			Content: content, Truncated: false,
			TotalLines: totalLines, TotalBytes: totalBytes,
			OutputLines: totalLines, OutputBytes: totalBytes,
			MaxLines: maxLines, MaxBytes: maxBytes,
		}
	}

	firstLineBytes := len(lines[0])
	if firstLineBytes > maxBytes {
		return TruncationResult{
			Content: "", Truncated: true, TruncatedBy: "bytes",
			TotalLines: totalLines, TotalBytes: totalBytes,
			FirstLineExceedsLimit: true,
			MaxLines:              maxLines, MaxBytes: maxBytes,
		}
	}

	var out []string
	outBytes := 0
	truncatedBy := "lines"
	for i := 0; i < len(lines) && i < maxLines; i++ {
		lineBytes := len(lines[i])
		if i > 0 {
			lineBytes++
		}
		if outBytes+lineBytes > maxBytes {
			truncatedBy = "bytes"
			break
		}
		out = append(out, lines[i])
		outBytes += lineBytes
	}
	if len(out) >= maxLines && outBytes <= maxBytes {
		truncatedBy = "lines"
	}
	outputContent := strings.Join(out, "\n")
	return TruncationResult{
		Content: outputContent, Truncated: true, TruncatedBy: truncatedBy,
		TotalLines: totalLines, TotalBytes: totalBytes,
		OutputLines: len(out), OutputBytes: len(outputContent),
		MaxLines: maxLines, MaxBytes: maxBytes,
	}
}

// TruncateTail keeps the last N complete lines/bytes of content. It may return
// a partial first line when the final line alone exceeds the byte limit.
func TruncateTail(content string, opts TruncationOptions) TruncationResult {
	maxLines := opts.MaxLines
	if maxLines == 0 {
		maxLines = DefaultMaxLines
	}
	maxBytes := opts.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}

	totalBytes := len(content)
	lines := splitLinesForCounting(content)
	totalLines := len(lines)

	if totalLines <= maxLines && totalBytes <= maxBytes {
		return TruncationResult{
			Content: content, Truncated: false,
			TotalLines: totalLines, TotalBytes: totalBytes,
			OutputLines: totalLines, OutputBytes: totalBytes,
			MaxLines: maxLines, MaxBytes: maxBytes,
		}
	}

	var revOut []string
	outBytes := 0
	truncatedBy := "lines"
	lastLinePartial := false
	for i := len(lines) - 1; i >= 0 && len(revOut) < maxLines; i-- {
		lineBytes := len(lines[i])
		if len(revOut) > 0 {
			lineBytes++
		}
		if outBytes+lineBytes > maxBytes {
			truncatedBy = "bytes"
			if len(revOut) == 0 {
				truncated := truncateStringToBytesFromEnd(lines[i], maxBytes)
				revOut = append(revOut, truncated)
				outBytes = len(truncated)
				lastLinePartial = true
			}
			break
		}
		revOut = append(revOut, lines[i])
		outBytes += lineBytes
	}
	// Reverse back into original order.
	out := make([]string, len(revOut))
	for i, l := range revOut {
		out[len(revOut)-1-i] = l
	}
	if len(out) >= maxLines && outBytes <= maxBytes {
		truncatedBy = "lines"
	}
	outputContent := strings.Join(out, "\n")
	return TruncationResult{
		Content: outputContent, Truncated: true, TruncatedBy: truncatedBy,
		TotalLines: totalLines, TotalBytes: totalBytes,
		OutputLines: len(out), OutputBytes: len(outputContent),
		LastLinePartial: lastLinePartial,
		MaxLines:        maxLines, MaxBytes: maxBytes,
	}
}

func truncateStringToBytesFromEnd(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// truncateLine shortens a single line to maxChars runes, adding a suffix.
func truncateLine(line string, maxChars int) (string, bool) {
	if utf8.RuneCountInString(line) <= maxChars {
		return line, false
	}
	runes := []rune(line)
	return string(runes[:maxChars]) + "... [truncated]", true
}

// maxInt32 is used as "no line limit" for byte-only truncation.
const maxInt32 = int(^uint32(0) >> 1)
