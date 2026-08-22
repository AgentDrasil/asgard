package notebook

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeSHA1(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"regular content", "hello notebook\n", "ce5d4a9fcc0f006dab194505cec352ff64a7bbdd"},
		{"empty file", "", "da39a3ee5e6b4b0d3255bfef95601890afd80709"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(tmpDir, strings.ReplaceAll(tt.name, " ", "_")+".md")
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o644))

			got, err := ComputeSHA1(path)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("missing file", func(t *testing.T) {
		t.Parallel()

		_, err := ComputeSHA1(filepath.Join(tmpDir, "does_not_exist.md"))
		require.ErrorIs(t, err, os.ErrNotExist)
	})
}

func TestSaveAndLoadStateRoundTrip(t *testing.T) {
	t.Parallel()

	stateFile := filepath.Join(t.TempDir(), ".state", "absorb_state.yaml")
	state := StateMap{
		"2025-08-20.md": {
			Path:   "01_Raw/Journal/2025-08-20.md",
			SHA1:   "abc123",
			Status: "absorbed",
			Date:   "2025-08-21",
			Extra:  map[string]interface{}{"model": "gemini"},
		},
		"broken.md": {
			Path:      "01_Raw/Notes/broken.md",
			Status:    "failed",
			FailCount: 2,
		},
	}

	require.NoError(t, SaveState(state, stateFile))

	// Keys are file base names and extras are inlined at entry level.
	raw, err := os.ReadFile(stateFile)
	require.NoError(t, err)
	content := string(raw)
	assert.Contains(t, content, "2025-08-20.md:")
	assert.Contains(t, content, "model: gemini")
	assert.NotContains(t, content, "extra:")

	loaded, err := LoadState(stateFile)
	require.NoError(t, err)
	assert.Equal(t, state, loaded)
}

func TestLoadStateCompatWithPythonYAML(t *testing.T) {
	t.Parallel()

	legacy := `2025-08-01.md:
  path: 01_Raw/Journal/2025-08-01.md
  sha1: deadbeef
  status: absorbed
  date: "2025-08-02"
clipping.md:
  path: Data/Clippings/clipping.md
  status: ingested
  output: 01_Raw/Entities/clipping.md
`
	stateFile := filepath.Join(t.TempDir(), "ingest_state.yaml")
	require.NoError(t, os.WriteFile(stateFile, []byte(legacy), 0o644))

	loaded, err := LoadState(stateFile)
	require.NoError(t, err)

	journal, ok := loaded["2025-08-01.md"]
	require.True(t, ok, "entry keyed by basename expected")
	assert.Equal(t, "01_Raw/Journal/2025-08-01.md", journal.Path)
	assert.Equal(t, "deadbeef", journal.SHA1)
	assert.Equal(t, "absorbed", journal.Status)
	assert.Equal(t, "2025-08-02", journal.Date)

	clipping, ok := loaded["clipping.md"]
	require.True(t, ok)
	assert.Equal(t, "ingested", clipping.Status)
	assert.Equal(t, "01_Raw/Entities/clipping.md", clipping.Extra["output"])
}

func TestLoadStateMissingFile(t *testing.T) {
	t.Parallel()

	loaded, err := LoadState(filepath.Join(t.TempDir(), "missing_state.yaml"))
	require.NoError(t, err)
	assert.Empty(t, loaded)
}

func TestNeedsProcessing(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "note.md")
	require.NoError(t, os.WriteFile(path, []byte("v1"), 0o644))
	sum, err := ComputeSHA1(path)
	require.NoError(t, err)

	tests := []struct {
		name  string
		state StateMap
		want  bool
	}{
		{"unknown file returns true", StateMap{}, true},
		{
			"unchanged file returns false",
			StateMap{"note.md": {Path: path, SHA1: sum, Status: "absorbed"}},
			false,
		},
		{
			"modified file returns true",
			StateMap{"note.md": {Path: path, SHA1: "stale", Status: "absorbed"}},
			true,
		},
		{
			"failed file without sha1 returns true",
			StateMap{"note.md": {Path: path, Status: "failed"}},
			true,
		},
		{
			"fail count at ceiling returns false even after change",
			StateMap{"note.md": {Path: path, Status: "failed", FailCount: MaxFailCount}},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := NeedsProcessing(path, tt.state)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("missing file errors", func(t *testing.T) {
		t.Parallel()

		_, err := NeedsProcessing(filepath.Join(tmpDir, "gone.md"), StateMap{
			"gone.md": {SHA1: "whatever"},
		})
		require.ErrorIs(t, err, os.ErrNotExist)
	})
}

func TestCollectCandidatesAndFindPending(t *testing.T) {
	t.Parallel()

	inputDir := t.TempDir()
	writeFile := func(rel, content string) string {
		t.Helper()
		path := filepath.Join(inputDir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		return path
	}

	journal := writeFile(filepath.Join("Journal", "day1.md"), "journal")
	notes := writeFile(filepath.Join("Notes", "note.md"), "note")
	upper := writeFile(filepath.Join("Notes", "UPPER.MD"), "uppercase markdown")
	writeFile(filepath.Join("97_Attachments", "pic.PNG"), "binary")
	writeFile("ignored.txt", "not markdown")

	candidates, err := CollectCandidates(inputDir, []string{".md"})
	require.NoError(t, err)
	assert.Equal(t, []string{journal, upper, notes}, candidates,
		"extension matching is case-insensitive and results are sorted")

	t.Run("missing input dir yields empty result", func(t *testing.T) {
		t.Parallel()

		got, err := CollectCandidates(filepath.Join(t.TempDir(), "absent"), []string{".md"})
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	state := StateMap{}
	RecordSuccess(journal, state, "absorbed", nil)

	pending, err := FindPending(candidates, state)
	require.NoError(t, err)
	assert.Equal(t, []string{upper, notes}, pending,
		"processed journal file is filtered out")

	exhausted := StateMap{}
	for range MaxFailCount {
		RecordFailure(notes, exhausted)
	}
	pendingWithFailures, err := FindPending(candidates, exhausted)
	require.NoError(t, err)
	assert.Equal(t, []string{journal, upper}, pendingWithFailures,
		"file at the failure ceiling is excluded from pending")
}

func TestRecordSuccess(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "entry.md")
	require.NoError(t, os.WriteFile(path, []byte("content"), 0o644))
	sum, err := ComputeSHA1(path)
	require.NoError(t, err)

	state := StateMap{"entry.md": {Path: "old/path.md", Status: "failed", FailCount: 2}}
	RecordSuccess(path, state, "absorbed", map[string]interface{}{"model": "gemini"})

	item, ok := state["entry.md"]
	require.True(t, ok)
	assert.Equal(t, path, item.Path)
	assert.Equal(t, sum, item.SHA1)
	assert.Equal(t, "absorbed", item.Status)
	assert.Equal(t, 0, item.FailCount)
	assert.NotEmpty(t, item.Date)
	assert.Equal(t, "gemini", item.Extra["model"])

	needed, err := NeedsProcessing(path, state)
	require.NoError(t, err)
	assert.False(t, needed, "recorded success makes the file up to date")
}

func TestRecordFailure(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "entry.md")
	require.NoError(t, os.WriteFile(path, []byte("content"), 0o644))

	state := StateMap{}
	for i := 1; i <= MaxFailCount; i++ {
		RecordFailure(path, state)
		item, ok := state[filepath.Base(path)]
		require.True(t, ok)
		assert.Equal(t, i, item.FailCount)
		assert.Equal(t, "failed", item.Status)
	}

	needed, err := NeedsProcessing(path, state)
	require.NoError(t, err)
	assert.False(t, needed, "at the failure ceiling the file is skipped")

	// Failures wipe the SHA-1 so the file is retried while below the ceiling.
	state = StateMap{}
	RecordFailure(path, state)
	needed, err = NeedsProcessing(path, state)
	require.NoError(t, err)
	assert.True(t, needed)
}

func TestFileLockLifecycle(t *testing.T) {
	t.Parallel()

	lockFile := filepath.Join(t.TempDir(), ".state", "absorb.lock")

	lock := NewFileLock(lockFile)
	assert.True(t, lock.Acquire())
	assert.True(t, lock.Acquired())

	data, err := os.ReadFile(lockFile)
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), mustAtoi(t, string(data)))

	t.Run("held lock blocks second acquisition", func(t *testing.T) {
		t.Parallel()

		rival := NewFileLock(lockFile)
		assert.False(t, rival.Acquire())
		assert.False(t, rival.Acquired())
	})

	t.Run("release allows re-acquisition", func(t *testing.T) {
		t.Parallel()

		other := filepath.Join(t.TempDir(), "other.lock")
		first := NewFileLock(other)
		require.True(t, first.Acquire())
		first.Release()
		assert.False(t, first.Acquired())

		second := NewFileLock(other)
		assert.True(t, second.Acquire())
		assert.FileExists(t, other)
	})

	t.Run("release without acquire is a no-op", func(t *testing.T) {
		t.Parallel()

		fresh := NewFileLock(filepath.Join(t.TempDir(), "noop.lock"))
		fresh.Release()
		assert.NoFileExists(t, fresh.lockFile)
	})

	lock.Release()
	assert.NoFileExists(t, lockFile)

	reacquired := NewFileLock(lockFile)
	assert.True(t, reacquired.Acquire())
}

func TestFileLockTakesOverStaleLock(t *testing.T) {
	t.Parallel()

	// Spawn and reap a short-lived process to obtain a PID that no longer
	// exists.
	cmd := exec.Command("sleep", "30")
	require.NoError(t, cmd.Start())
	stalePID := cmd.Process.Pid
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait(), "killed process reports a signal on wait")

	tests := []struct {
		name    string
		content string
	}{
		{"dead pid lock", strconv.Itoa(stalePID)},
		{"garbage content", "not-a-pid"},
		{"empty content", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lockFile := filepath.Join(t.TempDir(), ".state", "absorb.lock")
			require.NoError(t, os.MkdirAll(filepath.Dir(lockFile), 0o755))
			require.NoError(t, os.WriteFile(lockFile, []byte(tt.content), 0o644))

			lock := NewFileLock(lockFile)
			assert.True(t, lock.Acquire(), "stale lock should be taken over")

			data, err := os.ReadFile(lockFile)
			require.NoError(t, err)
			assert.Equal(t, os.Getpid(), mustAtoi(t, string(data)))
		})
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	value, err := strconv.Atoi(strings.TrimSpace(s))
	require.NoError(t, err)
	return value
}
