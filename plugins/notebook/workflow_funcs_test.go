package notebook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/pluginsdk"
)

// newTestVault creates an isolated vault skeleton (Data/, 01_Raw/, .state/).
func newTestVault(t *testing.T) string {
	t.Helper()
	vault := t.TempDir()
	for _, dir := range []string{ingestDir, absorbDir, ".state"} {
		require.NoError(t, os.MkdirAll(filepath.Join(vault, dir), 0o755))
	}
	return vault
}

// useVaultEnv injects the vault root via the environment override. The test
// cannot run in parallel because t.Setenv mutates process state.
func useVaultEnv(t *testing.T, vault string) {
	t.Helper()
	t.Setenv(envVaultDir, vault)
}

// writeVaultFile writes content at the vault-relative path and returns the
// absolute path.
func writeVaultFile(t *testing.T, vault, rel, content string) string {
	t.Helper()
	path := filepath.Join(vault, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// newTestNodeContext builds a minimal node context rooted at tmpDir.
func newTestNodeContext(tmpDir string) *pluginsdk.NodeContext {
	return &pluginsdk.NodeContext{SessionID: "sess-test", RunID: "run-test", TmpDir: tmpDir}
}

// readNonEmptyLines returns the trimmed, non-empty lines of a file.
func readNonEmptyLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// writeFanoutResults writes engine-shaped fan-out result entries as JSONL.
func writeFanoutResults(t *testing.T, tmpDir, name string, entries []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		require.NoError(t, err)
		buf.Write(data)
		buf.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, name), buf.Bytes(), 0o644))
}

// assertGroup decodes an absorb items line and compares it to want.
func assertGroup(t *testing.T, line string, want []string) {
	t.Helper()
	var got []string
	require.NoError(t, json.Unmarshal([]byte(line), &got))
	assert.Equal(t, want, got)
}

func TestNotebookFunctionsRegistered(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		FunctionScanIngestPending,
		FunctionRecordIngestSuccess,
		FunctionScanAbsorbPending,
		FunctionRecordAbsorbSuccess,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fn, ok := pluginsdk.DefaultFunctionRegistry().Get(name)
			require.True(t, ok, "function %s should be registered in the default registry", name)
			require.NotNil(t, fn)

			child := pluginsdk.NewFunctionRegistryWithParent(pluginsdk.DefaultFunctionRegistry())
			inherited, ok := child.Get(name)
			require.True(t, ok, "child registries should inherit %s", name)
			require.NotNil(t, inherited)
		})
	}
}

func TestResolveVaultDirPriority(t *testing.T) {
	tmp := t.TempDir()
	envVault := filepath.Join(tmp, "env-vault")
	runVault := filepath.Join(tmp, "run-vault")
	otherVault := filepath.Join(tmp, "other-vault")
	for _, dir := range []string{envVault, runVault, otherVault} {
		require.NoError(t, os.MkdirAll(dir, 0o755))
	}

	tests := []struct {
		name     string
		env      string
		runDirs  []string
		want     string
		wantErr  bool
		errSubst string
	}{
		{"env var takes priority", envVault, []string{runVault}, envVault, false, ""},
		{"first workflow run dir when env unset", "", []string{runVault, otherVault}, runVault, false, ""},
		{"relative run dir rejected", "", []string{"relative/vault"}, "", true, "vault dir unresolved"},
		{"no resolution source", "", nil, "", true, "vault dir unresolved"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envVaultDir, tt.env)

			got, err := resolveVaultDir(&pluginsdk.NodeContext{WorkflowRunDirs: tt.runDirs})
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.errSubst)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestScanPrefersEnvVaultOverRunDirs(t *testing.T) {
	envVault := newTestVault(t)
	runVault := newTestVault(t)
	writeVaultFile(t, envVault, "Data/env.pdf", "env")
	writeVaultFile(t, runVault, "Data/run.pdf", "run")
	tmp := t.TempDir()

	useVaultEnv(t, envVault)
	nctx := newTestNodeContext(tmp)
	nctx.WorkflowRunDirs = []string{runVault}

	out, err := ScanIngestPending(context.Background(), nctx)
	require.NoError(t, err)
	assert.Contains(t, out, "candidates=1 pending=1")
	assert.Equal(t, []string{"Data/env.pdf"}, readNonEmptyLines(t, filepath.Join(tmp, ingestItemsName)))
}

func TestScanUsesWorkflowRunDirWhenEnvUnset(t *testing.T) {
	vault := newTestVault(t)
	writeVaultFile(t, vault, "Data/Clippings/foo.pdf", "pdf-bytes")
	tmp := t.TempDir()

	t.Setenv(envVaultDir, "")
	nctx := newTestNodeContext(tmp)
	nctx.WorkflowRunDirs = []string{vault}

	out, err := ScanIngestPending(context.Background(), nctx)
	require.NoError(t, err)
	assert.Contains(t, out, "candidates=1 pending=1")
	assert.Equal(t, []string{"Data/Clippings/foo.pdf"}, readNonEmptyLines(t, filepath.Join(tmp, ingestItemsName)))
}

func TestScanEmptyVaultProducesEmptyItems(t *testing.T) {
	vault := newTestVault(t)
	useVaultEnv(t, vault)
	tmp := t.TempDir()

	out, err := ScanIngestPending(context.Background(), newTestNodeContext(tmp))
	require.NoError(t, err)
	assert.Contains(t, out, "candidates=0 pending=0")
	assert.Empty(t, readNonEmptyLines(t, filepath.Join(tmp, ingestItemsName)))
}

func TestIngestPipelineIncremental(t *testing.T) {
	vault := newTestVault(t)
	useVaultEnv(t, vault)
	tmp := t.TempDir()
	nctx := newTestNodeContext(tmp)

	writeVaultFile(t, vault, "Data/Clippings/bar.docx", "bar")
	writeVaultFile(t, vault, "Data/Clippings/foo.pdf", "foo")
	writeVaultFile(t, vault, "Data/Sub/deep.html", "deep")
	writeVaultFile(t, vault, "Data/notes.md", "notes")
	writeVaultFile(t, vault, "Data/Clippings/image.png", "binary") // unsupported extension

	t.Run("initial scan lists all supported files", func(t *testing.T) {
		out, err := ScanIngestPending(context.Background(), nctx)
		require.NoError(t, err)
		assert.Contains(t, out, "candidates=4 pending=4 excluded_failed=0")
		assert.Equal(t, []string{
			"Data/Clippings/bar.docx",
			"Data/Clippings/foo.pdf",
			"Data/Sub/deep.html",
			"Data/notes.md",
		}, readNonEmptyLines(t, filepath.Join(tmp, ingestItemsName)))
		assert.NoFileExists(t, filepath.Join(vault, ".state", "ingest.lock"),
			"lock must be released after the scan")
	})

	t.Run("record settles fan-out results", func(t *testing.T) {
		writeFanoutResults(t, tmp, ingestResultsName, []map[string]any{
			{"item_index": 1, "item": "Data/Clippings/bar.docx", "status": "SUCCEEDED", "output": "01_Raw/Entities/bar.md"},
			{"item_index": 2, "item": "Data/Clippings/foo.pdf", "status": "FAILED", "output": "conversion error"},
			{"item_index": 3, "item": "Data/Sub/deep.html", "status": "SUCCEEDED", "output": ""},
			{"item_index": 4, "item": "Data/notes.md", "status": "SUCCEEDED", "output": ""},
		})

		out, err := RecordIngestSuccess(context.Background(), nctx)
		require.NoError(t, err)
		assert.Contains(t, out, "succeeded=3 failed=1")

		state, err := LoadState(filepath.Join(vault, ".state", "ingest_state.yaml"))
		require.NoError(t, err)

		bar, ok := state["bar.docx"]
		require.True(t, ok)
		wantSum, err := ComputeSHA1(filepath.Join(vault, "Data", "Clippings", "bar.docx"))
		require.NoError(t, err)
		assert.Equal(t, wantSum, bar.SHA1)
		assert.Equal(t, statusIngested, bar.Status)
		assert.Equal(t, 0, bar.FailCount)

		foo, ok := state["foo.pdf"]
		require.True(t, ok)
		assert.Equal(t, 1, foo.FailCount)
		assert.Empty(t, foo.SHA1, "failures drop the SHA-1 so the file is retried")
	})

	t.Run("rescan only reports unsettled files", func(t *testing.T) {
		out, err := ScanIngestPending(context.Background(), nctx)
		require.NoError(t, err)
		assert.Contains(t, out, "candidates=4 pending=1")
		assert.Equal(t, []string{"Data/Clippings/foo.pdf"}, readNonEmptyLines(t, filepath.Join(tmp, ingestItemsName)))
	})

	t.Run("failure ceiling excludes file from scan", func(t *testing.T) {
		// foo.pdf already carries one failure; drive it to the ceiling.
		for range MaxFailCount - 1 {
			writeFanoutResults(t, tmp, ingestResultsName, []map[string]any{
				{"item_index": 1, "item": "Data/Clippings/foo.pdf", "status": "FAILED"},
			})
			_, err := RecordIngestSuccess(context.Background(), nctx)
			require.NoError(t, err)
		}

		out, err := ScanIngestPending(context.Background(), nctx)
		require.NoError(t, err)
		assert.Contains(t, out, "pending=0 excluded_failed=1")
		assert.Empty(t, readNonEmptyLines(t, filepath.Join(tmp, ingestItemsName)))
	})
}

func TestRecordIngestSuccessPreservesLegacyFields(t *testing.T) {
	vault := newTestVault(t)
	useVaultEnv(t, vault)
	tmp := t.TempDir()
	path := writeVaultFile(t, vault, "Data/Clippings/clipping.md", "content")

	legacy := StateMap{"clipping.md": {
		Path:   "Data/Clippings/clipping.md",
		Status: statusIngested,
		Extra:  map[string]any{"output": "01_Raw/Entities/legacy.md"},
	}}
	require.NoError(t, SaveState(legacy, filepath.Join(vault, ".state", "ingest_state.yaml")))

	writeFanoutResults(t, tmp, ingestResultsName, []map[string]any{
		{"item_index": 1, "item": "Data/Clippings/clipping.md", "status": "SUCCEEDED", "output": "new-output"},
	})

	out, err := RecordIngestSuccess(context.Background(), newTestNodeContext(tmp))
	require.NoError(t, err)
	assert.Contains(t, out, "succeeded=1")

	updated, err := LoadState(filepath.Join(vault, ".state", "ingest_state.yaml"))
	require.NoError(t, err)
	item, ok := updated["clipping.md"]
	require.True(t, ok)
	assert.Equal(t, "01_Raw/Entities/legacy.md", item.Extra["output"],
		"legacy informational fields are preserved, not migrated")
	wantSum, err := ComputeSHA1(path)
	require.NoError(t, err)
	assert.Equal(t, wantSum, item.SHA1)
}

func TestRecordFunctionsTolerateMissingResults(t *testing.T) {
	vault := newTestVault(t)
	useVaultEnv(t, vault)
	tmp := t.TempDir() // no results files at all

	t.Run("ingest record returns zero summary", func(t *testing.T) {
		out, err := RecordIngestSuccess(context.Background(), newTestNodeContext(tmp))
		require.NoError(t, err)
		assert.Contains(t, out, "succeeded=0 failed=0")
	})

	t.Run("absorb record returns zero summary", func(t *testing.T) {
		out, err := RecordAbsorbSuccess(context.Background(), newTestNodeContext(tmp))
		require.NoError(t, err)
		assert.Contains(t, out, "files_succeeded=0 groups_succeeded=0 groups_failed=0")
	})
}

func TestAbsorbPipelineIncremental(t *testing.T) {
	vault := newTestVault(t)
	useVaultEnv(t, vault)
	tmp := t.TempDir()
	nctx := newTestNodeContext(tmp)

	writeVaultFile(t, vault, "01_Raw/Journal/2026-04-01.md", "j1")
	writeVaultFile(t, vault, "01_Raw/Journal/2026-04-02.md", "j2")
	writeVaultFile(t, vault, "01_Raw/Journal/2026-04-03.md", "j3")
	writeVaultFile(t, vault, "01_Raw/Entities/acme.md", "acme")
	writeVaultFile(t, vault, "01_Raw/Notes/misc.md", "misc")

	t.Run("initial scan groups journal batch and single entities", func(t *testing.T) {
		out, err := ScanAbsorbPending(context.Background(), nctx)
		require.NoError(t, err)
		assert.Contains(t, out, "candidates=5 pending=5 excluded_failed=0 groups=3 journal_groups=1")

		lines := readNonEmptyLines(t, filepath.Join(tmp, absorbItemsName))
		require.Len(t, lines, 3)
		assertGroup(t, lines[0], []string{
			"01_Raw/Journal/2026-04-01.md",
			"01_Raw/Journal/2026-04-02.md",
			"01_Raw/Journal/2026-04-03.md",
		})
		assertGroup(t, lines[1], []string{"01_Raw/Entities/acme.md"})
		assertGroup(t, lines[2], []string{"01_Raw/Notes/misc.md"})
		assert.NoFileExists(t, filepath.Join(vault, ".state", "absorb.lock"),
			"lock must be released after the scan")
	})

	t.Run("record settles group results per file", func(t *testing.T) {
		journalGroup := `["01_Raw/Journal/2026-04-01.md","01_Raw/Journal/2026-04-02.md","01_Raw/Journal/2026-04-03.md"]`
		writeFanoutResults(t, tmp, absorbResultsName, []map[string]any{
			{"item_index": 1, "item": journalGroup, "status": "SUCCEEDED", "output": "merged"},
			{"item_index": 2, "item": `["01_Raw/Entities/acme.md"]`, "status": "FAILED", "output": "agent error"},
			{"item_index": 3, "item": `["01_Raw/Notes/misc.md"]`, "status": "SUCCEEDED", "output": ""},
		})

		out, err := RecordAbsorbSuccess(context.Background(), nctx)
		require.NoError(t, err)
		assert.Contains(t, out, "files_succeeded=4 groups_succeeded=2 groups_failed=1")

		state, err := LoadState(filepath.Join(vault, ".state", "absorb_state.yaml"))
		require.NoError(t, err)

		wantSum, err := ComputeSHA1(filepath.Join(vault, "01_Raw", "Journal", "2026-04-02.md"))
		require.NoError(t, err)
		journal, ok := state["2026-04-02.md"]
		require.True(t, ok)
		assert.Equal(t, wantSum, journal.SHA1)
		assert.Equal(t, statusAbsorbed, journal.Status)

		acme, ok := state["acme.md"]
		require.True(t, ok)
		assert.Equal(t, 1, acme.FailCount)
	})

	t.Run("rescan only reports failed entities", func(t *testing.T) {
		out, err := ScanAbsorbPending(context.Background(), nctx)
		require.NoError(t, err)
		assert.Contains(t, out, "candidates=5 pending=1 excluded_failed=0 groups=1 journal_groups=0")
		assert.Equal(t, []string{`["01_Raw/Entities/acme.md"]`},
			readNonEmptyLines(t, filepath.Join(tmp, absorbItemsName)))
	})
}

func TestScanAbsorbPendingJournalBatching(t *testing.T) {
	vault := newTestVault(t)
	useVaultEnv(t, vault)
	tmp := t.TempDir()

	var names []string
	for i := 1; i <= 8; i++ {
		rel := fmt.Sprintf("01_Raw/Journal/2026-04-%02d.md", i)
		writeVaultFile(t, vault, rel, fmt.Sprintf("journal %d", i))
		names = append(names, rel)
	}
	writeVaultFile(t, vault, "01_Raw/Entities/solo.md", "solo")

	out, err := ScanAbsorbPending(context.Background(), newTestNodeContext(tmp))
	require.NoError(t, err)
	assert.Contains(t, out, "candidates=9 pending=9 excluded_failed=0 groups=3 journal_groups=2")

	lines := readNonEmptyLines(t, filepath.Join(tmp, absorbItemsName))
	require.Len(t, lines, 3)
	assertGroup(t, lines[0], names[:7])
	assertGroup(t, lines[1], names[7:])
	assertGroup(t, lines[2], []string{"01_Raw/Entities/solo.md"})
}

func TestScanLockAntiReentry(t *testing.T) {
	vault := newTestVault(t)
	useVaultEnv(t, vault)
	tmp := t.TempDir()
	nctx := newTestNodeContext(tmp)

	t.Run("held ingest lock blocks scan", func(t *testing.T) {
		lock := NewFileLock(filepath.Join(vault, ".state", "ingest.lock"))
		require.True(t, lock.Acquire())
		t.Cleanup(lock.Release)

		_, err := ScanIngestPending(context.Background(), nctx)
		require.Error(t, err)
		assert.ErrorContains(t, err, "ingest scan aborted")
		assert.NoFileExists(t, filepath.Join(tmp, ingestItemsName))
	})

	t.Run("held absorb lock blocks scan", func(t *testing.T) {
		lock := NewFileLock(filepath.Join(vault, ".state", "absorb.lock"))
		require.True(t, lock.Acquire())
		t.Cleanup(lock.Release)

		_, err := ScanAbsorbPending(context.Background(), nctx)
		require.Error(t, err)
		assert.ErrorContains(t, err, "absorb scan aborted")
		assert.NoFileExists(t, filepath.Join(tmp, absorbItemsName))
	})

	t.Run("concurrent call while lock is held fails", func(t *testing.T) {
		lock := NewFileLock(filepath.Join(vault, ".state", "ingest.lock"))
		require.True(t, lock.Acquire())
		t.Cleanup(lock.Release)

		errCh := make(chan error, 1)
		go func() {
			_, err := ScanIngestPending(context.Background(), nctx)
			errCh <- err
		}()

		select {
		case err := <-errCh:
			require.Error(t, err)
			assert.ErrorContains(t, err, "ingest scan aborted")
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent scan did not return while the lock was held")
		}
	})

	t.Run("released lock allows scan", func(t *testing.T) {
		lock := NewFileLock(filepath.Join(vault, ".state", "ingest.lock"))
		require.True(t, lock.Acquire())
		lock.Release()

		_, err := ScanIngestPending(context.Background(), nctx)
		require.NoError(t, err)
	})
}

func TestContextCancellation(t *testing.T) {
	t.Parallel()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	nctx := &pluginsdk.NodeContext{TmpDir: t.TempDir()}

	tests := []struct {
		name string
		fn   pluginsdk.WorkflowFunction
	}{
		{"ScanIngestPending", ScanIngestPending},
		{"RecordIngestSuccess", RecordIngestSuccess},
		{"ScanAbsorbPending", ScanAbsorbPending},
		{"RecordAbsorbSuccess", RecordAbsorbSuccess},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out, err := tt.fn(canceled, nctx)
			require.Error(t, err)
			assert.ErrorIs(t, err, context.Canceled)
			assert.Empty(t, out)
		})
	}
}

func TestVaultResolutionError(t *testing.T) {
	t.Setenv(envVaultDir, "")
	nctx := &pluginsdk.NodeContext{} // no run dirs and no tmp dir

	tests := []struct {
		name string
		fn   pluginsdk.WorkflowFunction
	}{
		{"ScanIngestPending", ScanIngestPending},
		{"RecordIngestSuccess", RecordIngestSuccess},
		{"ScanAbsorbPending", ScanAbsorbPending},
		{"RecordAbsorbSuccess", RecordAbsorbSuccess},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := tt.fn(context.Background(), nctx)
			require.Error(t, err)
			assert.ErrorContains(t, err, envVaultDir)
			assert.Empty(t, out)
		})
	}
}
