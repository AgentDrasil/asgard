package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"

	"github.com/goccy/go-yaml"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/auth"
	"github.com/AgentDrasil/asgard/backend/lib/bwrap"
	"github.com/AgentDrasil/asgard/backend/lib/proxy"
	"github.com/AgentDrasil/asgard/pkg/paths"
)

type FirebaseWebpushWebConfig struct {
	APIKey            string `yaml:"api_key" json:"apiKey"`
	AuthDomain        string `yaml:"auth_domain" json:"authDomain"`
	ProjectID         string `yaml:"project_id" json:"projectId"`
	StorageBucket     string `yaml:"storage_bucket" json:"storageBucket"`
	MessagingSenderID string `yaml:"messaging_sender_id" json:"messagingSenderId"`
	AppID             string `yaml:"app_id" json:"appId"`
	VapidKey          string `yaml:"vapid_key" json:"vapidKey"`
}

const DefaultLanguage = "English (US)"

// DefaultRunnerRegistryPort is the registry_port used when the runner section
// leaves it unset.
const DefaultRunnerRegistryPort = 9311

// RunnerConfig configures the mTLS listener runners connect to.
type RunnerConfig struct {
	TLSCertFile  string `yaml:"tls_cert_file"`
	TLSKeyFile   string `yaml:"tls_key_file"`
	ClientCAFile string `yaml:"client_ca_file"`
	RegistryPort int    `yaml:"registry_port"`
	// AllowedNodes lists the runner ids (client certificate CNs) allowed to
	// connect. Empty is valid and means no runner may connect.
	AllowedNodes []string `yaml:"allowed_nodes"`
}

// ApplyDefaultsAndValidate applies defaults to and validates the runner
// section.
func (r *RunnerConfig) ApplyDefaultsAndValidate() error {
	if r.RegistryPort == 0 {
		r.RegistryPort = DefaultRunnerRegistryPort
	}
	if r.RegistryPort < 1 || r.RegistryPort > 65535 {
		return fmt.Errorf("invalid runner.registry_port %d", r.RegistryPort)
	}
	if r.TLSCertFile == "" {
		return fmt.Errorf("missing runner.tls_cert_file")
	}
	if r.TLSKeyFile == "" {
		return fmt.Errorf("missing runner.tls_key_file")
	}
	if r.ClientCAFile == "" {
		return fmt.Errorf("missing runner.client_ca_file")
	}
	seen := make(map[string]bool, len(r.AllowedNodes))
	nodes := make([]string, 0, len(r.AllowedNodes))
	for _, n := range r.AllowedNodes {
		if n == "" {
			return fmt.Errorf("runner.allowed_nodes contains an empty node id")
		}
		if !seen[n] {
			seen[n] = true
			nodes = append(nodes, n)
		}
	}
	r.AllowedNodes = nodes
	return nil
}

type Config struct {
	Debug bool   `yaml:"debug"`
	DB    string `yaml:"db"`
	DSN   string `yaml:"dsn"`
	// AgentDir is the fixed agent definitions root (~/asgard). It is not
	// configurable; the field is exported so tests can inject a temp root.
	AgentDir                           string                    `yaml:"-" json:"-"`
	Port                               int                       `yaml:"port"`
	Host                               string                    `yaml:"host"`
	WebUIPath                          string                    `yaml:"webui_path"`
	GeminiAPIKey                       string                    `yaml:"gemini_api_key"`
	GeminiModelForChatTitle            string                    `yaml:"gemini_model_for_chat_title"`
	GeminiModelForCommandResultCompass string                    `yaml:"gemini_model_for_command_result_compass"`
	TypesafeAPIKey                     string                    `yaml:"typesafe_api_key"`
	FirebaseWebpushWeb                 *FirebaseWebpushWebConfig `yaml:"firebase_webpush_web"`
	ChatLang                           string                    `yaml:"chat_lang"`
	DocLang                            string                    `yaml:"doc_lang"`
	CommentLang                        string                    `yaml:"comment_lang"`
	UILang                             string                    `yaml:"ui_lang"`
	Providers                          []string                  `yaml:"providers" json:"providers,omitempty"`
	Proxy                              *proxy.Config             `yaml:"proxy" json:"proxy,omitempty"`
	KMSKeyName                         string                    `yaml:"kms_key_name" json:"kms_key_name,omitempty"`
	// Runner enables the runner listener. A nil value leaves it disabled.
	Runner *RunnerConfig `yaml:"runner" json:"-"`
	// Auth enables optional OIDC/OAuth authentication. A nil value leaves every
	// API route unauthenticated.
	Auth       *auth.Config `yaml:"auth" json:"auth,omitempty"`
	ConfigPath string       `yaml:"-" json:"-"`
	proxyMu    sync.RWMutex `yaml:"-" json:"-"`
	runnerMu   sync.RWMutex `yaml:"-" json:"-"`
}

// GetRunner returns a copy of the runner section (nil when not configured).
func (c *Config) GetRunner() *RunnerConfig {
	if c == nil {
		return nil
	}
	c.runnerMu.RLock()
	defer c.runnerMu.RUnlock()
	if c.Runner == nil {
		return nil
	}
	cp := *c.Runner
	cp.AllowedNodes = append([]string(nil), c.Runner.AllowedNodes...)
	return &cp
}

// SetRunner replaces the runner section (used on live config reload).
func (c *Config) SetRunner(r *RunnerConfig) {
	if c == nil {
		return
	}
	c.runnerMu.Lock()
	c.Runner = r
	c.runnerMu.Unlock()
}

// AllowedRunnerNodes returns the currently allowed runner ids.
func (c *Config) AllowedRunnerNodes() []string {
	if r := c.GetRunner(); r != nil {
		return r.AllowedNodes
	}
	return nil
}

// AuthConfig returns the optional authentication configuration (nil when auth
// is disabled).
func (c *Config) AuthConfig() *auth.Config {
	if c == nil {
		return nil
	}
	return c.Auth
}

var SupportedUILangs = []string{"en", "zh-CN"}

var SupportedProviders = []string{"agy", "claude", "opencode", "simplest"}

func isSupportedProvider(name string) bool {
	for _, p := range SupportedProviders {
		if p == name {
			return true
		}
	}
	return false
}

func (c *Config) GetProviders() []string {
	if c == nil || len(c.Providers) == 0 {
		return append([]string(nil), SupportedProviders...)
	}
	return append([]string(nil), c.Providers...)
}

func (c *Config) IsProviderEnabled(provider string) bool {
	if c == nil || len(c.Providers) == 0 {
		return true
	}
	for _, p := range c.Providers {
		if p == provider {
			return true
		}
	}
	return false
}

func (c *Config) GetConfigPath() string {
	if c == nil {
		return ""
	}
	return c.ConfigPath
}

func (c *Config) GetAgentDir() string {
	if c == nil || c.AgentDir == "" {
		return paths.AgentsRoot()
	}
	return c.AgentDir
}

func (c *Config) GetChatLang() string {
	if c == nil || c.ChatLang == "" {
		return DefaultLanguage
	}
	return c.ChatLang
}

func (c *Config) GetDocLang() string {
	if c == nil || c.DocLang == "" {
		return DefaultLanguage
	}
	return c.DocLang
}

func (c *Config) GetCommentLang() string {
	if c == nil || c.CommentLang == "" {
		return DefaultLanguage
	}
	return c.CommentLang
}

func (c *Config) GetUILang() string {
	if c == nil || c.UILang == "" {
		return "en"
	}
	return c.UILang
}

func (c *Config) GetGeminiModelForCommandResultCompass() string {
	if c == nil || c.GeminiModelForCommandResultCompass == "" {
		return "gemini-3.5-flash-lite"
	}
	return c.GeminiModelForCommandResultCompass
}

func (c *Config) GetGeminiAPIKey() string {
	if c != nil && c.GeminiAPIKey != "" {
		return c.GeminiAPIKey
	}
	return os.Getenv("GEMINI_API_KEY")
}

func (c *Config) GetTypesafeAPIKey() string {
	if c != nil && c.TypesafeAPIKey != "" {
		return c.TypesafeAPIKey
	}
	return os.Getenv("TYPESAFE_API_KEY")
}

func (c *Config) LanguageRules() string {
	return fmt.Sprintf(`## Language Preferences

- Responses/Conversations: %s
- Documents and Artifacts: %s
- Code Comments and Docstrings: %s`, c.GetChatLang(), c.GetDocLang(), c.GetCommentLang())
}

func (c *Config) GetProxy() *proxy.Config {
	if c == nil {
		return nil
	}
	c.proxyMu.RLock()
	defer c.proxyMu.RUnlock()
	return c.Proxy
}

func (c *Config) SetProxy(cfg *proxy.Config) {
	if c == nil {
		return
	}
	c.proxyMu.Lock()
	c.Proxy = cfg
	c.proxyMu.Unlock()
}

func (c *Config) IsProxyEnabled() bool {
	if c == nil {
		return false
	}
	p := c.GetProxy()
	return p != nil && p.Enable
}

// ProxyEnvNames returns a sorted, deduplicated slice of environment variable names
// configured in proxy rules if proxy is enabled.
func (c *Config) ProxyEnvNames() []string {
	if c == nil {
		return nil
	}
	p := c.GetProxy()
	if p == nil || !p.Enable {
		return nil
	}
	return p.EnvNames()
}

// SandboxProxyOptions builds the bwrap proxy sandbox options for this config.
// It returns a disabled config when the proxy is not enabled.
func (c *Config) SandboxProxyOptions() bwrap.ProxySandboxConfig {
	var typesafeKey string
	var geminiKey string
	var compassModel string
	if c != nil {
		typesafeKey = c.GetTypesafeAPIKey()
		geminiKey = c.GetGeminiAPIKey()
		compassModel = c.GetGeminiModelForCommandResultCompass()
	}

	p := c.GetProxy()
	if p == nil || !p.Enable {
		log.Debug().Msg("sandbox proxy options: proxy is disabled or nil")
		return bwrap.ProxySandboxConfig{
			TypesafeAPIKey: typesafeKey,
			GeminiAPIKey:   geminiKey,
			CompassModel:   compassModel,
		}
	}
	opts := bwrap.ProxySandboxConfig{
		Enabled:         true,
		ProxyAddr:       p.ProxyHost(),
		CACert:          p.ResolvedCACertPath(),
		CAKey:           p.ResolvedCAKeyPath(),
		ProxyConfigPath: c.ResolvedProxyConfigPath(),
		EnvVars:         p.EnvVars(),
		TypesafeAPIKey:  typesafeKey,
		GeminiAPIKey:    geminiKey,
		CompassModel:    compassModel,
	}
	log.Debug().
		Bool("enabled", opts.Enabled).
		Str("proxy_addr", opts.ProxyAddr).
		Strs("env_names", p.EnvNames()).
		Msg("sandbox proxy options built")
	return opts
}

func (c *Config) ProxyHost() string {
	p := c.GetProxy()
	if c == nil || p == nil {
		return "http://127.0.0.1:8082"
	}
	return p.ProxyHost()
}

func (c *Config) ProxyAddr() string {
	p := c.GetProxy()
	if c == nil || p == nil || p.Server.Addr == "" {
		return "127.0.0.1:8082"
	}
	return p.Server.Addr
}

func (c *Config) ProxyCACertPath() string {
	p := c.GetProxy()
	if c == nil || p == nil {
		return ""
	}
	return p.ResolvedCACertPath()
}

func (c *Config) ProxyCAKeyPath() string {
	p := c.GetProxy()
	if c == nil || p == nil {
		return ""
	}
	return p.ResolvedCAKeyPath()
}

// ResolvedProxyConfigPath returns the fixed standalone proxy config path
// (~/asgard/config/proxy.yaml).
func (c *Config) ResolvedProxyConfigPath() string {
	if c == nil {
		return ""
	}
	return paths.ProxyConfigFile()
}

func (c *Config) validate() error {
	if c.Host == "" {
		return fmt.Errorf("missing host")
	}
	if c.DB != "pg" && c.DB != "sqlite" {
		return fmt.Errorf("invalid db: %s, must be 'pg' or 'sqlite'", c.DB)
	}
	if c.DB == "pg" && c.DSN == "" {
		return fmt.Errorf("missing dsn")
	}

	if c.GeminiAPIKey == "" {
		return fmt.Errorf("missing gemini_api_key")
	}
	if c.GeminiModelForChatTitle == "" {
		return fmt.Errorf("missing gemini_model_for_chat_title")
	}

	if c.UILang != "" && c.UILang != "en" && c.UILang != "zh-CN" {
		return fmt.Errorf("invalid ui_lang %q, must be 'en' or 'zh-CN'", c.UILang)
	}

	if c.AgentDir == "" {
		c.AgentDir = paths.AgentsRoot()
	}

	for _, p := range c.Providers {
		if !isSupportedProvider(p) {
			return fmt.Errorf("unsupported provider %q, must be one of %v", p, SupportedProviders)
		}
	}

	if c.Auth != nil {
		if err := c.Auth.Validate(); err != nil {
			return err
		}
		// auth builds the OAuth redirect URI from host, which must therefore be
		// the externally reachable absolute URL.
		if u, err := url.Parse(c.Host); err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("host must be an absolute URL like https://asgard.example.com when auth is enabled, got %q", c.Host)
		}
	}

	return nil
}

// ParseAndValidate unmarshals configuration YAML data, applies defaults, and validates contents.
// ParseAndValidate unmarshals configuration YAML data, applies defaults, and validates contents.
func ParseAndValidate(data []byte) (*Config, error) {
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if err := cfg.ApplyDefaultsAndValidate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg, err := ParseAndValidate(data)
	if err != nil {
		return nil, err
	}
	cfg.ConfigPath = path

	if cfg.Proxy == nil {
		resolvedProxyPath := paths.ProxyConfigFile()
		if _, statErr := os.Stat(resolvedProxyPath); statErr == nil {
			proxyCfg, pErr := proxy.LoadConfigFile(resolvedProxyPath)
			if pErr != nil {
				if os.IsPermission(pErr) || errors.Is(pErr, os.ErrPermission) {
					log.Warn().Err(pErr).Str("path", resolvedProxyPath).Msg("proxy config exists but is not accessible due to permission error; proxy disabled")
				} else {
					return nil, fmt.Errorf("failed to load proxy config from %s: %w", resolvedProxyPath, pErr)
				}
			} else if proxyCfg != nil {
				cfg.Proxy = proxyCfg
				log.Info().
					Str("path", resolvedProxyPath).
					Bool("enable", proxyCfg.Enable).
					Int("rules_count", len(proxyCfg.Rules)).
					Msg("loaded proxy config")
			}
		}
	}

	return cfg, nil
}
