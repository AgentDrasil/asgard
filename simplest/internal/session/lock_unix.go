//go:build !windows

package session

import (
	"errors"
	"os"
	"syscall"
)

// lockFileExclusive takes a non-blocking exclusive advisory lock on f.
// It returns locked=false (with a nil error) when another process holds the
// lock; the caller maps that to ErrSessionLocked.
func lockFileExclusive(f *os.File) (locked bool, err error) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// unlockFile releases the lock taken by lockFileExclusive.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
