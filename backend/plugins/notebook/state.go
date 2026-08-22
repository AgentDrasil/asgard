// Package notebook provides Go-native tooling for the Obsidian-style vault:
// incremental YAML state tracking, markdown task extraction, wiki analysis,
// and deterministic fan-out batch planning for the automated ingest/absorb
// pipelines. It is a port of the original Python scripts in tmp/Note/Scripts.
package notebook

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/goccy/go-yaml"
)

// MaxFailCount is the failure retry ceiling: files whose recorded fail_count
// reached this value are skipped by NeedsProcessing to prevent infinite
// retry storms.
const MaxFailCount = 3

// ComputeSHA1 returns the hexadecimal SHA-1 digest of the file's content.
func ComputeSHA1(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha1.Sum(data)
	return hex.EncodeToString(sum[:]), nil
}

// StateItem is the tracked processing state of a single vault file.
type StateItem struct {
	Path      string                 `yaml:"path"`
	SHA1      string                 `yaml:"sha1,omitempty"`
	Status    string                 `yaml:"status"`
	Date      string                 `yaml:"date,omitempty"`
	FailCount int                    `yaml:"fail_count,omitempty"`
	Extra     map[string]interface{} `yaml:",inline"`
}

// StateMap is keyed by file base name (filepath.Base), fully compatible with
// the YAML state files produced by the original Python scripts.
type StateMap map[string]StateItem

// LoadState reads and parses a YAML state file. A missing file yields an
// empty (non-nil) map. Known fields are decoded into StateItem; any other
// fields land in Extra, keeping full compatibility with the YAML state files
// produced by the original Python scripts.
func LoadState(stateFile string) (StateMap, error) {
	data, err := os.ReadFile(stateFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StateMap{}, nil
		}
		return nil, err
	}
	var raw map[string]map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse state file %s: %w", stateFile, err)
	}
	state := StateMap{}
	for name, entry := range raw {
		item := StateItem{
			Path:      asString(entry["path"]),
			SHA1:      asString(entry["sha1"]),
			Status:    asString(entry["status"]),
			Date:      asString(entry["date"]),
			FailCount: asInt(entry["fail_count"]),
		}
		extra := map[string]interface{}{}
		for key, value := range entry {
			if _, known := stateItemKnownFields[key]; !known {
				extra[key] = value
			}
		}
		if len(extra) > 0 {
			item.Extra = extra
		}
		state[name] = item
	}
	return state, nil
}

// stateItemKnownFields lists the StateItem fields consumed during YAML
// decoding; every other entry key is preserved in Extra.
var stateItemKnownFields = map[string]struct{}{
	"path":       {},
	"sha1":       {},
	"status":     {},
	"date":       {},
	"fail_count": {},
}

// asString renders a decoded YAML scalar as a string, with nil mapping to "".
func asString(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

// asInt converts a decoded YAML integer-like scalar to an int.
func asInt(value interface{}) int {
	switch v := value.(type) {
	case nil:
		return 0
	case uint64:
		return int(v)
	case int64:
		return int(v)
	case int:
		return v
	case uint:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

// SaveState atomically writes the state map to stateFile as YAML, creating
// parent directories as needed.
func SaveState(state StateMap, stateFile string) error {
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
		return err
	}
	normalized := make(StateMap, len(state))
	for name, item := range state {
		if len(item.Extra) == 0 {
			item.Extra = nil
		}
		normalized[name] = item
	}
	data, err := yaml.Marshal(normalized)
	if err != nil {
		return err
	}
	return writeFileAtomic(stateFile, data, 0o644)
}

// NeedsProcessing reports whether the file still requires processing: files
// unknown to the state or whose SHA-1 differs from the recorded one need
// processing, while files with FailCount >= MaxFailCount are skipped.
func NeedsProcessing(path string, state StateMap) (bool, error) {
	item, ok := state[filepath.Base(path)]
	if !ok {
		return true, nil
	}
	if item.FailCount >= MaxFailCount {
		return false, nil
	}
	current, err := ComputeSHA1(path)
	if err != nil {
		return false, err
	}
	return item.SHA1 != current, nil
}

// CollectCandidates recursively scans inputDir and returns the sorted list of
// files whose extension (case-insensitive) matches one of extensions. A
// missing inputDir yields an empty result.
func CollectCandidates(inputDir string, extensions []string) ([]string, error) {
	if _, err := os.Stat(inputDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	extSet := make(map[string]struct{}, len(extensions))
	for _, ext := range extensions {
		extSet[strings.ToLower(ext)] = struct{}{}
	}
	var candidates []string
	err := filepath.WalkDir(inputDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if _, ok := extSet[strings.ToLower(filepath.Ext(path))]; ok {
			candidates = append(candidates, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(candidates)
	return candidates, nil
}

// FindPending filters candidates down to the files that need processing.
func FindPending(candidates []string, state StateMap) ([]string, error) {
	var pending []string
	for _, candidate := range candidates {
		needed, err := NeedsProcessing(candidate, state)
		if err != nil {
			return nil, err
		}
		if needed {
			pending = append(pending, candidate)
		}
	}
	return pending, nil
}

// RecordSuccess records a successful processing of path under its base name:
// it stores the computed SHA-1, status, today's date, resets FailCount and
// merges extra fields into the inline extras.
func RecordSuccess(path string, state StateMap, status string, extra map[string]interface{}) error {
	sum, err := ComputeSHA1(path)
	if err != nil {
		return fmt.Errorf("notebook: computing sha1 for %s: %w", path, err)
	}
	item := state[filepath.Base(path)]
	item.Path = path
	item.SHA1 = sum
	item.Status = status
	item.Date = time.Now().Format("2006-01-02")
	item.FailCount = 0
	for key, value := range extra {
		if item.Extra == nil {
			item.Extra = make(map[string]interface{})
		}
		item.Extra[key] = value
	}
	state[filepath.Base(path)] = item
	return nil
}

// RecordFailure records a failed processing of path: the entry keeps only the
// path and status while incrementing FailCount. The SHA-1 is intentionally
// dropped so the file is retried on the next run until FailCount reaches
// MaxFailCount.
func RecordFailure(path string, state StateMap) {
	item := state[filepath.Base(path)]
	state[filepath.Base(path)] = StateItem{
		Path:      item.Path,
		Status:    "failed",
		FailCount: item.FailCount + 1,
	}
}

// FileLock is a process-aware PID file lock preventing concurrent double
// runs. Locks held by dead processes are detected and taken over; malformed
// lock files are treated as stale.
type FileLock struct {
	lockFile string
	acquired bool
}

// NewFileLock creates a FileLock guarding the given lock file path.
func NewFileLock(lockFile string) *FileLock {
	return &FileLock{lockFile: lockFile}
}

// Acquire attempts to take the lock, returning false when a live process
// currently holds it. It uses O_CREATE|O_EXCL to ensure atomic, race-free acquisition.
func (l *FileLock) Acquire() bool {
	dir := filepath.Dir(l.lockFile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	pidContent := []byte(strconv.Itoa(os.Getpid()) + "\n")
	for attempts := 0; attempts < 2; attempts++ {
		f, err := os.OpenFile(l.lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, writeErr := f.Write(pidContent)
			_ = f.Close()
			if writeErr != nil {
				_ = os.Remove(l.lockFile)
				return false
			}
			l.acquired = true
			return true
		}
		if !os.IsExist(err) {
			return false
		}
		// Lock file exists: check if holding PID is still alive.
		data, readErr := os.ReadFile(l.lockFile)
		if readErr != nil {
			// Failed to read, or removed concurrently: retry creation
			continue
		}
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr == nil && isPIDRunning(pid) {
			return false
		}
		// Stale or malformed lock: remove and retry
		_ = os.Remove(l.lockFile)
	}
	return false
}

// Release removes the lock file if this instance holds the lock. Removal is
// best effort: failures are ignored.
func (l *FileLock) Release() {
	if !l.acquired {
		return
	}
	_ = os.Remove(l.lockFile)
	l.acquired = false
}

// Acquired reports whether this lock instance currently holds the lock.
func (l *FileLock) Acquired() bool {
	return l.acquired
}

// isPIDRunning reports whether the PID belongs to a live process, using the
// signal-0 liveness probe (os.FindProcess + Signal(0) / syscall.Kill(pid, 0)).
func isPIDRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// writeFileAtomic writes data to path via a temporary file in the same
// directory followed by an atomic rename.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
