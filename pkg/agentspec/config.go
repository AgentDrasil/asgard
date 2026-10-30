package agentspec

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/AgentDrasil/asgard/agentwrapper"
)

var idRegex = regexp.MustCompile("^[a-z0-9-_]+$")

type LockMode string

const (
	LockModeExclusive LockMode = "exclusive"
	LockModeReadOnly  LockMode = "readonly"
)

type MaskMode string

const (
	MaskOthers  MaskMode = "mask_others"
	MaskMatched MaskMode = "mask_matched"
)

type SubdirMaskRule struct {
	Base   string   `yaml:"base,omitempty" json:"base,omitempty"`
	Mode   MaskMode `yaml:"mode" json:"mode"`
	KeepRW []string `yaml:"keep_rw,omitempty" json:"keep_rw,omitempty"`
	MaskRO []string `yaml:"mask_ro,omitempty" json:"mask_ro,omitempty"`
}

func (r *SubdirMaskRule) Clone() *SubdirMaskRule {
	if r == nil {
		return nil
	}
	cp := *r
	if r.KeepRW != nil {
		cp.KeepRW = append([]string(nil), r.KeepRW...)
	}
	if r.MaskRO != nil {
		cp.MaskRO = append([]string(nil), r.MaskRO...)
	}
	return &cp
}

type MountPathEntry struct {
	Path      string          `yaml:"path" json:"path"`
	MaskRules *SubdirMaskRule `yaml:"mask_rules,omitempty" json:"mask_rules,omitempty"`
}

func (e MountPathEntry) Clone() MountPathEntry {
	return MountPathEntry{
		Path:      e.Path,
		MaskRules: e.MaskRules.Clone(),
	}
}

func (e *MountPathEntry) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err == nil {
		e.Path = s
		e.MaskRules = nil
		return nil
	}
	type rawEntry MountPathEntry
	var raw rawEntry
	if err := unmarshal(&raw); err != nil {
		return err
	}
	*e = MountPathEntry(raw)
	if e.MaskRules != nil && e.MaskRules.Base == "" && e.Path != "" {
		e.MaskRules.Base = e.Path
	}
	return nil
}

type maskRulesListOrMap []SubdirMaskRule

func (m *maskRulesListOrMap) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var list []SubdirMaskRule
	listErr := unmarshal(&list)
	if listErr == nil {
		*m = list
		return nil
	}
	var mapping map[string]*SubdirMaskRule
	mapErr := unmarshal(&mapping)
	if mapErr == nil {
		keys := make([]string, 0, len(mapping))
		for k := range mapping {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		var res []SubdirMaskRule
		for _, k := range keys {
			v := mapping[k]
			if v == nil {
				return fmt.Errorf("mask_rules for %q cannot be null", k)
			}
			rule := *v
			if rule.Base == "" {
				rule.Base = k
			}
			res = append(res, rule)
		}
		*m = res
		return nil
	}
	return fmt.Errorf("failed to parse mask_rules (as list: %v, as map: %v)", listErr, mapErr)
}

type CLITarget struct {
	CLI   string `yaml:"cli"`
	Model string `yaml:"model"`
}

type MountConfig struct {
	ReadOnly         []string                   `yaml:"readonly" json:"readonly"`
	ReadWrite        []string                   `yaml:"readwrite" json:"readwrite"`
	ReadWriteEntries []MountPathEntry           `yaml:"readwrite_entries,omitempty" json:"readwrite_entries,omitempty"`
	MaskRules        map[string]*SubdirMaskRule `yaml:"mask_rules,omitempty" json:"mask_rules,omitempty"`
}

func (m *MountConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var raw struct {
		ReadOnly  []MountPathEntry   `yaml:"readonly"`
		ReadWrite []MountPathEntry   `yaml:"readwrite"`
		MaskRules maskRulesListOrMap `yaml:"mask_rules"`
	}
	if err := unmarshal(&raw); err != nil {
		return err
	}

	m.ReadOnly = nil
	m.ReadWrite = nil
	m.ReadWriteEntries = nil
	m.MaskRules = nil

	if len(raw.ReadOnly) > 0 {
		m.ReadOnly = make([]string, 0, len(raw.ReadOnly))
		for _, e := range raw.ReadOnly {
			if e.MaskRules != nil {
				return fmt.Errorf("readonly mount %q cannot have mask_rules", e.Path)
			}
			m.ReadOnly = append(m.ReadOnly, e.Path)
		}
	}

	var hasCustomEntries bool
	for _, e := range raw.ReadWrite {
		if e.MaskRules != nil {
			hasCustomEntries = true
			break
		}
	}

	if len(raw.ReadWrite) > 0 {
		m.ReadWrite = make([]string, 0, len(raw.ReadWrite))
		if hasCustomEntries {
			m.ReadWriteEntries = make([]MountPathEntry, 0, len(raw.ReadWrite))
		}
		for _, e := range raw.ReadWrite {
			m.ReadWrite = append(m.ReadWrite, e.Path)
			if hasCustomEntries {
				m.ReadWriteEntries = append(m.ReadWriteEntries, e)
			}
			if e.MaskRules != nil {
				if m.MaskRules == nil {
					m.MaskRules = make(map[string]*SubdirMaskRule)
				}
				clean := e.Path
				if e.Path != "" {
					clean = filepath.Clean(e.Path)
				}
				if clean != "" && m.MaskRules[clean] != nil {
					return fmt.Errorf("duplicate mask_rules for base %q", e.Path)
				}
				if e.MaskRules.Base == "" {
					e.MaskRules.Base = e.Path
				}
				m.MaskRules[clean] = e.MaskRules.Clone()
			}
		}
	}

	if len(raw.MaskRules) > 0 {
		if m.MaskRules == nil {
			m.MaskRules = make(map[string]*SubdirMaskRule)
		}
		for _, r := range raw.MaskRules {
			ruleCopy := r
			clean := ruleCopy.Base
			if ruleCopy.Base != "" {
				clean = filepath.Clean(ruleCopy.Base)
			}
			if ruleCopy.Base != "" && m.MaskRules[clean] != nil {
				return fmt.Errorf("duplicate mask_rules for base %q", r.Base)
			}
			m.MaskRules[clean] = ruleCopy.Clone()

			matched := false
			for _, rw := range m.ReadWrite {
				if clean != "" && filepath.Clean(rw) == clean {
					matched = true
					break
				}
			}
			for i := range m.ReadWriteEntries {
				if clean != "" && filepath.Clean(m.ReadWriteEntries[i].Path) == clean {
					if m.ReadWriteEntries[i].MaskRules == nil {
						m.ReadWriteEntries[i].MaskRules = ruleCopy.Clone()
					}
				}
			}
			if !matched && ruleCopy.Base != "" {
				m.ReadWrite = append(m.ReadWrite, ruleCopy.Base)
				if hasCustomEntries {
					m.ReadWriteEntries = append(m.ReadWriteEntries, MountPathEntry{
						Path:      ruleCopy.Base,
						MaskRules: ruleCopy.Clone(),
					})
				}
			}
		}
	}

	return nil
}

func (m MountConfig) MaskRuleFor(dir string) *SubdirMaskRule {
	if dir == "" {
		return nil
	}
	cleanDir := filepath.Clean(dir)
	if m.MaskRules != nil {
		if rule, ok := m.MaskRules[cleanDir]; ok && rule != nil {
			return rule
		}
		for k, rule := range m.MaskRules {
			if k != "" && filepath.Clean(k) == cleanDir && rule != nil {
				return rule
			}
		}
	}
	for _, entry := range m.ReadWriteEntries {
		if entry.Path != "" && filepath.Clean(entry.Path) == cleanDir && entry.MaskRules != nil {
			return entry.MaskRules
		}
	}
	return nil
}

func (m MountConfig) EffectiveReadWriteEntries() []MountPathEntry {
	var result []MountPathEntry
	seen := make(map[string]int)

	addEntry := func(path string, rule *SubdirMaskRule) {
		clean := path
		if path != "" {
			clean = filepath.Clean(path)
		}
		if idx, ok := seen[clean]; ok {
			if result[idx].MaskRules == nil && rule != nil {
				result[idx].MaskRules = rule.Clone()
			}
			return
		}
		effectiveRule := rule
		if effectiveRule == nil {
			effectiveRule = m.MaskRuleFor(path)
		}
		seen[clean] = len(result)
		result = append(result, MountPathEntry{
			Path:      path,
			MaskRules: effectiveRule.Clone(),
		})
	}

	for _, entry := range m.ReadWriteEntries {
		addEntry(entry.Path, entry.MaskRules)
	}

	for _, path := range m.ReadWrite {
		addEntry(path, nil)
	}

	var extraKeys []string
	for k := range m.MaskRules {
		clean := k
		if k != "" {
			clean = filepath.Clean(k)
		}
		if _, ok := seen[clean]; !ok {
			extraKeys = append(extraKeys, k)
		}
	}
	sort.Strings(extraKeys)
	for _, k := range extraKeys {
		rule := m.MaskRules[k]
		path := k
		if rule != nil && rule.Base != "" {
			path = rule.Base
		}
		addEntry(path, rule)
	}

	return result
}

func (m MountConfig) Clone() MountConfig {
	out := MountConfig{}
	if m.ReadOnly != nil {
		out.ReadOnly = append([]string(nil), m.ReadOnly...)
	}
	if m.ReadWrite != nil {
		out.ReadWrite = append([]string(nil), m.ReadWrite...)
	}
	if m.ReadWriteEntries != nil {
		out.ReadWriteEntries = make([]MountPathEntry, len(m.ReadWriteEntries))
		for i, e := range m.ReadWriteEntries {
			out.ReadWriteEntries[i] = e.Clone()
		}
	}
	if m.MaskRules != nil {
		out.MaskRules = make(map[string]*SubdirMaskRule, len(m.MaskRules))
		for k, v := range m.MaskRules {
			out.MaskRules[k] = v.Clone()
		}
	}
	return out
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

	// LockMode controls file locking mode: "exclusive" (default) or "readonly".
	LockMode LockMode `yaml:"lock_mode,omitempty" json:"lock_mode,omitempty"`

	// SessionMode controls whether the CLI session is resumed across calls.
	// "resume" (default): reuse the previous session ID stored in DB.
	// "fresh": always start a new session; do not persist the returned session ID.
	SessionMode string `yaml:"session_mode"`
}

func (cfg *AgentConfig) EffectiveLockMode() LockMode {
	if cfg != nil && cfg.LockMode == LockModeReadOnly {
		return LockModeReadOnly
	}
	return LockModeExclusive
}

func (cfg *AgentConfig) IsReadOnly() bool {
	return cfg != nil && cfg.EffectiveLockMode() == LockModeReadOnly
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

	switch cfg.LockMode {
	case "", LockModeExclusive, LockModeReadOnly:
		// valid
	default:
		return fmt.Errorf("invalid lock_mode: %q, must be %q or %q", cfg.LockMode, LockModeExclusive, LockModeReadOnly)
	}

	if cfg.LockMode == LockModeReadOnly {
		if len(cfg.MountDirs.ReadWrite) > 0 || len(cfg.MountDirs.EffectiveReadWriteEntries()) > 0 {
			return fmt.Errorf("agent with lock_mode %q cannot have readwrite mount dirs", cfg.LockMode)
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

	for base, rule := range cfg.MountDirs.MaskRules {
		targetBase := base
		if rule != nil && rule.Base != "" {
			targetBase = rule.Base
		}
		if targetBase == "" {
			return fmt.Errorf("mask rule base directory cannot be empty")
		}
		if !filepath.IsAbs(targetBase) {
			return fmt.Errorf("mask rule base directory must be an absolute path: %q", targetBase)
		}
		if rule != nil {
			if err := validateSubdirMaskRule(targetBase, rule); err != nil {
				return err
			}
		}
	}

	for _, entry := range cfg.MountDirs.EffectiveReadWriteEntries() {
		if entry.Path == "" {
			return fmt.Errorf("mount readwrite directory path cannot be empty")
		}
		if !filepath.IsAbs(entry.Path) {
			return fmt.Errorf("mount readwrite directory must be an absolute path: %q", entry.Path)
		}
		if entry.MaskRules != nil {
			if err := validateSubdirMaskRule(entry.Path, entry.MaskRules); err != nil {
				return err
			}
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

func validateSubdirMaskRule(base string, rule *SubdirMaskRule) error {
	if rule == nil {
		return nil
	}
	effectiveBase := rule.Base
	if effectiveBase == "" {
		effectiveBase = base
	}
	if effectiveBase == "" {
		return fmt.Errorf("mask rule base directory cannot be empty")
	}
	if !filepath.IsAbs(effectiveBase) {
		return fmt.Errorf("mask rule base directory must be an absolute path: %q", effectiveBase)
	}
	if base != "" && rule.Base != "" && filepath.Clean(base) != filepath.Clean(rule.Base) {
		return fmt.Errorf("mask rule base directory %q does not match mount path %q", rule.Base, base)
	}
	switch rule.Mode {
	case MaskOthers, MaskMatched:
		// valid
	default:
		return fmt.Errorf("invalid mask rule mode: %q, must be %q or %q", rule.Mode, MaskOthers, MaskMatched)
	}

	if rule.Mode == MaskMatched && len(rule.MaskRO) == 0 {
		return fmt.Errorf("mask rule mode %q requires at least one mask_ro pattern", rule.Mode)
	}

	for _, p := range rule.KeepRW {
		if p == "" {
			return fmt.Errorf("keep_rw pattern cannot be empty")
		}
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("invalid keep_rw pattern %q: %w", p, err)
		}
	}

	for _, p := range rule.MaskRO {
		if p == "" {
			return fmt.Errorf("mask_ro pattern cannot be empty")
		}
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("invalid mask_ro pattern %q: %w", p, err)
		}
	}

	return nil
}
