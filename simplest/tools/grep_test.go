package tools

import (
	"strings"
	"testing"

	"github.com/AgentDrasil/asgard/simplest/types"
)

func grepSetup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "code.go", "package main\nfunc needleHere() {}\n// another needle\n")
	writeFile(t, dir, "notes.txt", "a.c regex\nabc also\nnothing here\n")
	writeFile(t, dir, "longline.txt", strings.Repeat("x", 300)+"NEEDLE"+strings.Repeat("y", 400)+"\n")
	writeFile(t, dir, "skipdir/secret.txt", "needle in ignored dir\n")
	writeFile(t, dir, ".gitignore", "skipdir/\n")
	return dir
}

func grepExec(t *testing.T, dir, args string) (*types.ToolResult, string) {
	t.Helper()
	res := mustExec(t, NewGrepTool(dir), args)
	return res, textOf(t, res)
}

func TestGrepToolRegexMatchFormat(t *testing.T) {
	dir := grepSetup(t)
	res, out := grepExec(t, dir, `{"pattern":"needle","path":"."}`)
	for _, want := range []string{"code.go:2: func needleHere() {}", "code.go:3: // another needle"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "skipdir/secret.txt") {
		t.Errorf("gitignored file should not be searched:\n%s", out)
	}
	details := res.Details.(*GrepToolDetails)
	if details.LinesTruncated || details.MatchLimitReached != 0 {
		t.Errorf("unexpected details %+v", details)
	}
}

func TestGrepToolLiteralMode(t *testing.T) {
	dir := grepSetup(t)
	_, out := grepExec(t, dir, `{"pattern":"a.c","literal":true,"path":"notes.txt"}`)
	if !strings.Contains(out, "notes.txt:1: a.c regex") {
		t.Errorf("literal match missing:\n%s", out)
	}
	if strings.Contains(out, "abc") {
		t.Errorf("literal mode must not match regex expansion of a.c:\n%s", out)
	}

	_, out = grepExec(t, dir, `{"pattern":"a\\.c","path":"notes.txt"}`)
	if strings.Contains(out, "abc") {
		t.Errorf("regex mode should not match abc for a\\.c:\n%s", out)
	}
	if !strings.Contains(out, "notes.txt:1:") {
		t.Errorf("regex mode should still match literal dot via escape:\n%s", out)
	}
}

func TestGrepToolIgnoreCase(t *testing.T) {
	dir := grepSetup(t)
	tests := []struct {
		name      string
		args      string
		wantMatch bool
	}{
		{"case sensitive misses NEEDLE", `{"pattern":"needle","path":"longline.txt"}`, false},
		{"ignoreCase hits NEEDLE", `{"pattern":"NEEDLE","ignoreCase":true,"path":"longline.txt"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, out := grepExec(t, dir, tt.args)
			has := strings.Contains(out, "NEEDLE") || strings.Contains(out, "No matches found")
			if tt.wantMatch && (!strings.Contains(out, "longline.txt:1:")) {
				t.Errorf("expected case-insensitive match, got:\n%s", out)
			}
			if !tt.wantMatch && has && !strings.Contains(out, "No matches found") {
				t.Errorf("case-sensitive search should not match, got:\n%s", out)
			}
		})
	}
}

func TestGrepToolContextLines(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "ctx.txt", "one\ntwo\nthree\nfour\nfive\n")
	_, out := grepExec(t, dir, `{"pattern":"three","context":1,"path":"ctx.txt"}`)

	want := []string{
		"ctx.txt-2- two",
		"ctx.txt:3: three",
		"ctx.txt-4- four",
	}
	got := strings.Split(out, "\n")
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(got), len(want), out)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGrepToolMatchLimitNotice(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "many.txt", "hit1\nhit2\nhit3\nhit4\nhit5\n")
	res, out := grepExec(t, dir, `{"pattern":"hit","limit":2,"path":"many.txt"}`)
	if !strings.Contains(out, "[2 matches limit reached. Use limit=4 for more, or refine pattern]") {
		t.Errorf("missing limit notice:\n%s", out)
	}
	lines := strings.Split(out, "\n")
	if len(lines) < 2 || !strings.Contains(lines[0], "many.txt:1:") || !strings.Contains(lines[1], "many.txt:2:") {
		t.Errorf("expected only first 2 matches before notice:\n%s", out)
	}
	details := res.Details.(*GrepToolDetails)
	if details.MatchLimitReached != 2 {
		t.Errorf("MatchLimitReached = %d, want 2", details.MatchLimitReached)
	}
}

func TestGrepToolGitignoreRespected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".gitignore", "ignored/\n*.log\n")
	writeFile(t, dir, "visible.txt", "needle visible\n")
	writeFile(t, dir, "ignored/hidden.txt", "needle hidden\n")
	writeFile(t, dir, "noise.log", "needle log\n")

	_, out := grepExec(t, dir, `{"pattern":"needle"}`)
	if !strings.Contains(out, "visible.txt:1: needle visible") {
		t.Errorf("visible file missing:\n%s", out)
	}
	if strings.Contains(out, "ignored/hidden.txt") || strings.Contains(out, "noise.log") {
		t.Errorf("ignored paths leaked into results:\n%s", out)
	}
}

func TestGrepToolGlobFilter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "match.go", "target in go\n")
	writeFile(t, dir, "match.txt", "target in txt\n")

	_, out := grepExec(t, dir, `{"pattern":"target","glob":"*.go"}`)
	if !strings.Contains(out, "match.go:1: target in go") {
		t.Errorf("go file missing:\n%s", out)
	}
	if strings.Contains(out, "match.txt") {
		t.Errorf("glob filter should exclude .txt:\n%s", out)
	}
}

func TestGrepToolLongLineTruncatedFlag(t *testing.T) {
	dir := grepSetup(t)
	res, out := grepExec(t, dir, `{"pattern":"NEEDLE","path":"longline.txt"}`)
	if !strings.Contains(out, "[Some lines truncated to 500 chars. Use read tool to see full lines]") {
		t.Errorf("missing truncation notice:\n%s", out[maxInt(0, len(out)-120):])
	}
	details := res.Details.(*GrepToolDetails)
	if !details.LinesTruncated {
		t.Error("LinesTruncated should be true")
	}
	matchLine := strings.Split(out, "\n")[0]
	if len([]rune(strings.TrimPrefix(matchLine, "longline.txt:1: "))) > GrepMaxLineLength+len("... [truncated]") {
		t.Error("matched line was not truncated to 500 chars")
	}
}

func TestGrepToolNoMatchesAndBadPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "f.txt", "plain content\n")
	if _, out := grepExec(t, dir, `{"pattern":"zzz-not-there"}`); out != "No matches found" {
		t.Errorf("out = %q, want 'No matches found'", out)
	}
	if _, err := execTool(t, NewGrepTool(dir), `{"pattern":"x","path":"no/such/dir"}`); err == nil {
		t.Error("expected error for missing path")
	}
}
