package tools

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// DetectLineEnding returns "\r\n" if the first line ending in content is CRLF.
func DetectLineEnding(content string) string {
	crlfIdx := strings.Index(content, "\r\n")
	lfIdx := strings.Index(content, "\n")
	if lfIdx == -1 || crlfIdx == -1 {
		return "\n"
	}
	if crlfIdx < lfIdx {
		return "\r\n"
	}
	return "\n"
}

// NormalizeToLF converts CRLF and bare CR line endings to LF.
func NormalizeToLF(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

// RestoreLineEndings converts LF back to the given ending.
func RestoreLineEndings(text, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}

// SplitBom strips a leading UTF-8 BOM and returns it separately.
func SplitBom(s string) (bom, text string) {
	if strings.HasPrefix(s, "\ufeff") {
		return s[:3], s[3:]
	}
	return "", s
}

// normalizeForFuzzyMatch progressively normalizes text: NFKC,
// per-line trailing-whitespace strip, smart quotes to ASCII, Unicode dashes to
// '-', and special spaces to plain space.
func normalizeForFuzzyMatch(text string) string {
	lines := strings.Split(norm.NFKC.String(text), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRightFunc(line, unicode.IsSpace)
	}
	out := strings.Join(lines, "\n")
	var b strings.Builder
	for _, r := range out {
		switch {
		case r == '\u2018' || r == '\u2019' || r == '\u201A' || r == '\u201B':
			b.WriteByte('\'')
		case r == '\u201C' || r == '\u201D' || r == '\u201E' || r == '\u201F':
			b.WriteByte('"')
		case r == '\u2010' || r == '\u2011' || r == '\u2012' || r == '\u2013' ||
			r == '\u2014' || r == '\u2015' || r == '\u2212':
			b.WriteByte('-')
		case r == '\u00A0' || (r >= '\u2002' && r <= '\u200A') ||
			r == '\u202F' || r == '\u205F' || r == '\u3000':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
