package tools

import (
	"strings"
	"testing"
)

func TestLsToolOrderAndSuffixes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Banana", "")
	if err := mkdirIn(dir, "apple"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, ".hidden", "")
	writeFile(t, dir, ".hiddendir_marker", "")

	res := mustExec(t, NewLsTool(dir), `{}`)
	got := strings.Split(textOf(t, res), "\n")
	want := []string{".hidden", ".hiddendir_marker", "apple/", "Banana"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q (case-insensitive alphabetical order)", i, got[i], want[i])
		}
	}
}

func TestLsToolDotfilesIncluded(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".dotfile", "x")
	out := textOf(t, mustExec(t, NewLsTool(dir), `{}`))
	if out != ".dotfile" {
		t.Errorf("out = %q, want .dotfile to be listed", out)
	}
}

func TestLsToolEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	out := textOf(t, mustExec(t, NewLsTool(dir), `{"path":"."}`))
	if out != "(empty directory)" {
		t.Errorf("out = %q, want '(empty directory)'", out)
	}
}

func TestLsToolEntryLimitNotice(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		writeFile(t, dir, n+".txt", "")
	}
	res := mustExec(t, NewLsTool(dir), `{"limit":2}`)
	out := textOf(t, res)
	lines := strings.Split(out, "\n")
	if len(lines) < 2 || !strings.Contains(lines[len(lines)-1], "[2 entries limit reached. Use limit=4 for more]") {
		t.Errorf("missing limit notice:\n%s", out)
	}
	details := res.Details.(*LsToolDetails)
	if details.EntryLimitReached != 2 {
		t.Errorf("EntryLimitReached = %d, want 2", details.EntryLimitReached)
	}
}

func TestLsToolSubdirectoryPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "sub/inner.txt", "x")
	if err := mkdirIn(dir, "sub/nested"); err != nil {
		t.Fatal(err)
	}
	out := textOf(t, mustExec(t, NewLsTool(dir), `{"path":"sub"}`))
	got := strings.Split(out, "\n")
	if len(got) != 2 || got[0] != "inner.txt" || got[1] != "nested/" {
		t.Errorf("out = %v, want [inner.txt nested/]", got)
	}
}

func TestLsToolErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "file.txt", "x")
	if _, err := execTool(t, NewLsTool(dir), `{"path":"missing"}`); err == nil || !strings.Contains(err.Error(), "path not found") {
		t.Errorf("err = %v, want path not found", err)
	}
	if _, err := execTool(t, NewLsTool(dir), `{"path":"file.txt"}`); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("err = %v, want not a directory", err)
	}
}
