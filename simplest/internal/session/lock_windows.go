//go:build windows

package session

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockFileExclusive takes a non-blocking exclusive byte-range lock on the
// first byte of f via LockFileEx. It returns locked=false (with a nil error)
// when another process holds the lock; the caller maps that to
// ErrSessionLocked.
func lockFileExclusive(f *os.File) (locked bool, err error) {
	ol := new(windows.Overlapped)
	err = windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// unlockFile releases the lock taken by lockFileExclusive.
func unlockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
