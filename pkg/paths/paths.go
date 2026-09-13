// Package paths is the single source of truth for every Asgard-owned
// filesystem path. All runtime layout is rooted at ~/asgard and is no longer
// configurable; consumers must derive paths from this package instead of
// joining $HOME by hand.
package paths

import (
	"os"
	"path/filepath"
)

// home returns the user's home directory, falling back to the OS temp
// directory when it cannot be resolved.
func home() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return os.TempDir()
}

// Root is the Asgard root directory (~/asgard).
func Root() string { return filepath.Join(home(), "asgard") }

// ConfigDir is the configuration directory (~/asgard/config).
func ConfigDir() string { return filepath.Join(Root(), "config") }

// ConfigFile is the main configuration file (~/asgard/config/config.yaml).
func ConfigFile() string { return filepath.Join(ConfigDir(), "config.yaml") }

// ProxyConfigFile is the standalone proxy configuration file
// (~/asgard/config/proxy.yaml).
func ProxyConfigFile() string { return filepath.Join(ConfigDir(), "proxy.yaml") }

// KeysFile is the WebUI keybindings override file (~/asgard/config/keys.yaml).
func KeysFile() string { return filepath.Join(ConfigDir(), "keys.yaml") }

// CACertFile is the proxy CA certificate (~/asgard/config/ca/ca.crt).
func CACertFile() string { return filepath.Join(ConfigDir(), "ca", "ca.crt") }

// CAKeyFile is the proxy CA private key (~/asgard/config/ca/ca.key).
func CAKeyFile() string { return filepath.Join(ConfigDir(), "ca", "ca.key") }

// AgentsRoot is the agent definitions root (~/asgard), containing agents/ and
// teams.yaml.
func AgentsRoot() string { return Root() }

// AgentsDir is the agents directory (~/asgard/agents).
func AgentsDir() string { return filepath.Join(Root(), "agents") }

// TeamsFile is the teams definition file (~/asgard/teams.yaml).
func TeamsFile() string { return filepath.Join(Root(), "teams.yaml") }

// LogsDir is the log directory (~/asgard/logs).
func LogsDir() string { return filepath.Join(Root(), "logs") }

// DataDir is the runtime data directory (~/asgard/data).
func DataDir() string { return filepath.Join(Root(), "data") }

// DBFile is the SQLite database file (~/asgard/data/data.db).
func DBFile() string { return filepath.Join(DataDir(), "data.db") }

// SessionsDir is the persistent per-session data root (~/asgard/data/sessions).
func SessionsDir() string { return filepath.Join(DataDir(), "sessions") }

// SessionDir is the persistent directory for a session
// (~/asgard/data/sessions/<chatID>), bound as /session in the sandbox.
func SessionDir(chatID string) string { return filepath.Join(SessionsDir(), chatID) }

// TmpDir is the session temporary data root (~/asgard/data/tmp).
func TmpDir() string { return filepath.Join(DataDir(), "tmp") }

// SessionTmpDir is the temporary directory for a session
// (~/asgard/data/tmp/<chatID>), bound as /tmp in the sandbox.
func SessionTmpDir(chatID string) string { return filepath.Join(TmpDir(), chatID) }

// CABundleDirName is the directory name under TmpDir() holding per-chat CA bundles.
const CABundleDirName = ".asgard-ca"

// CABundleBaseDir is the directory containing all per-session CA bundles
// (~/asgard/data/tmp/.asgard-ca).
func CABundleBaseDir() string {
	return filepath.Join(TmpDir(), CABundleDirName)
}

// CABundleDir is the per-session merged CA bundle directory
// (~/asgard/data/tmp/.asgard-ca/<chatID>).
func CABundleDir(chatID string) string {
	return filepath.Join(CABundleBaseDir(), chatID)
}

// SockDir is a named directory under the temporary root
// (~/asgard/data/tmp/<name>), used for fakebash socket directories.
func SockDir(name string) string { return filepath.Join(TmpDir(), name) }

// AttachmentsDir is the upload attachment directory for a session
// (~/asgard/data/tmp/<chatID>/attachments).
func AttachmentsDir(chatID string) string {
	return filepath.Join(SessionTmpDir(chatID), "attachments")
}
