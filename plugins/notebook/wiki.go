package notebook

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// inlineTagPattern matches body hashtags like #health or #type-2-diabetes.
var inlineTagPattern = regexp.MustCompile(`#([a-zA-Z0-9_\-]+)`)

// BacklinkResult is a single wikilink reference pointing at a target note.
type BacklinkResult struct {
	Path string
	Line int
	Text string
}

// SearchResult is a single content line matching a keyword search.
type SearchResult struct {
	Path string
	Line int
	Text string
}

// ParseFrontmatter extracts the YAML frontmatter and the markdown body from
// content. When the frontmatter is absent, malformed, or not a mapping, the
// full content is returned as the body with an empty metadata map.
func ParseFrontmatter(content string) (map[string]interface{}, string) {
	if !strings.HasPrefix(content, "---") {
		return map[string]interface{}{}, content
	}
	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return map[string]interface{}{}, content
	}
	data := map[string]interface{}{}
	if err := yaml.Unmarshal([]byte(parts[1]), &data); err != nil {
		return map[string]interface{}{}, content
	}
	return data, parts[2]
}

// FindBacklinks returns every wiki note line referencing [[targetNote]],
// [[targetNote|alias]] or [[targetNote#anchor]] (case-insensitive). Paths are
// reported relative to the vault root (the parent of wikiDir).
func FindBacklinks(wikiDir, targetNote string) ([]BacklinkResult, error) {
	target := strings.TrimSuffix(strings.TrimSpace(targetNote), ".md")
	targetStem := filepath.Base(target)
	if targetStem == "" || targetStem == "." {
		return nil, fmt.Errorf("target note must not be empty")
	}
	pattern, err := regexp.Compile(`(?i)\[\[` + regexp.QuoteMeta(targetStem) + `(\|.*?)?(#.*?)?\]\]`)
	if err != nil {
		return nil, fmt.Errorf("compile backlink pattern: %w", err)
	}

	files, err := wikiMarkdownFiles(wikiDir)
	if err != nil {
		return nil, err
	}
	base := vaultRoot(wikiDir)

	var results []BacklinkResult
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for idx, line := range strings.Split(string(content), "\n") {
			if pattern.MatchString(line) {
				results = append(results, BacklinkResult{
					Path: relToSlash(base, file),
					Line: idx + 1,
					Text: strings.TrimSpace(line),
				})
			}
		}
	}
	return results, nil
}

// FindTags returns the vault-relative paths of all notes carrying the tag,
// either in the frontmatter `tags` field (list or comma-separated string) or
// as an inline #tag in the body. Matching is case-insensitive.
func FindTags(wikiDir, tagName string) ([]string, error) {
	target := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(tagName), "#"))
	if target == "" {
		return nil, fmt.Errorf("tag name must not be empty")
	}
	inlinePattern, err := regexp.Compile(`(?i)#\b` + regexp.QuoteMeta(target) + `\b`)
	if err != nil {
		return nil, fmt.Errorf("compile inline tag pattern: %w", err)
	}

	files, err := wikiMarkdownFiles(wikiDir)
	if err != nil {
		return nil, err
	}
	base := vaultRoot(wikiDir)

	var matches []string
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		frontmatter, body := ParseFrontmatter(string(content))
		matched := false
		for _, tag := range frontmatterTags(frontmatter) {
			if tag == target {
				matched = true
				break
			}
		}
		if !matched && inlinePattern.MatchString(body) {
			matched = true
		}
		if matched {
			matches = append(matches, relToSlash(base, file))
		}
	}
	return matches, nil
}

// ListAllTags counts, per unique tag, how many wiki notes carry it in their
// frontmatter or body. Tags repeated within a single note count once.
func ListAllTags(wikiDir string) (map[string]int, error) {
	files, err := wikiMarkdownFiles(wikiDir)
	if err != nil {
		return nil, err
	}

	counts := map[string]int{}
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		frontmatter, body := ParseFrontmatter(string(content))

		fileTags := map[string]struct{}{}
		for _, tag := range frontmatterTags(frontmatter) {
			if tag != "" {
				fileTags[strings.ToLower(tag)] = struct{}{}
			}
		}
		for _, match := range inlineTagPattern.FindAllStringSubmatch(body, -1) {
			fileTags[strings.ToLower(match[1])] = struct{}{}
		}
		for tag := range fileTags {
			counts[tag]++
		}
	}
	return counts, nil
}

// SearchKeywords performs a case-insensitive, line-level content search for
// any of the terms, returning every matching line with its note path.
func SearchKeywords(wikiDir string, terms []string) ([]SearchResult, error) {
	if len(terms) == 0 {
		return nil, nil
	}
	patterns := make([]*regexp.Regexp, 0, len(terms))
	for _, term := range terms {
		pattern, err := regexp.Compile(`(?i)` + regexp.QuoteMeta(term))
		if err != nil {
			return nil, fmt.Errorf("compile keyword pattern for %q: %w", term, err)
		}
		patterns = append(patterns, pattern)
	}

	files, err := wikiMarkdownFiles(wikiDir)
	if err != nil {
		return nil, err
	}
	base := vaultRoot(wikiDir)

	var results []SearchResult
	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for idx, line := range strings.Split(string(content), "\n") {
			for _, pattern := range patterns {
				if pattern.MatchString(line) {
					results = append(results, SearchResult{
						Path: relToSlash(base, file),
						Line: idx + 1,
						Text: strings.TrimSpace(line),
					})
					break
				}
			}
		}
	}
	return results, nil
}

// wikiMarkdownFiles returns the sorted list of markdown files under wikiDir.
// A missing directory yields an empty list.
func wikiMarkdownFiles(wikiDir string) ([]string, error) {
	if _, err := os.Stat(wikiDir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	err := filepath.WalkDir(wikiDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// vaultRoot returns the parent directory of wikiDir (e.g. the vault root for
// an 03_Wiki path), used as the base for reported relative paths.
func vaultRoot(wikiDir string) string {
	return filepath.Dir(filepath.Clean(wikiDir))
}

// relToSlash returns path relative to base with slash separators, falling
// back to the cleaned path when it is not under base.
func relToSlash(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return filepath.ToSlash(filepath.Clean(path))
	}
	return filepath.ToSlash(rel)
}

// frontmatterTags normalizes the `tags` field into lowercase tag strings,
// accepting a list or a comma-separated string.
func frontmatterTags(frontmatter map[string]interface{}) []string {
	raw, ok := frontmatter["tags"]
	if !ok || raw == nil {
		return nil
	}
	switch tags := raw.(type) {
	case string:
		parts := strings.Split(tags, ",")
		result := make([]string, 0, len(parts))
		for _, part := range parts {
			result = append(result, strings.ToLower(strings.TrimSpace(part)))
		}
		return result
	case []interface{}:
		result := make([]string, 0, len(tags))
		for _, tag := range tags {
			result = append(result, strings.ToLower(strings.TrimSpace(fmt.Sprint(tag))))
		}
		return result
	default:
		return nil
	}
}
