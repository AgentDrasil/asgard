package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildDefaultPrompt(t *testing.T) {
	got := BuildSystemPrompt(Options{
		CWD:           "/home/u/proj",
		ToolSnippets:  map[string]string{"read": "read files", "bash": "run commands"},
		SelectedTools: []string{"read", "bash", "grep"},
	})
	if !strings.HasPrefix(got, "You are an expert coding assistant") {
		t.Fatalf("identity missing:\n%s", got)
	}
	if !strings.Contains(got, "- read: read files\n- bash: run commands") {
		t.Fatalf("tools list wrong (only snippet-bearing tools, in selection order):\n%s", got)
	}
	if strings.Contains(got, "grep:") {
		t.Fatal("tools without snippets must be omitted")
	}
	if !strings.Contains(got, "Current working directory: /home/u/proj") {
		t.Fatalf("cwd line missing:\n%s", got)
	}
}

func TestGuidelinesFiltering(t *testing.T) {
	got := BuildSystemPrompt(Options{
		CWD:           "/p",
		SelectedTools: []string{"read", "bash"},
	})
	if !strings.Contains(got, "- Use bash for file operations like ls, rg, find") {
		t.Fatalf("bash-only guideline missing when grep/find/ls absent:\n%s", got)
	}
	got2 := BuildSystemPrompt(Options{
		CWD:           "/p",
		SelectedTools: []string{"read", "bash", "grep"},
	})
	if strings.Contains(got2, "Use bash for file operations") {
		t.Fatal("guideline must be filtered out when grep present")
	}
	for _, want := range []string{"- Be concise in your responses", "- Show file paths clearly when working with files"} {
		if !strings.Contains(got2, want) {
			t.Fatalf("default guideline %q missing", want)
		}
	}
	// Dedup + trimming of custom guidelines.
	got3 := BuildSystemPrompt(Options{
		CWD:              "/p",
		PromptGuidelines: []string{"custom rule", "  ", "custom rule"},
	})
	if strings.Count(got3, "- custom rule") != 1 {
		t.Fatalf("custom guideline not deduped/trimmed:\n%s", got3)
	}
}

func TestAppendAndProjectContext(t *testing.T) {
	files := []ContextFile{
		{Path: "/r/AGENTS.md", Content: "root rules"},
		{Path: "/r/sub/CLAUDE.md", Content: "leaf rules"},
	}
	got := BuildSystemPrompt(Options{
		CWD:                "/r/sub",
		AppendSystemPrompt: "EXTRA RULES",
		ContextFiles:       files,
	})
	if !strings.Contains(got, "\n\nEXTRA RULES") {
		t.Fatalf("append section missing:\n%s", got)
	}
	if !strings.Contains(got, "<project_context>\n\nProject-specific instructions and guidelines:\n\n"+
		"<project_instructions path=\"/r/AGENTS.md\">\nroot rules\n</project_instructions>\n\n"+
		"<project_instructions path=\"/r/sub/CLAUDE.md\">\nleaf rules\n</project_instructions>\n\n"+
		"</project_context>") {
		t.Fatalf("context section framing wrong:\n%s", got)
	}
	idxCtx := strings.Index(got, "<project_context>")
	idxAppend := strings.Index(got, "EXTRA RULES")
	if idxAppend > idxCtx {
		t.Fatal("append text must precede project context")
	}
	if !strings.HasSuffix(got, "Current working directory: /r/sub") {
		t.Fatalf("prompt must end with cwd line:\n%q", got[len(got)-60:])
	}

	// No context files => no section at all.
	bare := BuildSystemPrompt(Options{CWD: "/p"})
	if strings.Contains(bare, "<project_context>") {
		t.Fatal("empty context must omit the section")
	}
}

func TestCustomPromptPath(t *testing.T) {
	got := BuildSystemPrompt(Options{
		CustomPrompt:       "You are custom.",
		CWD:                "/w",
		ContextFiles:       []ContextFile{{Path: "/w/AGENTS.md", Content: "ctx"}},
		AppendSystemPrompt: "APPEND",
	})
	if !strings.HasPrefix(got, "You are custom.") {
		t.Fatalf("custom prompt must replace default body:\n%s", got)
	}
	if strings.Contains(got, "Available tools:") {
		t.Fatal("custom prompt must not include default body sections")
	}
	wantOrder := []string{"APPEND", "<project_context>", "Current working directory: /w"}
	last := -1
	for _, w := range wantOrder {
		i := strings.Index(got, w)
		if i < 0 || i < last {
			t.Fatalf("ordering broken around %q:\n%s", w, got)
		}
		last = i
	}
}

func TestLoadProjectContextFiles(t *testing.T) {
	root := t.TempDir()
	mid := filepath.Join(root, "mid")
	leaf := filepath.Join(mid, "leaf")
	agentDir := t.TempDir()
	for _, d := range []string{leaf} {
		_ = os.MkdirAll(d, 0o755)
	}

	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, append([]byte("\xef\xbb\xbf"), []byte(content)...), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(filepath.Join(root, "AGENTS.md"), "root agents")
	write(filepath.Join(agentDir, "AGENTS.md"), "global agents")
	write(filepath.Join(mid, "AGENTS.md"), "mid agents")
	write(filepath.Join(mid, "AGENTS.override.md"), "mid override wins")
	write(filepath.Join(leaf, "AGENTS.md"), "leaf agents")

	files := LoadProjectContextFiles(leaf, agentDir)

	var paths []string
	var contents []string
	for _, f := range files {
		paths = append(paths, f.Path)
		contents = append(contents, f.Content)
	}
	wantOrder := []string{
		filepath.Join(agentDir, "AGENTS.md"),
		filepath.Join(root, "AGENTS.md"),
		filepath.Join(mid, "AGENTS.override.md"),
		filepath.Join(leaf, "AGENTS.md"),
	}
	if strings.Join(paths, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("order wrong:\n got %v\nwant %v", paths, wantOrder)
	}
	// BOM stripped.
	for i, c := range contents {
		if strings.HasPrefix(c, "\xef\xbb\xbf") {
			t.Errorf("BOM not stripped from %s", paths[i])
		}
	}
}

func TestLoadProjectContextFilesEmpty(t *testing.T) {
	root := t.TempDir()
	leaf := filepath.Join(root, "a", "b")
	_ = os.MkdirAll(leaf, 0o755)
	if files := LoadProjectContextFiles(leaf, t.TempDir()); len(files) != 0 {
		t.Fatalf("expected no files, got %+v", files)
	}
}
