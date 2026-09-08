package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEditDocToolEditsMarkdown(t *testing.T) {
	dir := t.TempDir()
	tool := NewEditDocTool(dir, DocToolOptions{})

	planPath := filepath.Join(dir, "plan.md")
	writeTestFile(t, planPath, "# Plan\n\nStep one: analyze.\n")

	res, err := tool.Execute(context.Background(), "call-1",
		[]byte(`{"path":"plan.md","edits":[{"oldText":"analyze","newText":"review"}]}`), nil)
	if err != nil {
		t.Fatalf("markdown edit should be allowed, got: %v", err)
	}
	if got := readFile(t, planPath); !strings.Contains(got, "Step one: review.") {
		t.Errorf("file = %q, edit not applied", got)
	}
	if !strings.Contains(textOf(t, res), "Successfully replaced 1 block(s) in "+planPath+".") {
		t.Errorf("output = %q", textOf(t, res))
	}
}

func TestEditDocToolRejectsNonMarkdown(t *testing.T) {
	dir := t.TempDir()
	tool := NewEditDocTool(dir, DocToolOptions{})

	srcPath := filepath.Join(dir, "main.go")
	writeTestFile(t, srcPath, "package main\n")

	for _, args := range []string{
		`{"path":"main.go","edits":[{"oldText":"package main","newText":"package evil"}]}`,
		`{"path":"data.json","edits":[{"oldText":"a","newText":"b"}]}`,
	} {
		_, err := tool.Execute(context.Background(), "call-1", []byte(args), nil)
		if err == nil || !strings.Contains(err.Error(), "only markdown files ending in .md") {
			t.Errorf("args %s must be rejected, got: %v", args, err)
		}
	}
	if got := readFile(t, srcPath); got != "package main\n" {
		t.Errorf("source file must be untouched, got %q", got)
	}
}

func TestEditDocToolSymlinkPiercingBlocked(t *testing.T) {
	dir := t.TempDir()
	tool := NewEditDocTool(dir, DocToolOptions{})

	src := filepath.Join(dir, "app.go")
	writeTestFile(t, src, "package main\n")
	link := filepath.Join(dir, "notes.md")
	if err := os.Symlink(src, link); err != nil {
		t.Fatal(err)
	}

	_, err := tool.Execute(context.Background(), "call-1",
		[]byte(`{"path":"notes.md","edits":[{"oldText":"package main","newText":"package evil"}]}`), nil)
	if err == nil || !strings.Contains(err.Error(), "resolves to non-markdown target") {
		t.Fatalf("symlink piercing must be blocked, got: %v", err)
	}
	if got := readFile(t, src); got != "package main\n" {
		t.Errorf("source file must be untouched, got %q", got)
	}
}

func TestEditDocToolIdentity(t *testing.T) {
	tool := NewEditDocTool(t.TempDir(), DocToolOptions{})
	if tool.Name() != "edit_doc" {
		t.Errorf("name = %q, want edit_doc", tool.Name())
	}
	if tool.Label() != "edit_doc" {
		t.Errorf("label = %q, want edit_doc", tool.Label())
	}
	if len(tool.PromptGuidelines()) == 0 {
		t.Error("guidelines must be set")
	}
}
