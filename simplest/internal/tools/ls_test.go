package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestLsToolLimitSemantics(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"f1.txt", "f2.txt", "f3.txt"} {
		writeFile(t, dir, n, "")
	}

	t.Run("limit 0 yields empty directory", func(t *testing.T) {
		t.Parallel()
		out := textOf(t, mustExec(t, NewLsTool(dir), `{"limit":0}`))
		require.Equal(t, "(empty directory)", out)
	})

	t.Run("negative limit yields empty directory", func(t *testing.T) {
		t.Parallel()
		out := textOf(t, mustExec(t, NewLsTool(dir), `{"limit":-5}`))
		require.Equal(t, "(empty directory)", out)
	})

	t.Run("fractional limit rounds up", func(t *testing.T) {
		t.Parallel()
		res := mustExec(t, NewLsTool(dir), `{"limit":1.5}`)
		out := textOf(t, res)
		assert.NotContains(t, out, "f3.txt", "ceil(1.5)=2 should return 2 entries")
		assert.Contains(t, out, "[2 entries limit reached. Use limit=4 for more]")
		assert.Equal(t, 2, res.Details.(*LsToolDetails).EntryLimitReached)
	})

	t.Run("omitted limit defaults to 500", func(t *testing.T) {
		t.Parallel()
		out := textOf(t, mustExec(t, NewLsTool(dir), `{}`))
		assert.Contains(t, out, "f1.txt")
		assert.Contains(t, out, "f3.txt")
	})

	t.Run("huge limit does not overflow", func(t *testing.T) {
		t.Parallel()
		out := textOf(t, mustExec(t, NewLsTool(dir), `{"limit":1e300}`))
		assert.Contains(t, out, "f1.txt")
		assert.Contains(t, out, "f2.txt")
		assert.Contains(t, out, "f3.txt")
		assert.NotContains(t, out, "limit reached")
	})

	t.Run("32-bit overflow class limit", func(t *testing.T) {
		t.Parallel()
		out := textOf(t, mustExec(t, NewLsTool(dir), `{"limit":1e10}`))
		assert.Contains(t, out, "f3.txt")
		assert.NotContains(t, out, "limit reached")
	})
}

func TestLsToolStableCaseInsensitiveOrder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	names := []string{"a", "A", "b", "B", "c", "C", "d", "D", "e", "E", "f", "F", "g", "G", "h", "H"}
	for _, n := range names {
		writeFile(t, dir, n, "")
	}
	// Stable sort over os.ReadDir (byte-sorted: A < B < ... < a < b < ...) keeps
	// byte order within each case-insensitive group, so the expected sequence is
	// A,a,B,b,C,c,... — an unstable sort could swap same-key pairs.
	var want []string
	for i := 0; i < len(names); i += 2 {
		want = append(want, strings.ToUpper(names[i]), names[i])
	}

	var first []string
	for i := 0; i < 10; i++ {
		got := strings.Split(textOf(t, mustExec(t, NewLsTool(dir), `{}`)), "\n")
		require.Equal(t, want, got, "case-insensitive alphabetical order with deterministic same-key order expected")
		if first == nil {
			first = got
			continue
		}
		require.Equal(t, first, got, "ordering must be deterministic across repeated calls")
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
