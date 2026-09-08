package common

import (
	"fmt"
	"os"
	"strings"
)

// FileTakeover records an ambient file's previous content and permission bits
// so the aw dispatcher can transiently replace the file's content and later
// restore the original state (or remove the file again when it did not exist
// before).
type FileTakeover struct {
	path     string
	previous []byte
	existed  bool
	prevMode os.FileMode
}

// BeginFileTakeover reads path and records its state for a later Restore.
//
// It tolerates the hostile states a host user's ambient instruction file
// (e.g. ~/.gemini/GEMINI.md or ~/.config/opencode/AGENTS.md, both bound
// read-write into the sandbox) can be in:
//
//   - Read-only: users sometimes chmod their CLI instruction files to keep
//     the CLI from editing them. The file is chmod'ed owner-writable for the
//     duration of the takeover; Restore puts the original mode back. Without
//     this, overwriting the file fails with EACCES and kills the whole run.
//   - Leftover from a previous crashed run (content starts with
//     ManagedMarker): treated as originally absent, so Restore removes it
//     instead of preserving the stale takeover as "user content".
//
// A missing file is recorded as non-existent; a missing parent directory is
// an error (callers create the directory up front).
func BeginFileTakeover(path string) (*FileTakeover, error) {
	t := &FileTakeover{path: path}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if !strings.HasPrefix(string(data), ManagedMarker) {
			t.previous = data
			t.existed = true
		}
	case os.IsNotExist(err):
		// Recorded as absent; Restore removes the file again.
	default:
		return nil, fmt.Errorf("reading %q: %w", path, err)
	}

	if st, err := os.Stat(path); err == nil {
		mode := st.Mode().Perm()
		if t.existed {
			t.prevMode = mode
		}
		if mode&0200 == 0 {
			if err := os.Chmod(path, 0600); err != nil {
				return nil, fmt.Errorf("making read-only file %q writable for the run: %w", path, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat %q: %w", path, err)
	}
	return t, nil
}

// Restore undoes a completed takeover: it writes back the previous content
// and mode, or removes the file when it did not exist before (or was only a
// crashed run's leftover) and still carries the managed marker.
func (t *FileTakeover) Restore() {
	if !t.existed {
		data, err := os.ReadFile(t.path)
		if err != nil {
			return
		}
		if strings.HasPrefix(string(data), ManagedMarker) {
			_ = os.Remove(t.path)
		}
		return
	}
	if err := os.WriteFile(t.path, t.previous, 0600); err != nil {
		return
	}
	if t.prevMode != 0 {
		_ = os.Chmod(t.path, t.prevMode)
	}
}
