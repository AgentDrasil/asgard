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
	"sync"
	"time"

	"github.com/rs/zerolog/log"

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
}

// Setup performs the backend's startup steps for sandboxes (ssh-agent, proxy
// manager) and keeps them in sync with etcd changes. A failure does not abort
// the runner, but Ready reports it and runs are refused.
func Setup(src Source, opts Options) *Sandbox {
	if opts.SetupSSH == nil {
		opts.SetupSSH = sshagent.SetupSSHAgent
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

	geminiDir := filepath.Join(home, ".gemini")
	oauthPath := filepath.Join(geminiDir, "oauth_creds.json")
	if len(creds.OAuthCreds) > 0 {
		if err := os.MkdirAll(geminiDir, 0700); err != nil {
			return fmt.Errorf("create gemini directory %s: %w", geminiDir, err)
		}
		if err := os.Chmod(geminiDir, 0700); err != nil {
			return fmt.Errorf("chmod gemini directory %s: %w", geminiDir, err)
		}

		existing, err := os.ReadFile(oauthPath)
		if err != nil || !bytes.Equal(existing, creds.OAuthCreds) {
			if err := atomicWriteFile(oauthPath, creds.OAuthCreds, 0600); err != nil {
				return fmt.Errorf("write %s: %w", oauthPath, err)
			}
		} else {
			_ = os.Chmod(oauthPath, 0600)
		}
	} else {
		// Credential revoked / cleared: remove file if it exists
		if err := os.Remove(oauthPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", oauthPath, err)
		}
	}

	agyDir := filepath.Join(geminiDir, "antigravity-cli")
	agyPath := filepath.Join(agyDir, "antigravity-oauth-token")
	if len(creds.AntigravityOAuthToken) > 0 {
		if err := os.MkdirAll(agyDir, 0700); err != nil {
			return fmt.Errorf("create antigravity-cli directory %s: %w", agyDir, err)
		}
		if err := os.Chmod(agyDir, 0700); err != nil {
			return fmt.Errorf("chmod antigravity-cli directory %s: %w", agyDir, err)
		}

		existing, err := os.ReadFile(agyPath)
		if err != nil || !bytes.Equal(existing, creds.AntigravityOAuthToken) {
			if err := atomicWriteFile(agyPath, creds.AntigravityOAuthToken, 0600); err != nil {
				return fmt.Errorf("write %s: %w", agyPath, err)
			}
		} else {
			_ = os.Chmod(agyPath, 0600)
		}
	} else {
		// Credential revoked / cleared: remove file if it exists
		if err := os.Remove(agyPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", agyPath, err)
		}
	}

	return nil
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
