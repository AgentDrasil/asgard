package tools

import (
	"strings"
	"testing"
)

func TestWriteToolCreatesParentsAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(dir)

	mustExec(t, tool, `{"path":"a/b/c.txt","content":"first"}`)
	if got := readFile(t, dir+"/a/b/c.txt"); got != "first" {
		t.Errorf("nested write = %q, want %q", got, "first")
	}
	if got := textOf(t, mustExec(t, tool, `{"path":"a/b/c.txt","content":"second"}`)); !strings.Contains(got, "Successfully wrote") {
		t.Error("expected success message")
	}
	if got := readFile(t, dir+"/a/b/c.txt"); got != "second" {
		t.Errorf("overwrite = %q, want %q", got, "second")
	}
}

func TestWriteToolEmptyContent(t *testing.T) {
	dir := t.TempDir()
	res := mustExec(t, NewWriteTool(dir), `{"path":"empty.txt","content":""}`)
	if got := readFile(t, dir+"/empty.txt"); got != "" {
		t.Errorf("file = %q, want empty", got)
	}
	if !strings.Contains(textOf(t, res), "Successfully wrote 0 bytes") {
		t.Errorf("output = %q", textOf(t, res))
	}
}
