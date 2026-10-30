// Package runner is the filesystem layout of the machine that executes agents
// and commands: the cross-session data root /session-data (per-session tmp and
// session directories, per-session CA bundles, fakebash sockets, logs).
// In container environments, it defaults to /session-data.
// For unprivileged/development/test environments, it falls back to
// $HOME/asgard/session-data unless overridden by ASGARD_SESSION_DATA.
package runner

import (
	"os"
	"path/filepath"
	"sync"
)

const (
	DefaultDataDir = "/session-data"
	EnvDataDir     = "ASGARD_SESSION_DATA"
)

var (
	canWriteDataDirOnce sync.Once
	canWriteDataDir     bool
)

func defaultDataDirWritable() bool {
	canWriteDataDirOnce.Do(func() {
		canWriteDataDir = canWriteTo(DefaultDataDir)
	})
	return canWriteDataDir
}

func home() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return os.TempDir()
}

func canWriteTo(target string) bool {
	dir := target
	for {
		fi, err := os.Stat(dir)
		if err == nil {
			if !fi.IsDir() {
				return false
			}
			testFile := filepath.Join(dir, ".asgard_probe")
			if f, err := os.OpenFile(testFile, os.O_CREATE|os.O_WRONLY, 0600); err == nil {
				_ = f.Close()
				_ = os.Remove(testFile)
				return true
			}
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// DataDir is the cross-session data root (/session-data or fallback).
func DataDir() string {
	if d := os.Getenv(EnvDataDir); d != "" {
		return d
	}
	if defaultDataDirWritable() {
		return DefaultDataDir
	}
	return filepath.Join(home(), "asgard", "session-data")
}

// LogsDir is the runner and tool log directory (/session-data/logs).
func LogsDir() string { return filepath.Join(DataDir(), "logs") }

// SessionsDir is the persistent per-session data root (/session-data/sessions).
func SessionsDir() string { return filepath.Join(DataDir(), "sessions") }

// SessionDir is the persistent directory for a session
// (/session-data/sessions/<chatID>), bound as /session in the sandbox.
func SessionDir(chatID string) string { return filepath.Join(SessionsDir(), chatID) }

// TmpDir is the session temporary data root (/session-data/tmp).
func TmpDir() string { return filepath.Join(DataDir(), "tmp") }

// SessionTmpDir is the temporary directory for a session
// (/session-data/tmp/<chatID>), bound as /tmp in the sandbox.
func SessionTmpDir(chatID string) string { return filepath.Join(TmpDir(), chatID) }

// CABundleDirName is the directory name under TmpDir() holding per-chat CA bundles.
const CABundleDirName = ".asgard-ca"

// CABundleBaseDir is the directory containing all per-session CA bundles
// (/session-data/tmp/.asgard-ca).
func CABundleBaseDir() string { return filepath.Join(TmpDir(), CABundleDirName) }

// CABundleDir is the per-session merged CA bundle directory
// (/session-data/tmp/.asgard-ca/<chatID>).
func CABundleDir(chatID string) string { return filepath.Join(CABundleBaseDir(), chatID) }

// SockDir is a named directory under the temporary root
// (/session-data/tmp/<name>), used for fakebash socket directories.
func SockDir(name string) string { return filepath.Join(TmpDir(), name) }

// AttachmentsDir is the upload attachment directory for a session
// (/session-data/tmp/<chatID>/attachments).
func AttachmentsDir(chatID string) string {
	return filepath.Join(SessionTmpDir(chatID), "attachments")
}
