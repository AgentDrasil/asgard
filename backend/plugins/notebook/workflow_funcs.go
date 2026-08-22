package notebook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AgentDrasil/asgard/backend/lib/workflow"
)

// Names under which the notebook functions are registered in the workflow
// default FunctionRegistry, so `type: function` nodes can invoke them via
// e.g. `function: notebook.scan_ingest_pending`.
const (
	FunctionScanIngestPending   = "notebook.scan_ingest_pending"
	FunctionRecordIngestSuccess = "notebook.record_ingest_success"
	FunctionScanAbsorbPending   = "notebook.scan_absorb_pending"
	FunctionRecordAbsorbSuccess = "notebook.record_absorb_success"
)

// Vault-relative locations and file names shared by the workflow functions.
const (
	// envVaultDir overrides vault resolution for all notebook functions.
	envVaultDir = "NOTEBOOK_VAULT_DIR"

	ingestDir         = "Data"
	absorbDir         = "01_Raw"
	ingestLockRel     = ".state/ingest.lock"
	absorbLockRel     = ".state/absorb.lock"
	ingestStateRel    = ".state/ingest_state.yaml"
	absorbStateRel    = ".state/absorb_state.yaml"
	ingestItemsName   = "ingest_items.jsonl"
	absorbItemsName   = "absorb_items.jsonl"
	ingestResultsName = "ingest_results.jsonl"
	absorbResultsName = "absorb_results.jsonl"

	statusIngested = "ingested"
	statusAbsorbed = "absorbed"
)

// supportedIngestExts lists the Data/ file extensions eligible for ingest.
var supportedIngestExts = []string{".pdf", ".docx", ".pptx", ".xlsx", ".txt", ".md", ".html"}

// init registers the notebook workflow functions in the process-wide default
// registry so `type: function` nodes can resolve them by name.
func init() {
	workflow.RegisterFunction(FunctionScanIngestPending, ScanIngestPending)
	workflow.RegisterFunction(FunctionRecordIngestSuccess, RecordIngestSuccess)
	workflow.RegisterFunction(FunctionScanAbsorbPending, ScanAbsorbPending)
	workflow.RegisterFunction(FunctionRecordAbsorbSuccess, RecordAbsorbSuccess)
}

// ScanIngestPending implements `notebook.scan_ingest_pending`: under the
// `.state/ingest.lock` guard it recursively scans `Data/` for supported file
// types, filters them against `.state/ingest_state.yaml` (skipping entries at
// the failure ceiling), and atomically writes the pending vault-relative paths
// (one per line) to `${tmp_dir}/ingest_items.jsonl` for fan-out consumption.
// It returns a one-line scan summary.
func ScanIngestPending(ctx context.Context, nctx *workflow.NodeContext) (string, error) {
	if err := checkCanceled(ctx); err != nil {
		return "", err
	}
	vaultDir, err := resolveVaultDir(nctx)
	if err != nil {
		return "", err
	}

	lockRel := filepath.Join(vaultDir, filepath.FromSlash(ingestLockRel))
	lock := NewFileLock(lockRel)
	if !lock.Acquire() {
		return "", fmt.Errorf("notebook: ingest scan aborted: lock %s is held by a live process", lockRel)
	}
	defer lock.Release()

	state, err := LoadState(filepath.Join(vaultDir, filepath.FromSlash(ingestStateRel)))
	if err != nil {
		return "", fmt.Errorf("notebook: loading ingest state: %w", err)
	}
	candidates, err := CollectCandidates(filepath.Join(vaultDir, ingestDir), supportedIngestExts)
	if err != nil {
		return "", fmt.Errorf("notebook: scanning %s: %w", ingestDir, err)
	}
	pending, excludedFailed, err := pendingRelativeFiles(vaultDir, candidates, state)
	if err != nil {
		return "", err
	}
	if err := checkCanceled(ctx); err != nil {
		return "", err
	}

	itemsPath := resolveTmpFilePath(nctx, ingestItemsName)
	if err := writeLinesAtomic(itemsPath, pending); err != nil {
		return "", fmt.Errorf("notebook: writing %s: %w", itemsPath, err)
	}
	return fmt.Sprintf("ingest scan: candidates=%d pending=%d excluded_failed=%d items_file=%s",
		len(candidates), len(pending), excludedFailed, itemsPath), nil
}

// RecordIngestSuccess implements `notebook.record_ingest_success`: it reads
// the fan-out results from `${tmp_dir}/ingest_results.jsonl` (a missing file
// is tolerated as an empty result set), records SHA-1 checkpoints for
// SUCCEEDED items into `.state/ingest_state.yaml`, increments fail_count for
// FAILED ones, and returns a settlement summary.
//
// Note on concurrency: state settlement relies on the workflow engine's
// single-instance execution constraint per workflow to avoid concurrent
// read-modify-write races on `.state/ingest_state.yaml`.
func RecordIngestSuccess(ctx context.Context, nctx *workflow.NodeContext) (string, error) {
	if err := checkCanceled(ctx); err != nil {
		return "", err
	}
	vaultDir, err := resolveVaultDir(nctx)
	if err != nil {
		return "", err
	}

	results, err := loadFanoutResults(resolveTmpFilePath(nctx, ingestResultsName))
	if err != nil {
		return "", err
	}

	state, err := LoadState(filepath.Join(vaultDir, filepath.FromSlash(ingestStateRel)))
	if err != nil {
		return "", fmt.Errorf("notebook: loading ingest state: %w", err)
	}

	succeeded, failed := 0, 0
	for _, result := range results {
		if err := checkCanceled(ctx); err != nil {
			return "", err
		}
		path, err := vaultItemPath(vaultDir, result.Item)
		if err != nil {
			return "", err
		}
		switch result.Status {
		case string(workflow.StatusSucceeded):
			if _, err := os.Stat(path); err != nil {
				// The source file vanished: keep it retryable instead of
				// recording a success without a SHA-1.
				RecordFailure(path, state)
				failed++
				continue
			}
			if err := RecordSuccess(path, state, statusIngested, nil); err != nil {
				return "", fmt.Errorf("notebook: recording ingest success for %s: %w", path, err)
			}
			succeeded++
		case string(workflow.StatusFailed):
			RecordFailure(path, state)
			failed++
		}
	}
	if err := checkCanceled(ctx); err != nil {
		return "", err
	}

	statePath := filepath.Join(vaultDir, filepath.FromSlash(ingestStateRel))
	if err := SaveState(state, statePath); err != nil {
		return "", fmt.Errorf("notebook: saving ingest state: %w", err)
	}
	return fmt.Sprintf("ingest record: succeeded=%d failed=%d state_file=%s",
		succeeded, failed, statePath), nil
}

// ScanAbsorbPending implements `notebook.scan_absorb_pending`: under the
// `.state/absorb.lock` guard it scans `01_Raw/` markdown notes, filters them
// against `.state/absorb_state.yaml` (skipping entries at the failure
// ceiling), plans deterministic absorption groups via PlanGroups, and
// atomically writes the groups (one JSON array per line) to
// `${tmp_dir}/absorb_items.jsonl` for fan-out consumption. It returns a
// grouping summary.
func ScanAbsorbPending(ctx context.Context, nctx *workflow.NodeContext) (string, error) {
	if err := checkCanceled(ctx); err != nil {
		return "", err
	}
	vaultDir, err := resolveVaultDir(nctx)
	if err != nil {
		return "", err
	}

	lockRel := filepath.Join(vaultDir, filepath.FromSlash(absorbLockRel))
	lock := NewFileLock(lockRel)
	if !lock.Acquire() {
		return "", fmt.Errorf("notebook: absorb scan aborted: lock %s is held by a live process", lockRel)
	}
	defer lock.Release()

	state, err := LoadState(filepath.Join(vaultDir, filepath.FromSlash(absorbStateRel)))
	if err != nil {
		return "", fmt.Errorf("notebook: loading absorb state: %w", err)
	}
	candidates, err := CollectCandidates(filepath.Join(vaultDir, absorbDir), []string{".md"})
	if err != nil {
		return "", fmt.Errorf("notebook: scanning %s: %w", absorbDir, err)
	}
	pending, excludedFailed, err := pendingRelativeFiles(vaultDir, candidates, state)
	if err != nil {
		return "", err
	}
	if err := checkCanceled(ctx); err != nil {
		return "", err
	}

	groups := PlanGroups(pending, 0)
	journalGroups := 0
	for _, group := range groups {
		if len(group) > 0 && hasJournalComponent(group[0]) {
			journalGroups++
		}
	}

	itemsPath := resolveTmpFilePath(nctx, absorbItemsName)
	if err := WriteFanoutItemsFile(groups, itemsPath); err != nil {
		return "", fmt.Errorf("notebook: writing %s: %w", itemsPath, err)
	}
	return fmt.Sprintf("absorb scan: candidates=%d pending=%d excluded_failed=%d groups=%d journal_groups=%d items_file=%s",
		len(candidates), len(pending), excludedFailed, len(groups), journalGroups, itemsPath), nil
}

// RecordAbsorbSuccess implements `notebook.record_absorb_success`: it reads
// the fan-out results from `${tmp_dir}/absorb_results.jsonl` (a missing file
// is tolerated as an empty result set). For SUCCEEDED groups it records
// SHA-1 checkpoints of every contained Raw file into
// `.state/absorb_state.yaml`; for FAILED groups it increments each file's
// fail_count. It returns the number of successfully settled files.
//
// Note on concurrency: state settlement relies on the workflow engine's
// single-instance execution constraint per workflow to avoid concurrent
// read-modify-write races on `.state/absorb_state.yaml`.
func RecordAbsorbSuccess(ctx context.Context, nctx *workflow.NodeContext) (string, error) {
	if err := checkCanceled(ctx); err != nil {
		return "", err
	}
	vaultDir, err := resolveVaultDir(nctx)
	if err != nil {
		return "", err
	}

	results, err := loadFanoutResults(resolveTmpFilePath(nctx, absorbResultsName))
	if err != nil {
		return "", err
	}

	state, err := LoadState(filepath.Join(vaultDir, filepath.FromSlash(absorbStateRel)))
	if err != nil {
		return "", fmt.Errorf("notebook: loading absorb state: %w", err)
	}

	filesSucceeded, groupsSucceeded, groupsFailed := 0, 0, 0
	for _, result := range results {
		if err := checkCanceled(ctx); err != nil {
			return "", err
		}
		files, err := parseGroupItem(result.Item)
		if err != nil {
			return "", err
		}
		groupOK := true
		switch result.Status {
		case string(workflow.StatusSucceeded):
			for _, rel := range files {
				path, err := vaultItemPath(vaultDir, rel)
				if err != nil {
					return "", err
				}
				if _, err := os.Stat(path); err != nil {
					// The source file vanished: keep it retryable instead of
					// recording a success without a SHA-1.
					RecordFailure(path, state)
					groupOK = false
					continue
				}
				if err := RecordSuccess(path, state, statusAbsorbed, nil); err != nil {
					return "", fmt.Errorf("notebook: recording absorb success for %s: %w", path, err)
				}
				filesSucceeded++
			}
		case string(workflow.StatusFailed):
			for _, rel := range files {
				path, err := vaultItemPath(vaultDir, rel)
				if err != nil {
					return "", err
				}
				RecordFailure(path, state)
			}
			groupOK = false
		default:
			continue
		}
		if groupOK {
			groupsSucceeded++
		} else {
			groupsFailed++
		}
	}
	if err := checkCanceled(ctx); err != nil {
		return "", err
	}

	statePath := filepath.Join(vaultDir, filepath.FromSlash(absorbStateRel))
	if err := SaveState(state, statePath); err != nil {
		return "", fmt.Errorf("notebook: saving absorb state: %w", err)
	}
	return fmt.Sprintf("absorb record: files_succeeded=%d groups_succeeded=%d groups_failed=%d state_file=%s",
		filesSucceeded, groupsSucceeded, groupsFailed, statePath), nil
}

// resolveVaultDir resolves the vault root for a node execution: the
// NOTEBOOK_VAULT_DIR environment variable wins when non-empty, then the first
// configured workflow run dir when absolute. Anything else is an explicit
// error to prevent silently scanning a wrong (e.g. empty RunDir) location.
func resolveVaultDir(nctx *workflow.NodeContext) (string, error) {
	if dir := os.Getenv(envVaultDir); dir != "" {
		return filepath.Clean(dir), nil
	}
	if len(nctx.WorkflowRunDirs) > 0 {
		dir := nctx.WorkflowRunDirs[0]
		if dir != "" && filepath.IsAbs(dir) {
			return filepath.Clean(dir), nil
		}
	}
	return "", fmt.Errorf("notebook: vault dir unresolved: set %s or configure an absolute workflow run_dir", envVaultDir)
}

// resolveTmpFilePath joins name with the node's tmp dir, falling back to a
// run-scoped temp path (or process temp dir) when the context carries none.
func resolveTmpFilePath(nctx *workflow.NodeContext, name string) string {
	if nctx != nil && nctx.TmpDir != "" {
		return filepath.Join(nctx.TmpDir, name)
	}
	if nctx != nil && nctx.RunID != "" {
		return filepath.Join(os.TempDir(), nctx.RunID+"-"+name)
	}
	return filepath.Join(os.TempDir(), name)
}

// fanoutResult mirrors one JSONL entry produced by fan-out workflow nodes
// (uppercase SUCCEEDED/FAILED statuses).
type fanoutResult struct {
	ItemIndex int    `json:"item_index"`
	Item      string `json:"item"`
	Status    string `json:"status"`
	Output    string `json:"output"`
}

// loadFanoutResults reads a fan-out results JSONL file. A missing file is
// reported as an empty result set so upstream skips or no-output runs settle
// quietly; malformed lines are hard errors.
func loadFanoutResults(path string) ([]fanoutResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("notebook: reading %s: %w", path, err)
	}
	var results []fanoutResult
	for idx, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var result fanoutResult
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			return nil, fmt.Errorf("notebook: parsing %s line %d: %w", path, idx+1, err)
		}
		results = append(results, result)
	}
	return results, nil
}

// parseGroupItem decodes an absorb fan-out item: one JSON array of
// vault-relative file paths.
func parseGroupItem(item string) ([]string, error) {
	var files []string
	if err := json.Unmarshal([]byte(item), &files); err != nil {
		return nil, fmt.Errorf("notebook: parsing absorb group item %q: %w", item, err)
	}
	return files, nil
}

// vaultItemPath maps a vault-relative item path to its absolute path,
// rejecting empty, absolute, or vault-escaping items.
func vaultItemPath(vaultDir, item string) (string, error) {
	rel := filepath.FromSlash(strings.TrimSpace(item))
	if rel == "" {
		return "", fmt.Errorf("notebook: empty vault item path")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("notebook: vault item %q must be vault-relative", item)
	}
	abs := filepath.Join(vaultDir, rel)
	under, err := filepath.Rel(vaultDir, abs)
	if err != nil || under == ".." || strings.HasPrefix(under, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("notebook: vault item %q escapes the vault root", item)
	}
	return abs, nil
}

// pendingRelativeFiles filters candidates down to the files needing
// processing, returning their vault-relative slash paths plus the number of
// candidates excluded by the failure ceiling. Files vanishing mid-scan are
// skipped.
func pendingRelativeFiles(vaultDir string, candidates []string, state StateMap) (pending []string, excludedFailed int, err error) {
	for _, candidate := range candidates {
		needed, err := NeedsProcessing(candidate, state)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, 0, fmt.Errorf("notebook: checking %s: %w", candidate, err)
		}
		if !needed {
			if item, ok := state[filepath.Base(candidate)]; ok && item.FailCount >= MaxFailCount {
				excludedFailed++
			}
			continue
		}
		rel, err := filepath.Rel(vaultDir, candidate)
		if err != nil {
			return nil, 0, fmt.Errorf("notebook: relativizing %s: %w", candidate, err)
		}
		pending = append(pending, filepath.ToSlash(rel))
	}
	return pending, excludedFailed, nil
}

// writeLinesAtomic writes one entry per line via the shared atomic
// write-then-rename helper.
func writeLinesAtomic(path string, lines []string) error {
	var buf bytes.Buffer
	for _, line := range lines {
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	return writeFileAtomic(path, buf.Bytes(), 0o644)
}

// checkCanceled converts context cancellation into a descriptive error so the
// function runner fails the node with the interruption cause.
func checkCanceled(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("notebook: interrupted: %w", err)
	}
	return nil
}
