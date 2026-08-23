package tools

import (
	"fmt"
	"regexp"
	"strings"
)

// globToRegexp compiles a gitignore-style glob body (no anchoring, no
// negation) into a regexp matching slash-separated relative paths.
func globToRegexp(body string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	runes := []rune(body)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch c {
		case '*':
			if i+1 < len(runes) && runes[i+1] == '*' {
				if i+2 < len(runes) && runes[i+2] == '/' {
					b.WriteString("(?:.*/)?")
					i += 2
				} else {
					b.WriteString(".*")
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			j := i + 1
			neg := false
			if j < len(runes) && (runes[j] == '!' || runes[j] == '^') {
				neg = true
				j++
			}
			start := j
			for j < len(runes) && runes[j] != ']' {
				j++
			}
			if j >= len(runes) || j == start {
				b.WriteString(regexp.QuoteMeta(string(c)))
				continue
			}
			class := string(runes[start:j])
			if neg {
				class = "^" + class
			}
			b.WriteString("[" + class + "]")
			i = j
		case '\\':
			if i+1 < len(runes) {
				b.WriteString(regexp.QuoteMeta(string(runes[i+1])))
				i++
			} else {
				b.WriteString(regexp.QuoteMeta("\\"))
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// matchGlob reports whether a gitignore-style glob matches target.
// Patterns containing "/" are anchored: they match the full slash-separated
// path. Patterns without "/" match any single path segment (basename), unless
// forceFull is true in which case they must match the whole path.
func matchGlob(pattern, target string, forceFull bool) (bool, error) {
	pattern = strings.TrimSuffix(pattern, "/")
	if pattern == "" {
		return false, nil
	}
	anchored := strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "/")
	if anchored && strings.HasPrefix(pattern, "/") {
		pattern = strings.TrimPrefix(pattern, "/")
	}
	re, err := globToRegexp(pattern)
	if err != nil {
		return false, fmt.Errorf("invalid glob %q: %w", pattern, err)
	}
	if anchored || forceFull {
		return re.MatchString(target), nil
	}
	if idx := strings.LastIndex(target, "/"); idx >= 0 {
		target = target[idx+1:]
	}
	return re.MatchString(target), nil
}
