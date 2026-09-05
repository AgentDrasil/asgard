package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/AgentDrasil/asgard/backend/lib/proxy"
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

type Config struct {
	Debug                   bool                      `yaml:"debug"`
	DB                      string                    `yaml:"db"`
	DSN                     string                    `yaml:"dsn"`
	AgentDir                string                    `yaml:"agent_dir"`
	Port                    int                       `yaml:"port"`
	InternalPort            int                       `yaml:"internal_port"`
	Host                    string                    `yaml:"host"`
	WebUIPath               string                    `yaml:"webui_path"`
	GeminiAPIKey            string                    `yaml:"gemini_api_key"`
	GeminiModelForChatTitle string                    `yaml:"gemini_model_for_chat_title"`
	FirebaseWebpushWeb      *FirebaseWebpushWebConfig `yaml:"firebase_webpush_web"`
	ChatLang                string                    `yaml:"chat_lang"`
	DocLang                 string                    `yaml:"doc_lang"`
	CommentLang             string                    `yaml:"comment_lang"`
	UILang                  string                    `yaml:"ui_lang"`
	Providers               []string                  `yaml:"providers" json:"providers,omitempty"`
	Proxy                   *proxy.Config             `yaml:"proxy" json:"proxy,omitempty"`
	ProxyConfig             string                    `yaml:"proxy_config" json:"proxy_config,omitempty"`
	ConfigPath              string                    `yaml:"-" json:"-"`
}

var SupportedUILangs = []string{"en", "zh-CN"}

var SupportedProviders = []string{"agy", "opencode", "simplest"}

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

func (c *Config) LanguageRules() string {
	return fmt.Sprintf(`## Language Preferences

- Responses/Conversations: %s
- Documents and Artifacts: %s
- Code Comments and Docstrings: %s`, c.GetChatLang(), c.GetDocLang(), c.GetCommentLang())
}

func (c *Config) APIHost() string {
	if c == nil || c.Port <= 0 {
		return "http://127.0.0.1:8080"
	}
	return fmt.Sprintf("http://127.0.0.1:%d", c.Port)
}

func (c *Config) InternalAPIHost() string {
	if c == nil || c.InternalPort <= 0 {
		return "http://127.0.0.1:8081"
	}
	return fmt.Sprintf("http://127.0.0.1:%d", c.InternalPort)
}

func (c *Config) StatusURL() string {
	return c.InternalAPIHost() + "/agent-status"
}

func (c *Config) GetProxy() *proxy.Config {
	if c == nil {
		return nil
	}
	return c.Proxy
}

func (c *Config) IsProxyEnabled() bool {
	if c == nil || c.Proxy == nil {
		return false
	}
	return c.Proxy.Enable
}

func (c *Config) ProxyHost() string {
	if c == nil || c.Proxy == nil {
		return "http://127.0.0.1:8082"
	}
	return c.Proxy.ProxyHost()
}

func (c *Config) ProxyAddr() string {
	if c == nil || c.Proxy == nil || c.Proxy.Server.Addr == "" {
		return "127.0.0.1:8082"
	}
	return c.Proxy.Server.Addr
}

func (c *Config) ProxyCACertPath() string {
	if c == nil || c.Proxy == nil {
		return ""
	}
	return c.Proxy.ResolvedCACertPath()
}

func (c *Config) ProxyCAKeyPath() string {
	if c == nil || c.Proxy == nil {
		return ""
	}
	return c.Proxy.ResolvedCAKeyPath()
}

func (c *Config) ResolvedProxyConfigPath() string {
	if c == nil || c.ProxyConfig == "" {
		return ""
	}
	p := c.ProxyConfig
	if strings.HasPrefix(p, "~/") || p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	if c.ConfigPath != "" {
		return filepath.Clean(filepath.Join(filepath.Dir(c.ConfigPath), p))
	}
	abs, err := filepath.Abs(p)
	if err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
}

func (c *Config) validate() error {
	if c.Host == "" {
		return fmt.Errorf("missing host")
	}
	if c.DB != "pg" && c.DB != "sqlite" {
		return fmt.Errorf("invalid db: %s, must be 'pg' or 'sqlite'", c.DB)
	}
	if c.DSN == "" {
		return fmt.Errorf("missing dsn")
	}
	if c.AgentDir == "" {
		return fmt.Errorf("missing agent_dir")
	}

	absDir, err := filepath.Abs(c.AgentDir)
	if err != nil {
		return fmt.Errorf("failed to make agent_dir absolute: %w", err)
	}
	c.AgentDir = absDir

	if c.GeminiAPIKey == "" {
		return fmt.Errorf("missing gemini_api_key")
	}
	if c.GeminiModelForChatTitle == "" {
		return fmt.Errorf("missing gemini_model_for_chat_title")
	}

	if c.UILang != "" && c.UILang != "en" && c.UILang != "zh-CN" {
		return fmt.Errorf("invalid ui_lang %q, must be 'en' or 'zh-CN'", c.UILang)
	}

	for _, p := range c.Providers {
		if !isSupportedProvider(p) {
			return fmt.Errorf("unsupported provider %q, must be one of %v", p, SupportedProviders)
		}
	}

	return nil
}

func (c Config) verifyDirs() error {
	dirs := []string{
		c.AgentDir,
		fmt.Sprintf("%s/agents", c.AgentDir),
	}

	for _, d := range dirs {
		info, err := os.Stat(d)
		if err != nil {
			return fmt.Errorf("directory verification failed: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", d)
		}
	}

	return nil
}

// ParseAndValidate unmarshals configuration YAML data, applies defaults, and validates contents and directories.
func ParseAndValidate(data []byte) (*Config, error) {
	cfg := &Config{}
	err := yaml.Unmarshal(data, cfg)
	if err != nil {
		return nil, err
	}

	if cfg.Port <= 0 {
		cfg.Port = 8080
	}

	if cfg.InternalPort <= 0 {
		cfg.InternalPort = 8081
	}

	if cfg.ChatLang == "" {
		cfg.ChatLang = DefaultLanguage
	}

	if cfg.DocLang == "" {
		cfg.DocLang = DefaultLanguage
	}

	if cfg.CommentLang == "" {
		cfg.CommentLang = DefaultLanguage
	}

	if cfg.UILang == "" {
		cfg.UILang = "en"
	}

	if len(cfg.Providers) == 0 {
		cfg.Providers = append([]string(nil), SupportedProviders...)
	} else {
		seen := make(map[string]bool, len(cfg.Providers))
		deduped := make([]string, 0, len(cfg.Providers))
		for _, p := range cfg.Providers {
			if !seen[p] {
				seen[p] = true
				deduped = append(deduped, p)
			}
		}
		cfg.Providers = deduped
	}

	if cfg.Proxy != nil {
		cfg.Proxy.ApplyDefaults()
		if err := cfg.Proxy.Validate(); err != nil {
			return nil, fmt.Errorf("proxy config error: %w", err)
		}
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	if err := cfg.verifyDirs(); err != nil {
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
	return cfg, nil
}
