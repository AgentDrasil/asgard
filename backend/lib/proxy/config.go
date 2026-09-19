package proxy

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/AgentDrasil/asgard/pkg/paths"
)

// ServerConfig holds the proxy server network and CA configuration. The CA
// paths are internal fixed defaults (~/asgard/config/ca) and are not settable
// from proxy.yaml; the fields exist so the proxy manager can be constructed
// with explicit paths in tests.
type ServerConfig struct {
	Addr   string `yaml:"addr" json:"addr"`
	CACert string `yaml:"-" json:"ca_cert"`
	CAKey  string `yaml:"-" json:"ca_key"`
}

// DebugConfig controls proxy debugging and request dumping options.
type DebugConfig struct {
	Enable       bool  `yaml:"enable" json:"enable"`
	DumpHeaders  bool  `yaml:"dump_headers" json:"dump_headers"`
	DumpBody     bool  `yaml:"dump_body" json:"dump_body"`
	MaxBodyBytes int64 `yaml:"max_body_bytes" json:"max_body_bytes"`
}

// Rule defines an interception and credential substitution rule.
type Rule struct {
	Host        string `yaml:"host" json:"host"`
	PathPrefix  string `yaml:"path_prefix" json:"path_prefix"`
	HeaderKey   string `yaml:"header_key" json:"header_key"`
	RealSecret  string `yaml:"real_secret" json:"real_secret"`
	DummySecret string `yaml:"dummy_secret" json:"dummy_secret"`
	Env         string `yaml:"env" json:"env"`
}

// Config represents the full proxy configuration.
type Config struct {
	Enable bool         `yaml:"enable" json:"enable"`
	Server ServerConfig `yaml:"server" json:"server"`
	Debug  DebugConfig  `yaml:"debug" json:"debug"`
	Rules  []Rule       `yaml:"rules" json:"rules"`
}

// DefaultConfig returns a Config populated with default values.
func DefaultConfig() *Config {
	cfg := &Config{
		Enable: false,
		Server: ServerConfig{
			Addr:   "127.0.0.1:8082",
			CACert: paths.CACertFile(),
			CAKey:  paths.CAKeyFile(),
		},
		Debug: DebugConfig{
			Enable:       false,
			DumpHeaders:  false,
			DumpBody:     false,
			MaxBodyBytes: 4096,
		},
		Rules: []Rule{},
	}
	cfg.ApplyDefaults()
	return cfg
}

// ParseConfig unmarshals YAML data into Config, applies defaults, and validates.
func ParseConfig(data []byte) (*Config, error) {
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal proxy config: %w", err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadConfigFile reads a YAML file and parses it as Config.
func LoadConfigFile(filePath string) (*Config, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read proxy config file %s: %w", filePath, err)
	}
	return ParseConfig(data)
}

// ApplyDefaults populates default server address, CA paths, and debug limits.
func (c *Config) ApplyDefaults() {
	if c == nil {
		return
	}
	if c.Server.Addr == "" {
		c.Server.Addr = "127.0.0.1:8082"
	}
	if c.Server.CACert == "" {
		c.Server.CACert = paths.CACertFile()
	}
	if c.Server.CAKey == "" {
		c.Server.CAKey = paths.CAKeyFile()
	}

	if c.Debug.MaxBodyBytes <= 0 {
		c.Debug.MaxBodyBytes = 4096
	}
}

// Validate validates the proxy configuration.
// Fail-Closed core defense: If Enable == true, Rules must not be empty.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}

	if c.Enable {
		if len(c.Rules) == 0 {
			return fmt.Errorf("proxy is enabled but rules list is empty")
		}
	}

	for i, r := range c.Rules {
		if strings.TrimSpace(r.Host) == "" {
			return fmt.Errorf("rule [%d]: missing host", i)
		}
		if strings.TrimSpace(r.HeaderKey) == "" {
			return fmt.Errorf("rule [%d]: missing header_key", i)
		}
		if strings.TrimSpace(r.RealSecret) == "" {
			return fmt.Errorf("rule [%d]: missing real_secret", i)
		}
	}

	return nil
}

// ProxyHost returns the standard HTTP proxy URL format.
func (c *Config) ProxyHost() string {
	if c == nil || c.Server.Addr == "" {
		return "http://127.0.0.1:8082"
	}
	addr := c.Server.Addr
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		return "http://" + addr
	}
	return addr
}

// ResolvedCACertPath returns the CA certificate path.
func (c *Config) ResolvedCACertPath() string {
	if c == nil {
		return ""
	}
	return c.Server.CACert
}

// ResolvedCAKeyPath returns the CA private key path.
func (c *Config) ResolvedCAKeyPath() string {
	if c == nil {
		return ""
	}
	return c.Server.CAKey
}

// EnvVars returns a map of environment variable names to dummy secrets configured in rules.
// If multiple rules define the same env var, the later rule's dummy secret takes precedence.
func (c *Config) EnvVars() map[string]string {
	if c == nil || len(c.Rules) == 0 {
		return nil
	}
	envs := make(map[string]string)
	for _, r := range c.Rules {
		envKey := strings.TrimSpace(r.Env)
		if envKey != "" {
			envs[envKey] = r.DummySecret
		}
	}
	if len(envs) == 0 {
		return nil
	}
	return envs
}

// EnvNames returns a sorted, deduplicated slice of environment variable names configured in rules.
func (c *Config) EnvNames() []string {
	if c == nil || len(c.Rules) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var names []string
	for _, r := range c.Rules {
		envKey := strings.TrimSpace(r.Env)
		if envKey != "" {
			if _, exists := seen[envKey]; !exists {
				seen[envKey] = struct{}{}
				names = append(names, envKey)
			}
		}
	}
	sort.Strings(names)
	return names
}
