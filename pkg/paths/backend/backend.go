// Package backend is the filesystem layout private to the Asgard backend
// process, below the shared Asgard root.
package backend

import (
	"path/filepath"

	"github.com/AgentDrasil/asgard/pkg/paths"
)

// DataDir is the backend state directory (/asgard/data).
func DataDir() string { return filepath.Join(paths.Root(), "data") }

// DBFile is the SQLite database file (/asgard/data/data.db).
func DBFile() string { return filepath.Join(DataDir(), "data.db") }

// AvatarsDir is the cache of locally stored user avatars (/asgard/data/avatars).
func AvatarsDir() string { return filepath.Join(DataDir(), "avatars") }

// LogsDir is the backend service logs directory (/asgard/logs).
func LogsDir() string { return filepath.Join(paths.Root(), "logs") }
