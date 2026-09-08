package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteDocToolOnlyMarkdown(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteDocTool(dir, DocToolOptions{})

	// .md paths are allowed (case-insensitive), others rejected.
	if _, err := tool.Execute(context.Background(), "call-1", []byte(`{"path":"docs/intend.md","content":"x"}`), nil); err != nil {
		t.Errorf("markdown write should be allowed, got: %v", err)
	}
	if got := readFile(t, dir+"/docs/intend.md"); got != "x" {
		t.Errorf("file = %q, want %q", got, "x")
	}
	if _, err := tool.Execute(context.Background(), "call-2", []byte(`{"path":"NOTES.MD","content":"x"}`), nil); err != nil {
		t.Errorf("uppercase .MD write should be allowed, got: %v", err)
	}

	for _, p := range []string{"src/main.go", "data.json", "notes.md.txt", "AGENTS.md.bak"} {
		_, err := tool.Execute(context.Background(), "call-3", []byte(`{"path":"`+p+`","content":"x"}`), nil)
		if err == nil || !strings.Contains(err.Error(), "only markdown files ending in .md") {
			t.Errorf("path %q must be rejected, got: %v", p, err)
		}
	}
	if _, statErr := os.Stat(dir + "/src"); !os.IsNotExist(statErr) {
		t.Error("rejected file must not be created")
	}
}

func TestWriteDocToolSymlinkPiercingBlocked(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteDocTool(dir, DocToolOptions{})

	// foo.md is a symlink to a source file: the resolved target is not
	// markdown, so the write must be rejected.
	src := filepath.Join(dir, "src", "app.go")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "evil.md")
	if err := os.Symlink(src, link); err != nil {
		t.Fatal(err)
	}

	_, err := tool.Execute(context.Background(), "call-1", []byte(`{"path":"evil.md","content":"hacked"}`), nil)
	if err == nil || !strings.Contains(err.Error(), "resolves to non-markdown target") {
		t.Fatalf("symlink piercing must be blocked, got: %v", err)
	}
	if got, readErr := os.ReadFile(src); readErr != nil || string(got) != "package main" {
		t.Fatalf("source file must be untouched, got %q (%v)", got, readErr)
	}
}

func TestWriteDocToolAllowedDirs(t *testing.T) {
	dir := t.TempDir()
	docsDir := filepath.Join(dir, "docs")
	sessionDir := filepath.Join(dir, "session")
	for _, d := range []string{docsDir, sessionDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tool := NewWriteDocTool(dir, DocToolOptions{AllowedDirs: []string{sessionDir}})

	if _, err := tool.Execute(context.Background(), "call-1", []byte(`{"path":"session/intend.md","content":"x"}`), nil); err != nil {
		t.Errorf("write inside allowed dir should succeed, got: %v", err)
	}

	_, err := tool.Execute(context.Background(), "call-2", []byte(`{"path":"docs/plan.md","content":"x"}`), nil)
	if err == nil || !strings.Contains(err.Error(), "outside the allowed directories") {
		t.Errorf("write outside allowed dirs must be rejected, got: %v", err)
	}

	// A symlink inside an allowed dir pointing outside is blocked too.
	outside := filepath.Join(dir, "outside.md")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(sessionDir, "escape.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	_, err = tool.Execute(context.Background(), "call-3", []byte(`{"path":"session/escape.md","content":"hacked"}`), nil)
	if err == nil || !strings.Contains(err.Error(), "outside the allowed directories") {
		t.Errorf("symlink escape from allowed dir must be blocked, got: %v", err)
	}
	if got, readErr := os.ReadFile(outside); readErr != nil || string(got) != "keep" {
		t.Fatalf("symlink target must be untouched, got %q (%v)", got, readErr)
	}
}

func TestWriteDocToolDanglingSymlinkBlocked(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteDocTool(dir, DocToolOptions{})

	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	// notes.md -> outside/x.md where the target does not exist yet: without
	// the dangling check, os.WriteFile would follow the link and create the
	// file outside the workspace-adjacent tree.
	if err := os.Symlink(filepath.Join(outside, "x.md"), filepath.Join(dir, "notes.md")); err != nil {
		t.Fatal(err)
	}

	_, err := tool.Execute(context.Background(), "call-1", []byte(`{"path":"notes.md","content":"hacked"}`), nil)
	if err == nil || !strings.Contains(err.Error(), "dangling symlink") {
		t.Fatalf("dangling symlink write must be rejected, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "x.md")); !os.IsNotExist(statErr) {
		t.Fatal("dangling target must not have been created")
	}
}

func TestWriteDocToolIdentity(t *testing.T) {
	tool := NewWriteDocTool(t.TempDir(), DocToolOptions{})
	if tool.Name() != "write_doc" {
		t.Errorf("name = %q, want write_doc", tool.Name())
	}
	if tool.Label() != "write_doc" {
		t.Errorf("label = %q, want write_doc", tool.Label())
	}
	if tool.Description() == "" || tool.PromptSnippet() == "" || len(tool.PromptGuidelines()) == 0 {
		t.Error("description, snippet, and guidelines must be set")
	}
}
