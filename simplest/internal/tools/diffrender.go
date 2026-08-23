package tools

import (
	"fmt"
	"strings"
)

// diffPart is one contiguous block of added, removed, or equal lines.
type diffPart struct {
	lines          []string
	added, removed bool
}

// maxMyersDistance bounds the Myers search. Beyond this edit distance the
// memory/trace cost outweighs diff quality and we emit one big remove/add block.
const maxMyersDistance = 1024

// myersMatches returns the ascending (oldIdx, newIdx) pairs of equal lines
// using Myers O(ND) algorithm (as jsdiff does), or nil when the edit distance
// exceeds maxMyersDistance.
func myersMatches(a, b []string) [][2]int {
	n, m := len(a), len(b)
	p := 0
	for p < n && p < m && a[p] == b[p] {
		p++
	}
	s := 0
	for s < n-p && s < m-p && a[n-1-s] == b[m-1-s] {
		s++
	}
	ca, cb := a[p:n-s], b[p:m-s]

	var matches [][2]int
	for i := 0; i < p; i++ {
		matches = append(matches, [2]int{i, i})
	}

	cn, cm := len(ca), len(cb)
	if cn+cm == 0 {
		for i := 0; i < s; i++ {
			matches = append(matches, [2]int{n - s + i, m - s + i})
		}
		return matches
	}
	if cn+cm > maxMyersDistance {
		return nil
	}

	offset := cn + cm
	vsize := 2*(cn+cm) + 1
	v := make([]int, vsize)
	var trace [][]int
	dFinal := -1

search:
	for d := 0; d <= cn+cm; d++ {
		trace = append(trace, append([]int(nil), v...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < cn && y < cm && ca[x] == cb[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= cn && y >= cm {
				dFinal = d
				break search
			}
		}
	}

	if dFinal < 0 || dFinal > maxMyersDistance {
		return nil
	}

	type pt struct{ x, y int }
	cur := pt{cn, cm}
	var rev [][2]int
	for d := dFinal; d >= 1; d-- {
		vd := trace[d]
		k := cur.x - cur.y
		var prevK int
		if k == -d || (k != d && vd[offset+k-1] < vd[offset+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := vd[offset+prevK]
		prevY := prevX - prevK
		for cur.x > prevX && cur.y > prevY {
			cur.x--
			cur.y--
			rev = append(rev, [2]int{cur.x + p, cur.y + p})
		}
		if cur.x == prevX {
			cur.y--
		} else {
			cur.x--
		}
	}
	for cur.x > 0 && cur.y > 0 {
		cur.x--
		cur.y--
		rev = append(rev, [2]int{cur.x + p, cur.y + p})
	}
	for i := len(rev) - 1; i >= 0; i-- {
		matches = append(matches, rev[i])
	}
	for i := 0; i < s; i++ {
		matches = append(matches, [2]int{n - s + i, m - s + i})
	}
	return matches
}

// diffLines computes a line-level Myers diff between two LF-terminated texts.
// When the edit distance is too large it falls back to a single remove/add
// block instead of allocating an O(n×m) matrix.
func diffLines(oldContent, newContent string) []diffPart {
	oldLines := splitLinesWithEndings(oldContent)
	newLines := splitLinesWithEndings(newContent)

	var parts []diffPart
	appendLine := func(line string, added, removed bool) {
		if len(parts) > 0 {
			last := &parts[len(parts)-1]
			if last.added == added && last.removed == removed {
				last.lines = append(last.lines, line)
				return
			}
		}
		parts = append(parts, diffPart{lines: []string{line}, added: added, removed: removed})
	}

	matches := myersMatches(oldLines, newLines)
	if matches == nil {
		for _, l := range oldLines {
			appendLine(l, false, true)
		}
		for _, l := range newLines {
			appendLine(l, true, false)
		}
		return parts
	}
	mi := 0
	for i, j := 0, 0; i < len(oldLines) || j < len(newLines); {
		if mi < len(matches) && matches[mi] == [2]int{i, j} {
			appendLine(oldLines[i], false, false)
			i++
			j++
			mi++
			continue
		}
		nextI, nextJ := len(oldLines), len(newLines)
		if mi < len(matches) {
			nextI, nextJ = matches[mi][0], matches[mi][1]
		}
		for i < nextI {
			appendLine(oldLines[i], false, true)
			i++
		}
		for j < nextJ {
			appendLine(newLines[j], true, false)
			j++
		}
	}
	return parts
}

type diffStringResult struct {
	diff             string
	firstChangedLine int // -1 when unchanged
}

// GenerateDiffString renders a display-oriented diff with line numbers and
// limited context with line numbers.
func GenerateDiffString(oldContent, newContent string, contextLines int) diffStringResult {
	if contextLines <= 0 {
		contextLines = 4
	}
	parts := diffLines(oldContent, newContent)
	var output []string

	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")
	maxLineNum := len(oldLines)
	if len(newLines) > maxLineNum {
		maxLineNum = len(newLines)
	}
	lineNumWidth := len(fmt.Sprint(maxLineNum))

	oldLineNum, newLineNum := 1, 1
	lastWasChange := false
	firstChangedLine := -1

	pad := func(n int) string {
		s := fmt.Sprint(n)
		return strings.Repeat(" ", lineNumWidth-len(s)) + s
	}

	for i := 0; i < len(parts); i++ {
		part := parts[i]
		raw := append([]string{}, part.lines...)
		if len(raw) > 0 && strings.HasSuffix(raw[len(raw)-1], "\n") {
			raw[len(raw)-1] = strings.TrimSuffix(raw[len(raw)-1], "\n")
		}
		if len(raw) == 1 && raw[0] == "" {
			raw = nil
		}

		if part.added || part.removed {
			if firstChangedLine == -1 {
				firstChangedLine = newLineNum
			}
			for _, line := range raw {
				if part.added {
					output = append(output, fmt.Sprintf("+%s %s", pad(newLineNum), line))
					newLineNum++
				} else {
					output = append(output, fmt.Sprintf("-%s %s", pad(oldLineNum), line))
					oldLineNum++
				}
			}
			lastWasChange = true
			continue
		}

		nextPartIsChange := i < len(parts)-1 && (parts[i+1].added || parts[i+1].removed)
		hasLeadingChange := lastWasChange
		hasTrailingChange := nextPartIsChange

		emitCtx := func(lines []string) {
			for _, line := range lines {
				output = append(output, fmt.Sprintf(" %s %s", pad(oldLineNum), line))
				oldLineNum++
				newLineNum++
			}
		}

		switch {
		case hasLeadingChange && hasTrailingChange:
			if len(raw) <= contextLines*2 {
				emitCtx(raw)
			} else {
				emitCtx(raw[:contextLines])
				skipped := len(raw) - contextLines*2
				output = append(output, fmt.Sprintf(" %s ...", strings.Repeat(" ", lineNumWidth)))
				oldLineNum += skipped
				newLineNum += skipped
				emitCtx(raw[len(raw)-contextLines:])
			}
		case hasLeadingChange:
			shown := raw
			if len(shown) > contextLines {
				shown = shown[:contextLines]
			}
			emitCtx(shown)
			if skipped := len(raw) - len(shown); skipped > 0 {
				output = append(output, fmt.Sprintf(" %s ...", strings.Repeat(" ", lineNumWidth)))
				oldLineNum += skipped
				newLineNum += skipped
			}
		case hasTrailingChange:
			skipped := len(raw) - contextLines
			if skipped > 0 {
				output = append(output, fmt.Sprintf(" %s ...", strings.Repeat(" ", lineNumWidth)))
				oldLineNum += skipped
				newLineNum += skipped
			} else {
				skipped = 0
			}
			emitCtx(raw[skipped:])
		default:
			oldLineNum += len(raw)
			newLineNum += len(raw)
		}
		lastWasChange = false
	}
	return diffStringResult{diff: strings.Join(output, "\n"), firstChangedLine: firstChangedLine}
}

// GenerateUnifiedPatch renders a standard unified patch with hunk headers,
// mirroring Diff.createTwoFilesPatch output shape.
func GenerateUnifiedPatch(path, oldContent, newContent string, contextLines int) string {
	if contextLines <= 0 {
		contextLines = 4
	}
	parts := diffLines(oldContent, newContent)

	type taggedLine struct {
		tag  byte // ' ', '-', '+'
		text string
	}
	var lines []taggedLine
	for _, part := range parts {
		tag := byte(' ')
		if part.added {
			tag = '+'
		} else if part.removed {
			tag = '-'
		}
		for _, l := range part.lines {
			lines = append(lines, taggedLine{tag: tag, text: strings.TrimSuffix(l, "\n")})
		}
	}

	type rng struct{ start, end int }
	var groups []rng
	for i, l := range lines {
		if l.tag == ' ' {
			continue
		}
		if len(groups) > 0 && i <= groups[len(groups)-1].end+contextLines*2 {
			groups[len(groups)-1].end = i
		} else {
			groups = append(groups, rng{start: i, end: i})
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", path, path)
	for _, g := range groups {
		start := maxInt(0, g.start-contextLines)
		end := minInt(len(lines)-1, g.end+contextLines)
		oldStart, oldCount, newStart, newCount := 0, 0, 0, 0
		oldNo, newNo := 1, 1
		for i := start; i <= end; i++ {
			if i == start {
				oldStart = oldNo
				newStart = newNo
			}
			switch lines[i].tag {
			case '-':
				oldCount++
				oldNo++
			case '+':
				newCount++
				newNo++
			default:
				oldCount++
				newCount++
				oldNo++
				newNo++
			}
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for i := start; i <= end; i++ {
			b.WriteByte(lines[i].tag)
			b.WriteString(lines[i].text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
