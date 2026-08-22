package notebook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTasksFromFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	tests := []struct {
		name    string
		content string
		want    []TaskItem
	}{
		{
			"open and completed tasks",
			"# Title\n- [ ] first task\n- [x] completed task\n- [ ] second task\n",
			[]TaskItem{
				{Line: 2, Text: "first task"},
				{Line: 4, Text: "second task"},
			},
		},
		{
			"indented and CRLF tasks",
			"  - [ ] indented task\r\n\t- [ ] tabbed task\r\n",
			[]TaskItem{
				{Line: 1, Text: "indented task"},
				{Line: 2, Text: "tabbed task"},
			},
		},
		{
			"plain checkbox and list markers are ignored",
			"- [X] uppercase done\n* [ ] star bullet\n- [] no space\n- [ ]no space after bracket\n",
			[]TaskItem{{Line: 4, Text: "no space after bracket"}},
		},
		{"no tasks", "# Just a heading\nregular text\n", nil},
		{"empty file", "", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fileName := strings.ReplaceAll(tt.name, " ", "_") + ".md"
			path := filepath.Join(tmpDir, fileName)
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o644))

			got, err := ParseTasksFromFile(path, tmpDir)
			require.NoError(t, err)
			for i := range tt.want {
				tt.want[i].RelPath = fileName
			}
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("nested path is relative to base dir", func(t *testing.T) {
		t.Parallel()

		nested := filepath.Join(tmpDir, "04_Trackers", "tax.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(nested), 0o755))
		require.NoError(t, os.WriteFile(nested, []byte("- [ ] file tax return\n"), 0o644))

		got, err := ParseTasksFromFile(nested, tmpDir)
		require.NoError(t, err)
		assert.Equal(t, []TaskItem{{RelPath: "04_Trackers/tax.md", Line: 1, Text: "file tax return"}}, got)
	})

	t.Run("missing file errors", func(t *testing.T) {
		t.Parallel()

		_, err := ParseTasksFromFile(filepath.Join(tmpDir, "gone.md"), tmpDir)
		require.ErrorIs(t, err, os.ErrNotExist)
	})
}

func TestGetTodoTasks(t *testing.T) {
	t.Parallel()

	noteDir := t.TempDir()
	writeNote := func(rel, content string) {
		t.Helper()
		path := filepath.Join(noteDir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	writeNote(filepath.Join("01_Raw", "Journal", "2025-08-20.md"), "- [ ] write journal\n- [x] done part\n")
	writeNote(filepath.Join("04_Trackers", "tax.md"), "- [ ] file tax return\n")
	writeNote(filepath.Join("00_Home", "Inbox.md"), "nothing pending here\n")
	writeNote(filepath.Join(".git", "config.md"), "- [ ] git hidden task\n")
	writeNote(filepath.Join(".obsidian", "ui.md"), "- [ ] obsidian hidden task\n")
	writeNote("tmp.md", "- [ ] root-level file still scanned\n")
	writeNote(filepath.Join("99_Templates", "daily.md"), "- [ ] template task\n")
	writeNote(filepath.Join("node_modules", "pkg", "readme.md"), "- [ ] node task\n")

	got, err := GetTodoTasks(noteDir, nil)
	require.NoError(t, err)

	expected := "- [01_Raw/Journal/2025-08-20.md:1] write journal\n" +
		"- [04_Trackers/tax.md:1] file tax return\n" +
		"- [tmp.md:1] root-level file still scanned"
	assert.Equal(t, expected, got)
}

func TestGetTodoTasksCustomSkipDirs(t *testing.T) {
	t.Parallel()

	noteDir := t.TempDir()
	path := filepath.Join(noteDir, "98_Archived", "old.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("- [ ] archived task\n"), 0o644))

	got, err := GetTodoTasks(noteDir, []string{"98_Archived"})
	require.NoError(t, err)
	assert.Equal(t, "No pending tasks found.", got)

	got, err = GetTodoTasks(noteDir, nil)
	require.NoError(t, err)
	assert.Equal(t, "- [98_Archived/old.md:1] archived task", got)
}

func TestGetTodoTasksNoPending(t *testing.T) {
	t.Parallel()

	noteDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(noteDir, "clean.md"), []byte("- [x] all done\n"), 0o644))

	got, err := GetTodoTasks(noteDir, nil)
	require.NoError(t, err)
	assert.Equal(t, "No pending tasks found.", got)
}

func TestDefaultSkipDirs(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{
		".git", ".obsidian", ".venv", ".agents", "tmp", "99_Templates", "node_modules",
	}, DefaultSkipDirs)
}
