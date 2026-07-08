package agents

import (
	"fmt"
	"path/filepath"
	"regexp"

	"github.com/AgentDrasil/asgard/lib/agentwrapper"
)

var idRegex = regexp.MustCompile("^[a-z0-9-_]+$")

type CLITarget struct {
	CLI   string `yaml:"cli"`
	Model string `yaml:"model"`
}

type MountConfig struct {
	ReadOnly  []string `yaml:"readonly"`
	ReadWrite []string `yaml:"readwrite"`
}

type AgentConfig struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Team        string `yaml:"team"`

	// CLI is a list of CLI targets (CLI name and model) that can be used,
	// typically ordered by preference to support quota-based fallbacks.
	CLI []CLITarget `yaml:"cli"`

	// Allow to start agents in these directories. Will mount as rw.
	RunDirs []string `yaml:"run_dirs"`

	// MountDirs configures additional directories to mount into the sandbox.
	MountDirs MountConfig `yaml:"mount_dirs"`
}

// Validate checks the AgentConfig fields for correctness.
func (cfg *AgentConfig) Validate() error {
	if cfg.ID == "" {
		return fmt.Errorf("id cannot be empty")
	}
	if !idRegex.MatchString(cfg.ID) {
		return fmt.Errorf("id must be in lowercase alphanumeric, hyphen, or underscore format: %q", cfg.ID)
	}

	if cfg.Name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if cfg.Description == "" {
		return fmt.Errorf("description cannot be empty")
	}

	if len(cfg.CLI) == 0 {
		return fmt.Errorf("cli list cannot be empty")
	}

	supportedCLIs := agentwrapper.GetSupportedCLIsAndModels()
	for _, target := range cfg.CLI {
		if target.CLI == "" {
			return fmt.Errorf("cli target name cannot be empty")
		}

		models, supported := supportedCLIs[target.CLI]
		if !supported {
			return fmt.Errorf("unsupported cli agent: %q", target.CLI)
		}

		if target.Model == "" {
			return fmt.Errorf("model for cli %q cannot be empty", target.CLI)
		}

		if len(models) > 0 {
			modelSupported := false
			for _, m := range models {
				if target.Model == m {
					modelSupported = true
					break
				}
			}
			if !modelSupported {
				return fmt.Errorf("model %q is not supported by cli %q", target.Model, target.CLI)
			}
		}
	}

	for _, dir := range cfg.RunDirs {
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("run directory must be an absolute path: %q", dir)
		}
	}

	for _, dir := range cfg.MountDirs.ReadOnly {
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("mount readonly directory must be an absolute path: %q", dir)
		}
	}

	for _, dir := range cfg.MountDirs.ReadWrite {
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("mount readwrite directory must be an absolute path: %q", dir)
		}
	}

	return nil
}
