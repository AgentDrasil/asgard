// Package paths holds the shared filesystem layout for Asgard.
// In container environments, the Asgard root defaults to /asgard.
// For non-container or unprivileged development/test environments,
// it falls back to $HOME/asgard unless overridden by ASGARD_ROOT.
package paths

import (
	"os"
	"path/filepath"
	"sync"
)

const (
	DefaultRoot = "/asgard"
	EnvRoot     = "ASGARD_ROOT"
)

var (
	canWriteRootOnce sync.Once
	canWriteRoot     bool
)

func defaultRootWritable() bool {
	canWriteRootOnce.Do(func() {
		canWriteRoot = canWriteTo(DefaultRoot)
	})
	return canWriteRoot
}

func home() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return os.TempDir()
}

// canWriteTo reports whether the directory is writable, or if it does not exist,
// whether its parent directory is writable.
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

// Root is the Asgard root directory (/asgard or fallback $HOME/asgard).
func Root() string {
	if r := os.Getenv(EnvRoot); r != "" {
		return r
	}
	if defaultRootWritable() {
		return DefaultRoot
	}
	return filepath.Join(home(), "asgard")
}

// ConfigDir is the configuration directory (/asgard/config).
func ConfigDir() string { return filepath.Join(Root(), "config") }

// ConfigFile is the main configuration file (/asgard/config/config.yaml).
func ConfigFile() string { return filepath.Join(ConfigDir(), "config.yaml") }

// ProxyConfigFile is the standalone proxy configuration file
// (/asgard/config/proxy.yaml).
func ProxyConfigFile() string { return filepath.Join(ConfigDir(), "proxy.yaml") }

// KeysFile is the WebUI keybindings override file (/asgard/config/keys.yaml).
func KeysFile() string { return filepath.Join(ConfigDir(), "keys.yaml") }

// CACertFile is the proxy CA certificate (/asgard/config/ca/ca.crt).
func CACertFile() string { return filepath.Join(ConfigDir(), "ca", "ca.crt") }

// CAKeyFile is the proxy CA private key (/asgard/config/ca/ca.key).
func CAKeyFile() string { return filepath.Join(ConfigDir(), "ca", "ca.key") }

// AgentsRoot is the agent definitions root (/asgard), containing agents/ and
// teams.yaml.
func AgentsRoot() string { return Root() }

// AgentsDir is the agents directory (/asgard/agents).
func AgentsDir() string { return filepath.Join(Root(), "agents") }

// TeamsFile is the teams definition file (/asgard/teams.yaml).
func TeamsFile() string { return filepath.Join(Root(), "teams.yaml") }

// AgentsCacheDir is the directory holding agent definitions materialized from
// etcd (/asgard/agents-cache).
func AgentsCacheDir() string { return filepath.Join(Root(), "agents-cache") }

// AgentCacheDir is the materialized directory of a global agent, addressed by
// a content hash of its files (/asgard/agents-cache/<id>@<hash>), so an
// existing directory always holds identical content.
func AgentCacheDir(id, hash string) string {
	return filepath.Join(AgentsCacheDir(), id+"@"+hash)
}

// RunnerAgentCacheDir is the materialized directory of a runner-scoped agent
// (/asgard/agents-cache/runners/<nodeID>/<id>@<hash>).
func RunnerAgentCacheDir(nodeID, id, hash string) string {
	return filepath.Join(AgentsCacheDir(), "runners", nodeID, id+"@"+hash)
}
