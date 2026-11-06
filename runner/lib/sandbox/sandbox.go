// Package sandbox prepares the host side of the runner's sandboxes the way the
// backend does at startup: the ssh-agent, the credential-masking proxy
// (backend/lib/proxy, CA under ~/asgard/config/ca) and the sandbox
// configuration (proxy options, config path to mask) taken from the runner's
// etcd view. Runs are refused while that setup is not healthy, so a sandbox
// never starts without its masking.
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/rs/zerolog/log"

	awconfig "github.com/AgentDrasil/asgard/agentwrapper/config"

	"github.com/AgentDrasil/asgard/backend/lib/bwrap"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/proxy"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/backend/lib/sshagent"
)

// Source is the etcd view the sandbox setup is derived from (config.Remote).
type Source interface {
	SandboxConfig() (*config.Config, error)
	OnTreeChange(fn func(*config.AsgardTreeConfig))
	Tree() *config.AsgardTreeConfig
}

// Options configures Setup.
type Options struct {
	// SetupSSH starts the ssh-agent (default sshagent.SetupSSHAgent).
	SetupSSH func() error
	// ClearSSH removes all identities from the ssh-agent (default sshagent.ClearKeys).
	ClearSSH func() error
	// RemoveSSHKey removes one key file's identity from the ssh-agent (default sshagent.RemoveKey).
	RemoveSSHKey func(keyPath string) error
	// SkipSSH disables ssh-agent setup (tests).
	SkipSSH bool
	// NewProxy creates the proxy manager (default proxy.NewManager).
	NewProxy func(cfg *proxy.Config, configPath string) (*proxy.ProxyManager, error)
	// HomeDir overrides the user home directory for credential syncing (tests).
	HomeDir string
}

// Sandbox holds the live sandbox configuration and the proxy manager.
type Sandbox struct {
	opts Options

	mu   sync.Mutex
	conf *config.Config
	pm   *proxy.ProxyManager
	// cfgErr is the outcome of the latest configuration apply; the next
	// successful apply clears it.
	cfgErr error
	// proxyErr is a proxy start failure (or server exit). Only a later
	// successful start clears it, never a configuration reload.
	proxyErr error
	closed   bool
	// gen identifies the current proxy start; exits of older starts are ignored.
	gen int

	// refreshMu serializes reading the source and applying, so a stale
	// snapshot never overwrites a newer one.
	refreshMu sync.Mutex
	src       Source

	// lastSSHSynced tracks whether SSH credentials have been synced from etcd.
	lastSSHSynced bool
	// lastSSHFingerprint tracks the last synced SSH credentials fingerprint to detect actual changes.
	lastSSHFingerprint []byte
}

// Setup performs the backend's startup steps for sandboxes (ssh-agent, proxy
// manager) and keeps them in sync with etcd changes. A failure does not abort
// the runner, but Ready reports it and runs are refused.
func Setup(src Source, opts Options) *Sandbox {
	if opts.SetupSSH == nil {
		opts.SetupSSH = sshagent.SetupSSHAgent
	}
	if opts.RemoveSSHKey == nil {
		opts.RemoveSSHKey = sshagent.RemoveKey
	}
	if opts.ClearSSH == nil {
		opts.ClearSSH = sshagent.ClearKeys
	}
	if opts.NewProxy == nil {
		opts.NewProxy = func(cfg *proxy.Config, path string) (*proxy.ProxyManager, error) {
			return proxy.NewManager(cfg, path)
		}
	}
	s := &Sandbox{opts: opts, src: src}
	if !opts.SkipSSH {
		if err := opts.SetupSSH(); err != nil {
			log.Warn().Err(err).Msg("failed to setup SSH agent")
		}
	}
	// Register before reading the initial snapshot so no change is lost; every
	// refresh re-reads the latest configuration instead of trusting the event.
	src.OnTreeChange(func(tree *config.AsgardTreeConfig) { s.refresh(tree) })
	s.refresh(src.Tree())
	return s
}

// refresh reads the latest configuration from the source and applies it.
func (s *Sandbox) refresh(tree *config.AsgardTreeConfig) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if tree == nil {
		tree = s.src.Tree()
	}
	conf, err := s.src.SandboxConfig()
	s.apply(conf, tree, err)
}

// apply installs conf and brings the proxy manager in line with it.
func (s *Sandbox) apply(conf *config.Config, tree *config.AsgardTreeConfig, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if tree != nil {
		if syncErr := SyncCLICredentials(s.opts.HomeDir, &tree.Credentials); syncErr != nil {
			log.Warn().Err(syncErr).Msg("failed to sync CLI credentials to home directory")
		}
		if syncErr := SyncOpencodeAW(s.opts.HomeDir, tree.Credentials.Opencode); syncErr != nil {
			log.Warn().Err(syncErr).Msg("failed to sync opencode.aw.yaml to home directory")
		}
		if syncErr := SyncSimplestKey(s.opts.HomeDir, tree.Credentials.Simplest); syncErr != nil {
			log.Warn().Err(syncErr).Msg("failed to sync simplest key.yaml to home directory")
		}
		if syncErr := SyncSimplestCatalog(s.opts.HomeDir, tree.Simplest.Models, tree.Simplest.Providers); syncErr != nil {
			log.Warn().Err(syncErr).Msg("failed to sync simplest models/providers to home directory")
		}
		if syncErr := SyncAWConfig(s.opts.HomeDir, &tree.AW); syncErr != nil {
			log.Warn().Err(syncErr).Msg("failed to sync aw config.yaml to home directory")
		}

		hasKeys := tree.Credentials.SSH.HasKeys()
		currFP := tree.Credentials.SSH.Fingerprint()
		keyChanged := hasKeys && (!s.lastSSHSynced || !bytes.Equal(s.lastSSHFingerprint, currFP))
		keyRevoked := !hasKeys && s.lastSSHSynced

		if keyRevoked && !s.opts.SkipSSH && s.opts.ClearSSH != nil {
			if err := s.opts.ClearSSH(); err != nil {
				log.Warn().Err(err).Msg("failed to clear keys from ssh-agent")
			}
		}

		var onRemoveKey func(string)
		if !s.opts.SkipSSH && s.opts.RemoveSSHKey != nil {
			onRemoveKey = func(path string) {
				if err := s.opts.RemoveSSHKey(path); err != nil {
					log.Debug().Err(err).Str("key", path).Msg("failed to remove SSH key from ssh-agent")
				}
			}
		}
		if syncErr := syncSSHCredentials(s.opts.HomeDir, &tree.Credentials.SSH, onRemoveKey); syncErr != nil {
			log.Warn().Err(syncErr).Msg("failed to sync SSH credentials to home directory")
		} else {
			if keyChanged {
				s.lastSSHSynced = true
				s.lastSSHFingerprint = bytes.Clone(currFP)
				if !s.opts.SkipSSH && s.opts.SetupSSH != nil {
					if err := s.opts.SetupSSH(); err != nil {
						log.Warn().Err(err).Msg("failed to re-add SSH keys to ssh-agent")
					}
				}
			} else if keyRevoked {
				s.lastSSHSynced = false
				s.lastSSHFingerprint = nil
			}
		}
	}
	if err != nil {
		s.cfgErr = fmt.Errorf("sandbox configuration unavailable: %w", err)
		log.Error().Err(err).Msg("sandbox configuration unavailable; runs are refused")
		return
	}
	s.conf = conf
	s.cfgErr = nil
	switch {
	case !conf.IsProxyEnabled() && (s.pm != nil || s.proxyErr != nil):
		// Disabled by configuration: stop a running proxy and forget any
		// earlier start failure. Bumping the generation makes its exit callback
		// a no-op; with no proxy configured Ready is OK, as on the backend
		// where the sandbox proxy options are then empty.
		s.gen++
		if s.pm != nil {
			s.shutdownProxy(s.pm)
			s.pm = nil
		}
		s.proxyErr = nil
		log.Info().Msg("proxy disabled by configuration")
	case s.pm != nil:
		if err := s.pm.Reload(conf.GetProxy()); err != nil {
			s.cfgErr = fmt.Errorf("proxy reload failed: %w", err)
			log.Error().Err(err).Msg("failed to live-reload proxy configuration from etcd")
		} else {
			log.Info().Msg("proxy configuration live-reloaded successfully from etcd")
		}
		if s.proxyErr != nil && !s.pm.Running() {
			s.startProxyLocked() // retry a failed or exited server
		}
	case conf.IsProxyEnabled():
		pm, err := s.opts.NewProxy(conf.GetProxy(), conf.ResolvedProxyConfigPath())
		if err != nil {
			s.proxyErr = fmt.Errorf("proxy unavailable: %w", err)
			log.Error().Err(err).Msg("failed to initialize proxy manager; runs are refused")
			return
		}
		s.pm = pm
		s.startProxyLocked()
	}
}

// startProxyLocked binds the proxy listener (synchronously) and serves in the
// background. proxyErr is cleared exactly when the bind succeeds and set when
// it fails or the server later exits. Callers hold s.mu.
func (s *Sandbox) startProxyLocked() {
	pm := s.pm
	s.gen++
	gen := s.gen
	err := pm.StartBackground(func(err error) {
		if err == nil {
			err = errors.New("proxy server exited")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.gen == gen && !s.closed {
			s.proxyErr = fmt.Errorf("proxy server stopped: %w", err)
			log.Error().Err(err).Msg("proxy manager server stopped; runs are refused")
		}
	})
	if err != nil {
		s.proxyErr = fmt.Errorf("proxy server stopped: %w", err)
		log.Error().Err(err).Msg("proxy manager server failed to start; runs are refused")
		return
	}
	s.proxyErr = nil
}

// Conf returns the current sandbox configuration (nil until loaded).
func (s *Sandbox) Conf() *config.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conf
}

// Ready reports why runs must be refused, or nil when the sandbox setup is
// healthy: configuration applied and, if a proxy is configured, listening.
func (s *Sandbox) Ready() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.cfgErr != nil:
		return s.cfgErr
	case s.proxyErr != nil:
		return s.proxyErr
	case s.conf == nil:
		return errors.New("sandbox configuration not loaded")
	case s.pm != nil && !s.pm.Running():
		return errors.New("proxy is not listening")
	}
	return nil
}

// shutdownProxy stops pm with a bounded wait. Callers hold s.mu.
func (s *Sandbox) shutdownProxy(pm *proxy.ProxyManager) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pm.Shutdown(ctx); err != nil {
		log.Warn().Err(err).Msg("proxy manager shutdown failed")
	}
}

// Close stops the proxy manager. It is idempotent; after Close, configuration
// changes are ignored so no manager can be started or shut down twice.
func (s *Sandbox) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.gen++
	if s.pm != nil {
		s.shutdownProxy(s.pm)
		s.pm = nil
	}
}

// GitExec runs a git invocation inside a bubblewrap sandbox that holds only
// the repository (see bwrap.CommandForGit), with the runner's credential and
// proxy masking. It refuses while the sandbox setup is not healthy.
func (s *Sandbox) GitExec(ctx context.Context, spec runnerfs.GitExecSpec) (*exec.Cmd, error) {
	if err := s.Ready(); err != nil {
		return nil, fmt.Errorf("sandbox not ready: %w", err)
	}
	conf := s.Conf()
	cmd, err := bwrap.CommandForGit(ctx, bwrap.GitSandboxSpec{
		ChatID: spec.ChatID, ReadWrite: spec.ReadWrite, Network: spec.Network, Env: spec.Env,
		ConfigPath: conf.GetConfigPath(), Proxy: conf.SandboxProxyOptions(), Git: spec.Git,
	})
	if err != nil {
		return nil, err
	}
	cmd.ExtraFiles = []*os.File{spec.WorkTree}
	return cmd, nil
}

// SyncCLICredentials writes CLI credentials received from etcd to the runner user's home directory.
func SyncCLICredentials(home string, creds *config.CredentialsScope) error {
	if creds == nil {
		return nil
	}
	if home == "" {
		home = os.Getenv("HOME")
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolving home directory: %w", err)
			}
		}
	}

	configDir := filepath.Join(home, ".config")
	gcpsaPath := filepath.Join(configDir, "gcp-sa.json")
	if len(creds.GCPSAKey) > 0 {
		if err := os.MkdirAll(configDir, 0700); err != nil {
			return fmt.Errorf("create config directory %s: %w", configDir, err)
		}
		if err := os.Chmod(configDir, 0700); err != nil {
			return fmt.Errorf("chmod config directory %s: %w", configDir, err)
		}

		existing, err := os.ReadFile(gcpsaPath)
		if err != nil || !bytes.Equal(existing, creds.GCPSAKey) {
			if err := atomicWriteFile(gcpsaPath, creds.GCPSAKey, 0600); err != nil {
				return fmt.Errorf("write %s: %w", gcpsaPath, err)
			}
		} else {
			_ = os.Chmod(gcpsaPath, 0600)
		}
		_ = os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", gcpsaPath)
	} else {
		// Credential revoked / cleared: remove file if it exists and unset env if pointing to it
		if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == gcpsaPath {
			_ = os.Unsetenv("GOOGLE_APPLICATION_CREDENTIALS")
		}
		if err := os.Remove(gcpsaPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", gcpsaPath, err)
		}
	}

	return nil
}

// SyncOpencodeAW writes opencode.aw.yaml received from etcd to ~/.config/aw/opencode.aw.yaml.
func SyncOpencodeAW(home string, content []byte) error {
	if home == "" {
		home = os.Getenv("HOME")
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolving home directory: %w", err)
			}
		}
	}

	awDir := filepath.Join(home, ".config", "aw")
	awPath := filepath.Join(awDir, "opencode.aw.yaml")

	if len(content) > 0 {
		if err := os.MkdirAll(awDir, 0700); err != nil {
			return fmt.Errorf("create aw directory %s: %w", awDir, err)
		}
		if err := os.Chmod(awDir, 0700); err != nil {
			return fmt.Errorf("chmod aw directory %s: %w", awDir, err)
		}

		existing, err := os.ReadFile(awPath)
		if err != nil || !bytes.Equal(existing, content) {
			if err := atomicWriteFile(awPath, content, 0600); err != nil {
				return fmt.Errorf("write %s: %w", awPath, err)
			}
		} else {
			_ = os.Chmod(awPath, 0600)
		}
	} else {
		if err := os.Remove(awPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", awPath, err)
		}
	}

	return nil
}

// SyncSimplestKey writes simplest key.yaml received from etcd to ~/.config/simplest/key.yaml.
func SyncSimplestKey(home string, content []byte) error {
	if home == "" {
		home = os.Getenv("HOME")
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolving home directory: %w", err)
			}
		}
	}

	simplestDir := filepath.Join(home, ".config", "simplest")
	keyPath := filepath.Join(simplestDir, "key.yaml")

	if len(content) > 0 {
		if err := os.MkdirAll(simplestDir, 0700); err != nil {
			return fmt.Errorf("create simplest directory %s: %w", simplestDir, err)
		}
		if err := os.Chmod(simplestDir, 0700); err != nil {
			return fmt.Errorf("chmod simplest directory %s: %w", simplestDir, err)
		}

		existing, err := os.ReadFile(keyPath)
		if err != nil || !bytes.Equal(existing, content) {
			if err := atomicWriteFile(keyPath, content, 0600); err != nil {
				return fmt.Errorf("write %s: %w", keyPath, err)
			}
		} else {
			_ = os.Chmod(keyPath, 0600)
		}
	} else {
		if err := os.Remove(keyPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", keyPath, err)
		}
	}

	return nil
}

// SyncSimplestCatalog writes simplest's models.yaml and providers.yaml received
// from etcd to ~/.config/simplest. Empty content removes the file.
func SyncSimplestCatalog(home string, models, providers []byte) error {
	dir, err := configSubdir(home, "simplest")
	if err != nil {
		return err
	}
	if err := syncConfigFile(dir, "models.yaml", models); err != nil {
		return err
	}
	return syncConfigFile(dir, "providers.yaml", providers)
}

// SyncAWConfig writes the aw model allow-list to ~/.config/aw/config.yaml,
// where `aw models|usage` reads it. An empty allow-list removes the file.
func SyncAWConfig(home string, cfg *awconfig.Config) error {
	dir, err := configSubdir(home, "aw")
	if err != nil {
		return err
	}
	var content []byte
	if cfg != nil && len(cfg.Agents) > 0 {
		if content, err = yaml.Marshal(cfg); err != nil {
			return fmt.Errorf("encode aw config: %w", err)
		}
	}
	return syncConfigFile(dir, "config.yaml", content)
}

func configSubdir(home, name string) (string, error) {
	if home == "" {
		home = os.Getenv("HOME")
		if home == "" {
			var err error
			if home, err = os.UserHomeDir(); err != nil {
				return "", fmt.Errorf("resolving home directory: %w", err)
			}
		}
	}
	return filepath.Join(home, ".config", name), nil
}

// syncConfigFile makes dir/name hold content (mode 0600), or removes it when
// content is empty.
func syncConfigFile(dir, name string, content []byte) error {
	path := filepath.Join(dir, name)
	if len(content) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	if existing, err := os.ReadFile(path); err != nil || !bytes.Equal(existing, content) {
		if err := atomicWriteFile(path, content, 0600); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	} else {
		_ = os.Chmod(path, 0600)
	}
	return nil
}

// legacySSHFiles are the fixed names managed by versions that predate the manifest.
// They are only swept once, on the first sync after upgrade (no manifest yet).
var legacySSHFiles = []string{
	"key", "key.pub",
	"id_ed25519", "id_ed25519.pub",
	"id_rsa", "id_rsa.pub",
	"id_ecdsa", "id_ecdsa.pub",
	"id_dsa", "id_dsa.pub",
	"config", "known_hosts",
}

// readSSHManifest returns the names Asgard previously wrote to sshDir and
// whether a manifest exists.
func readSSHManifest(sshDir string) ([]string, bool, error) {
	data, err := os.ReadFile(filepath.Join(sshDir, config.SSHManifestName))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read ssh manifest: %w", err)
	}
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names, true, nil
}

// removeManagedSSH deletes files not in keep that Asgard previously wrote (manifest names;
// legacy fixed names only when no manifest exists, i.e. the one-time upgrade sweep).
// Only single-segment names are ever touched. onRemove, if set, is called with the path of
// each removed private key before it is deleted.
func removeManagedSSH(sshDir string, names []string, keep map[string][]byte, onRemove func(path string)) error {
	for _, name := range names {
		if _, ok := keep[name]; ok || name != filepath.Base(name) || name == "." || name == ".." || name == config.SSHManifestName {
			continue
		}
		target := filepath.Join(sshDir, name)
		if onRemove != nil && !strings.HasSuffix(name, ".pub") {
			if data, err := os.ReadFile(target); err == nil && bytes.Contains(data, []byte("-----BEGIN ")) {
				onRemove(target)
			}
		}
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", target, err)
		}
	}
	return nil
}

// writeSSHManifest records the given names. An empty manifest is still written so
// its existence marks that the one-time legacy sweep has already happened.
func writeSSHManifest(sshDir string, names []string) error {
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("create ssh directory %s: %w", sshDir, err)
	}
	sorted := slices.Compact(slices.Sorted(slices.Values(names)))
	content := ""
	if len(sorted) > 0 {
		content = strings.Join(sorted, "\n") + "\n"
	}
	return atomicWriteFile(filepath.Join(sshDir, config.SSHManifestName), []byte(content), 0600)
}

// SyncSSHCredentials writes SSH credentials received from etcd to the runner user's ~/.ssh directory.
// Any filename is supported; files previously written by Asgard (tracked in a manifest)
// that are no longer present are removed. Files Asgard never wrote are left alone, except for
// a one-time sweep of the legacy fixed names on the first non-empty sync after upgrade
// (when no manifest exists yet).
func SyncSSHCredentials(home string, ssh *config.SSHScope) error {
	return syncSSHCredentials(home, ssh, nil)
}

// syncSSHCredentials is SyncSSHCredentials with a hook called with the path of each private
// key that is about to be deleted while other credentials remain (for ssh-agent revocation).
func syncSSHCredentials(home string, ssh *config.SSHScope, onRemoveKey func(path string)) error {
	if home == "" {
		home = os.Getenv("HOME")
		if home == "" {
			var err error
			home, err = os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolving home directory: %w", err)
			}
		}
	}

	sshDir := filepath.Join(home, ".ssh")
	allFiles := ssh.AllFiles()
	managed, hasManifest, err := readSSHManifest(sshDir)
	if err != nil {
		return err
	}
	candidates := managed
	if !hasManifest {
		// One-time upgrade sweep of the legacy fixed names.
		candidates = legacySSHFiles
	}

	if ssh == nil || (!ssh.HasKeys() && len(allFiles["config"]) == 0 && len(allFiles["known_hosts"]) == 0) {
		// Revocation: remove all managed files (manifest names, or legacy names once).
		if err := removeManagedSSH(sshDir, candidates, nil, onRemoveKey); err != nil {
			return err
		}
		return writeSSHManifest(sshDir, nil)
	}

	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("create ssh directory %s: %w", sshDir, err)
	}
	if err := os.Chmod(sshDir, 0700); err != nil {
		return fmt.Errorf("chmod ssh directory %s: %w", sshDir, err)
	}

	// Record old + new names before writing so a partial failure cannot orphan files.
	newNames := make([]string, 0, len(allFiles))
	for name := range allFiles {
		newNames = append(newNames, name)
	}
	if err := writeSSHManifest(sshDir, append(slices.Clone(candidates), newNames...)); err != nil {
		return err
	}

	privKeys := ssh.PrivateKeys()

	for name, data := range allFiles {
		targetPath := filepath.Join(sshDir, name)
		perm := os.FileMode(0644)
		if _, isPriv := privKeys[name]; isPriv {
			perm = 0600
		}

		existing, err := os.ReadFile(targetPath)
		if err != nil || !bytes.Equal(existing, data) {
			// Rotation: revoke the old identity from the agent before overwriting it.
			if err == nil && onRemoveKey != nil && !strings.HasSuffix(name, ".pub") && bytes.Contains(existing, []byte("-----BEGIN ")) {
				onRemoveKey(targetPath)
			}
			if err := atomicWriteFile(targetPath, data, perm); err != nil {
				return fmt.Errorf("write %s: %w", targetPath, err)
			}
		} else {
			_ = os.Chmod(targetPath, perm)
		}
	}

	if err := removeManagedSSH(sshDir, candidates, allFiles, onRemoveKey); err != nil {
		return err
	}
	return writeSSHManifest(sshDir, newNames)
}

func atomicWriteFile(target string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, filepath.Base(target)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}
