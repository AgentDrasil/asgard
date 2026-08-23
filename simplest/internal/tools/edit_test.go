package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func editArgsJSON(path string, edits [][2]string) string {
	parts := make([]string, len(edits))
	for i, e := range edits {
		old, _ := json.Marshal(e[0])
		new, _ := json.Marshal(e[1])
		parts[i] = fmt.Sprintf(`{"oldText":%s,"newText":%s}`, old, new)
	}
	return fmt.Sprintf(`{"path":%q,"edits":[%s]}`, path, strings.Join(parts, ","))
}

func TestEditToolUniqueReplacement(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "f.txt", "hello world\n")
	res := mustExec(t, NewEditTool(dir), editArgsJSON("f.txt", [][2]string{{"world", "Go"}}))
	if !strings.Contains(textOf(t, res), "Successfully replaced 1 block(s)") {
		t.Errorf("unexpected output: %s", textOf(t, res))
	}
	if got := readFile(t, dir+"/f.txt"); got != "hello Go\n" {
		t.Errorf("file = %q, want %q", got, "hello Go\n")
	}
}

func TestEditToolMultipleDisjointEditsMatchOriginal(t *testing.T) {
	tests := []struct {
		name    string
		initial string
		edits   [][2]string
		want    string
	}{
		{
			name:    "two disjoint replacements",
			initial: "foo\nbar\nbaz\n",
			edits:   [][2]string{{"foo", "FOO"}, {"baz", "BAZ"}},
			want:    "FOO\nbar\nBAZ\n",
		},
		{
			name:    "matched against original not incremental result",
			initial: "start\nmiddle\nend\n",
			edits:   [][2]string{{"start", "middle"}, {"middle", "MID"}},
			want:    "middle\nMID\nend\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "f.txt", tt.initial)
			mustExec(t, NewEditTool(dir), editArgsJSON("f.txt", tt.edits))
			if got := readFile(t, dir+"/f.txt"); got != tt.want {
				t.Errorf("file = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEditToolErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "dup.txt", "dup\ndup\nend\n")
	writeFile(t, dir, "plain.txt", "alpha\nbeta\ngamma\n")

	tests := []struct {
		name    string
		file    string
		args    string
		wantSub []string
	}{
		{
			name:    "duplicate oldText",
			file:    "dup.txt",
			args:    editArgsJSON("dup.txt", [][2]string{{"dup", "X"}}),
			wantSub: []string{"found 2 occurrences", "must be unique"},
		},
		{
			name:    "not found single edit",
			file:    "plain.txt",
			args:    editArgsJSON("plain.txt", [][2]string{{"missing text", "X"}}),
			wantSub: []string{"could not find the exact text"},
		},
		{
			name:    "not found indexed among multiple edits",
			file:    "plain.txt",
			args:    editArgsJSON("plain.txt", [][2]string{{"alpha", "A"}, {"missing text", "X"}}),
			wantSub: []string{"could not find edits[1]", "the oldText must match exactly"},
		},
		{
			name:    "empty oldText single edit",
			file:    "plain.txt",
			args:    editArgsJSON("plain.txt", [][2]string{{"", "X"}}),
			wantSub: []string{"oldText must not be empty"},
		},
		{
			name:    "empty oldText among multiple edits",
			file:    "plain.txt",
			args:    editArgsJSON("plain.txt", [][2]string{{"alpha", "A"}, {"", "X"}}),
			wantSub: []string{"edits[1].oldText must not be empty"},
		},
		{
			name:    "overlapping edits",
			file:    "plain.txt",
			args:    editArgsJSON("plain.txt", [][2]string{{"alph", "X"}, {"alpha", "Y"}}),
			wantSub: []string{"overlap", "merge them into one edit"},
		},
		{
			name:    "no change produced",
			file:    "plain.txt",
			args:    editArgsJSON("plain.txt", [][2]string{{"beta", "beta"}}),
			wantSub: []string{"no changes made to plain.txt"},
		},
		{
			name:    "missing file",
			file:    "nope.txt",
			args:    editArgsJSON("nope.txt", [][2]string{{"a", "b"}}),
			wantSub: []string{"could not edit file", "no such file or directory"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := execTool(t, NewEditTool(dir), tt.args)
			if err == nil {
				t.Fatal("expected error")
			}
			for _, sub := range tt.wantSub {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q missing substring %q", err.Error(), sub)
				}
			}
		})
	}
}

func TestEditToolFuzzyMatchPreservesUnchangedLines(t *testing.T) {
	dir := t.TempDir()
	original := "alpha   \nbeta \u201Ccurly\u201D gamma\ndelta\n"
	writeFile(t, dir, "fuzzy.txt", original)

	mustExec(t, NewEditTool(dir), editArgsJSON("fuzzy.txt", [][2]string{
		{`beta "curly" gamma`, `beta REPLACED gamma`},
	}))

	want := "alpha   \nbeta REPLACED gamma\ndelta\n"
	if got := readFile(t, dir+"/fuzzy.txt"); got != want {
		t.Errorf("file = %q, want %q (trailing spaces on unchanged lines must survive)", got, want)
	}
}

func TestEditToolCRLFPreserved(t *testing.T) {
	tests := []struct{ name, initial, oldText, newText, want string }{
		{
			name:    "CRLF round trip keeps \\r\\n endings",
			initial: "one\r\ntwo\r\nthree\r\n",
			oldText: "two",
			newText: "TWO",
			want:    "one\r\nTWO\r\nthree\r\n",
		},
		{
			name:    "LF file stays LF",
			initial: "one\ntwo\nthree\n",
			oldText: "two",
			newText: "TWO",
			want:    "one\nTWO\nthree\n",
		},
		{
			name:    "BOM preserved",
			initial: "\xEF\xBB\xBFaaa\nbbb\n",
			oldText: "bbb",
			newText: "CCC",
			want:    "\xEF\xBB\xBFaaa\nCCC\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "f.txt", tt.initial)
			mustExec(t, NewEditTool(dir), editArgsJSON("f.txt", [][2]string{{tt.oldText, tt.newText}}))
			if got := readFile(t, dir+"/f.txt"); got != tt.want {
				t.Errorf("file = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeEditArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      string
		wantPath  string
		wantEdits []Edit
	}{
		{
			name:      "normal array passes through",
			args:      `{"path":"a.go","edits":[{"oldText":"x","newText":"y"}]}`,
			wantPath:  "a.go",
			wantEdits: []Edit{{OldText: "x", NewText: "y"}},
		},
		{
			name:      "edits sent as JSON string",
			args:      `{"path":"a.go","edits":"[{\"oldText\":\"x\",\"newText\":\"y\"}]"}`,
			wantPath:  "a.go",
			wantEdits: []Edit{{OldText: "x", NewText: "y"}},
		},
		{
			name:      "edits sent as single object",
			args:      `{"path":"a.go","edits":{"oldText":"x","newText":"y"}}`,
			wantPath:  "a.go",
			wantEdits: []Edit{{OldText: "x", NewText: "y"}},
		},
		{
			name:      "legacy top-level oldText/newText",
			args:      `{"path":"a.go","oldText":"x","newText":"y"}`,
			wantPath:  "a.go",
			wantEdits: []Edit{{OldText: "x", NewText: "y"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			norm, err := NormalizeEditArgs(json.RawMessage(tt.args))
			if err != nil {
				t.Fatalf("NormalizeEditArgs: %v", err)
			}
			parsed, err := parseEditArgs(norm)
			if err != nil {
				t.Fatalf("parseEditArgs(%s): %v", norm, err)
			}
			if parsed.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", parsed.Path, tt.wantPath)
			}
			if len(parsed.Edits) != len(tt.wantEdits) {
				t.Fatalf("got %d edits, want %d", len(parsed.Edits), len(tt.wantEdits))
			}
			for i, e := range tt.wantEdits {
				if parsed.Edits[i] != e {
					t.Errorf("edits[%d] = %+v, want %+v", i, parsed.Edits[i], e)
				}
			}
		})
	}
}

func TestEditToolAcceptsStringAndLegacyArgsEndToEnd(t *testing.T) {
	dir := t.TempDir()

	t.Run("edits as JSON string", func(t *testing.T) {
		writeFile(t, dir, "s.txt", "aaa bbb\n")
		args := `{"path":"s.txt","edits":"[{\"oldText\":\"bbb\",\"newText\":\"BBB\"}]"}`
		mustExec(t, NewEditTool(dir), args)
		if got := readFile(t, dir+"/s.txt"); got != "aaa BBB\n" {
			t.Errorf("file = %q", got)
		}
	})

	t.Run("legacy top-level oldText/newText", func(t *testing.T) {
		writeFile(t, dir, "l.txt", "ccc ddd\n")
		mustExec(t, NewEditTool(dir), `{"path":"l.txt","oldText":"ddd","newText":"DDD"}`)
		if got := readFile(t, dir+"/l.txt"); got != "ccc DDD\n" {
			t.Errorf("file = %q", got)
		}
	})
}

func TestEditToolDetailsPopulated(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "d.txt", "line1\nline2\nline3\n")
	res := mustExec(t, NewEditTool(dir), editArgsJSON("d.txt", [][2]string{{"line2", "LINE2"}}))

	details, ok := res.Details.(*EditToolDetails)
	if !ok {
		t.Fatalf("Details type = %T, want *EditToolDetails", res.Details)
	}
	if details.Diff == "" {
		t.Error("Diff should be populated")
	}
	if !strings.Contains(details.Diff, "-2 line2") || !strings.Contains(details.Diff, "+2 LINE2") {
		t.Errorf("Diff = %q, want +/- markers for line2", details.Diff)
	}
	if details.FirstChangedLine != 2 {
		t.Errorf("FirstChangedLine = %d, want 2", details.FirstChangedLine)
	}
	if !strings.Contains(details.Patch, "@@") {
		t.Errorf("Patch = %q, want @@ hunk header", details.Patch)
	}
	if !strings.Contains(details.Patch, "--- d.txt") || !strings.Contains(details.Patch, "+++ d.txt") {
		t.Errorf("Patch = %q, want ---/+++ headers naming the file", details.Patch)
	}
}

func TestGenerateUnifiedPatchHunkNumbers(t *testing.T) {
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprintf("l%02d", i))
	}
	oldContent := strings.Join(lines, "\n") + "\n"
	lines[11] = "CHANGED"
	newContent := strings.Join(lines, "\n") + "\n"

	patch := GenerateUnifiedPatch("f.txt", oldContent, newContent, 4)

	if !strings.HasPrefix(patch, "--- f.txt\n+++ f.txt\n") {
		t.Fatalf("patch header wrong: %q", patch[:minInt(40, len(patch))])
	}
	hunkRe := regexp.MustCompile(`(?m)^@@ -(\d+),(\d+) \+(\d+),(\d+) @@$`)
	matches := hunkRe.FindAllStringSubmatch(patch, -1)
	if len(matches) != 1 {
		t.Fatalf("expected exactly one hunk header, got %d in:\n%s", len(matches), patch)
	}
	var oldStart, oldCount, newStart, newCount int
	_, _ = fmt.Sscanf(matches[0][0], "@@ -%d,%d +%d,%d @@", &oldStart, &oldCount, &newStart, &newCount)
	// Hunk spans 4 context lines + 1 removed + 1 added + 4 context lines.
	if oldCount != 9 || newCount != 9 {
		t.Errorf("hunk counts = -%d +%d, want -9 +9", oldCount, newCount)
	}
	if oldStart < 1 || newStart < 1 {
		t.Errorf("hunk starts must be >= 1, got -%d +%d", oldStart, newStart)
	}
	body := patch[strings.Index(patch, "@@"):]
	for _, want := range []string{"-l12", "+CHANGED", " l08", " l09", " l16"} {
		if !strings.Contains(body, want) {
			t.Errorf("hunk body missing %q:\n%s", want, body)
		}
	}
}
