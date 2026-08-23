package tools

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Edit is one oldText -> newText replacement.
type Edit struct {
	OldText string
	NewText string
}

var lineWithEndingRe = regexp.MustCompile(`[^\n]*\n|[^\n]+`)

func splitLinesWithEndings(content string) []string {
	return lineWithEndingRe.FindAllString(content, -1)
}

type lineSpan struct {
	start, end int
}

func getLineSpans(content string) []lineSpan {
	spans := make([]lineSpan, 0, 16)
	offset := 0
	for _, line := range splitLinesWithEndings(content) {
		spans = append(spans, lineSpan{start: offset, end: offset + len(line)})
		offset += len(line)
	}
	return spans
}

type textReplacement struct {
	matchIndex, matchLength int
	newText                 string
}

type fuzzyMatchResult struct {
	found          bool
	index          int
	matchLength    int
	usedFuzzy      bool
	contentForRepl string
}

// fuzzyFindText finds oldText in content, exact first, then in
// fuzzy-normalized space. When fuzzy matching is used ContentForReplacement is
// the normalized content and Index refers to it.
func fuzzyFindText(content, oldText string) fuzzyMatchResult {
	if i := strings.Index(content, oldText); i != -1 {
		return fuzzyMatchResult{found: true, index: i, matchLength: len(oldText), contentForRepl: content}
	}
	fuzzyContent := normalizeForFuzzyMatch(content)
	fuzzyOld := normalizeForFuzzyMatch(oldText)
	i := strings.Index(fuzzyContent, fuzzyOld)
	if i == -1 {
		return fuzzyMatchResult{contentForRepl: content}
	}
	return fuzzyMatchResult{found: true, index: i, matchLength: len(fuzzyOld), usedFuzzy: true, contentForRepl: fuzzyContent}
}

func countOccurrences(content, oldText string) int {
	fuzzyContent := normalizeForFuzzyMatch(content)
	fuzzyOld := normalizeForFuzzyMatch(oldText)
	return strings.Count(fuzzyContent, fuzzyOld)
}

func getReplacementLineRange(lines []lineSpan, rep textReplacement) (startLine, endLine int, err error) {
	startLine = -1
	for i, l := range lines {
		if rep.matchIndex >= l.start && rep.matchIndex < l.end {
			startLine = i
			break
		}
	}
	if startLine == -1 {
		return 0, 0, fmt.Errorf("replacement range is outside the base content")
	}
	endLine = startLine
	for endLine < len(lines) && lines[endLine].end < rep.matchIndex+rep.matchLength {
		endLine++
	}
	if endLine >= len(lines) {
		return 0, 0, fmt.Errorf("replacement range is outside the base content")
	}
	return startLine, endLine + 1, nil
}

func applyReplacementsWithOffset(content string, reps []textReplacement, offset int) string {
	sort.Slice(reps, func(i, j int) bool { return reps[i].matchIndex > reps[j].matchIndex })
	result := content
	for _, r := range reps {
		matchIndex := r.matchIndex - offset
		result = result[:matchIndex] + r.newText + result[matchIndex+r.matchLength:]
	}
	return result
}

// applyReplacementsPreservingUnchangedLines overlays replacements matched in
// baseContent onto originalContent so untouched lines keep their original bytes.
func applyReplacementsPreservingUnchangedLines(originalContent, baseContent string, reps []textReplacement) (string, error) {
	originalLines := splitLinesWithEndings(originalContent)
	baseLines := getLineSpans(baseContent)
	if len(originalLines) != len(baseLines) {
		return "", fmt.Errorf("cannot preserve unchanged lines because the base content has a different line count")
	}
	sorted := make([]textReplacement, len(reps))
	copy(sorted, reps)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].matchIndex < sorted[j].matchIndex })

	type group struct {
		startLine, endLine int
		reps               []textReplacement
	}
	var groups []group
	for _, r := range sorted {
		s, e, err := getReplacementLineRange(baseLines, r)
		if err != nil {
			return "", err
		}
		if len(groups) > 0 {
			cur := &groups[len(groups)-1]
			if s < cur.endLine {
				if e > cur.endLine {
					cur.endLine = e
				}
				cur.reps = append(cur.reps, r)
				continue
			}
		}
		groups = append(groups, group{startLine: s, endLine: e, reps: []textReplacement{r}})
	}

	var b strings.Builder
	origIdx := 0
	for _, g := range groups {
		for _, line := range originalLines[origIdx:g.startLine] {
			b.WriteString(line)
		}
		start := baseLines[g.startLine].start
		end := baseLines[g.endLine-1].end
		b.WriteString(applyReplacementsWithOffset(baseContent[start:end], g.reps, start))
		origIdx = g.endLine
	}
	for _, line := range originalLines[origIdx:] {
		b.WriteString(line)
	}
	return b.String(), nil
}

func getNotFoundError(path string, editIndex, totalEdits int) error {
	if totalEdits == 1 {
		return fmt.Errorf("could not find the exact text in %s: the old text must match exactly including all whitespace and newlines", path)
	}
	return fmt.Errorf("could not find edits[%d] in %s: the oldText must match exactly including all whitespace and newlines", editIndex, path)
}

func getDuplicateError(path string, editIndex, totalEdits, occurrences int) error {
	if totalEdits == 1 {
		return fmt.Errorf("found %d occurrences of the text in %s: the text must be unique; provide more context to make it unique", occurrences, path)
	}
	return fmt.Errorf("found %d occurrences of edits[%d] in %s: each oldText must be unique; provide more context to make it unique", occurrences, editIndex, path)
}

func getEmptyOldTextError(path string, editIndex, totalEdits int) error {
	if totalEdits == 1 {
		return fmt.Errorf("oldText must not be empty in %s", path)
	}
	return fmt.Errorf("edits[%d].oldText must not be empty in %s", editIndex, path)
}

func getNoChangeError(path string, totalEdits int) error {
	if totalEdits == 1 {
		return fmt.Errorf("no changes made to %s: the replacement produced identical content; this might indicate an issue with special characters or the text not existing as expected", path)
	}
	return fmt.Errorf("no changes made to %s: the replacements produced identical content", path)
}

type appliedEdits struct {
	baseContent, newContent string
}

// ApplyEditsToNormalizedContent matches every edit against the same original
// content and applies them in reverse offset order. If any edit needs fuzzy
// matching, matching happens in normalized space and changed lines are overlaid
// back onto the original so unchanged blocks keep their bytes.
func ApplyEditsToNormalizedContent(normalizedContent string, edits []Edit, path string) (appliedEdits, error) {
	normEdits := make([]Edit, len(edits))
	for i, e := range edits {
		normEdits[i] = Edit{OldText: NormalizeToLF(e.OldText), NewText: NormalizeToLF(e.NewText)}
	}
	for i, e := range normEdits {
		if len(e.OldText) == 0 {
			return appliedEdits{}, getEmptyOldTextError(path, i, len(normEdits))
		}
	}

	anyFuzzy := false
	for _, e := range normEdits {
		if fuzzyFindText(normalizedContent, e.OldText).usedFuzzy {
			anyFuzzy = true
			break
		}
	}
	replacementBase := normalizedContent
	if anyFuzzy {
		replacementBase = normalizeForFuzzyMatch(normalizedContent)
	}

	type mEdit struct {
		editIndex   int
		matchIndex  int
		matchLength int
		newText     string
	}
	matched := make([]mEdit, 0, len(normEdits))
	for i, e := range normEdits {
		m := fuzzyFindText(replacementBase, e.OldText)
		if !m.found {
			return appliedEdits{}, getNotFoundError(path, i, len(normEdits))
		}
		if occ := countOccurrences(replacementBase, e.OldText); occ > 1 {
			return appliedEdits{}, getDuplicateError(path, i, len(normEdits), occ)
		}
		matched = append(matched, mEdit{editIndex: i, matchIndex: m.index, matchLength: m.matchLength, newText: e.NewText})
	}

	sort.Slice(matched, func(i, j int) bool { return matched[i].matchIndex < matched[j].matchIndex })
	for i := 1; i < len(matched); i++ {
		prev, cur := matched[i-1], matched[i]
		if prev.matchIndex+prev.matchLength > cur.matchIndex {
			return appliedEdits{}, fmt.Errorf("edits[%d] and edits[%d] overlap in %s: merge them into one edit or target disjoint regions", prev.editIndex, cur.editIndex, path)
		}
	}

	reps := make([]textReplacement, len(matched))
	for i, m := range matched {
		reps[i] = textReplacement{matchIndex: m.matchIndex, matchLength: m.matchLength, newText: m.newText}
	}

	var newContent string
	var err error
	if anyFuzzy {
		newContent, err = applyReplacementsPreservingUnchangedLines(normalizedContent, replacementBase, reps)
		if err != nil {
			return appliedEdits{}, err
		}
	} else {
		newContent = applyReplacementsWithOffset(replacementBase, reps, 0)
	}
	if normalizedContent == newContent {
		return appliedEdits{}, getNoChangeError(path, len(normEdits))
	}
	return appliedEdits{baseContent: normalizedContent, newContent: newContent}, nil
}
