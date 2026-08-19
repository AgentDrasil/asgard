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

// AgentFatherID is the ID of the initial root agent required by Asgard.
// The default agent set can be cloned from https://github.com/AgentDrasil/asgard-agents.git
const AgentFatherID = "agent_father"

var allowAgentType = []string{
	"agent",
	"workflow",
}

type AgentConfig struct {
	Type        string `yaml:"type"`
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Icon        string `yaml:"icon"`
	Team        string `yaml:"team"`
	MainAgent   *bool  `yaml:"main_agent"`

	// CLI is a list of CLI targets (CLI name and model) that can be used,
	// typically ordered by preference to support quota-based fallbacks.
	CLI []CLITarget `yaml:"cli"`

	// Allow to start agents in these directories. Will mount as rw.
	RunDirs []string `yaml:"run_dirs"`

	// MountDirs configures additional directories to mount into the sandbox.
	MountDirs MountConfig `yaml:"mount_dirs"`

	// SessionMode controls whether the CLI session is resumed across calls.
	// "resume" (default): reuse the previous session ID stored in DB.
	// "fresh": always start a new session; do not persist the returned session ID.
	SessionMode string `yaml:"session_mode"`
}

// IsMainAgent returns true if MainAgent is true or nil (default).
func (cfg *AgentConfig) IsMainAgent() bool {
	if cfg.MainAgent == nil {
		return true
	}
	return *cfg.MainAgent
}

// Validate checks the AgentConfig fields for correctness.
func (cfg *AgentConfig) Validate() error {
	supportedCLIs := agentwrapper.GetSupportedCLIsAndModels()
	return cfg.ValidateWithCLIs(supportedCLIs)
}

// ValidateWithCLIs checks the AgentConfig fields for correctness against a provided map of supported CLIs and models.
func (cfg *AgentConfig) ValidateWithCLIs(supportedCLIs map[string][]string) error {
	if cfg.MainAgent == nil {
		def := true
		cfg.MainAgent = &def
	}

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
	if cfg.Icon == "" {
		cfg.Icon = "fluent-color:bot-24"
	}

	// Workflow agents orchestrate other agents and do not need CLI targets.
	if len(cfg.CLI) == 0 && cfg.Type != "workflow" {
		return fmt.Errorf("cli list cannot be empty")
	}

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
				if agentwrapper.MatchesModel(target.CLI, target.Model, m) {
					modelSupported = true
					break
				}
			}
			if !modelSupported {
				return fmt.Errorf("model %q is not supported by cli %q", target.Model, target.CLI)
			}
		}
	}

	if cfg.IsMainAgent() && len(cfg.RunDirs) == 0 {
		return fmt.Errorf("main_agent requires at least one run_dir")
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

	if cfg.Type != "" {
		validType := false
		for _, t := range allowAgentType {
			if cfg.Type == t {
				validType = true
				break
			}
		}
		if !validType {
			return fmt.Errorf("invalid agent type: %q, must be one of %v", cfg.Type, allowAgentType)
		}
	}

	switch cfg.SessionMode {
	case "", "resume", "fresh":
		// valid
	default:
		return fmt.Errorf("session_mode must be \"resume\" or \"fresh\", got %q", cfg.SessionMode)
	}

	return nil
}
