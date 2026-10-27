package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/elmhuangyu/configs/pkg/configlib"

	"github.com/AgentDrasil/asgard/pkg/paths"
)

const (
	// EnvEtcdEndpoints is the environment variable specifying etcd cluster endpoints.
	EnvEtcdEndpoints = "ETCD_ENDPOINTS"
	// EnvEtcdPrefix is the environment variable specifying etcd prefix for configuration.
	EnvEtcdPrefix = "ETCD_PREFIX"
	// DefaultEtcdPrefix is the default prefix if not specified in environment.
	DefaultEtcdPrefix = "/asgard/configs"
)

// ErrMissingEtcdEndpoints is returned when ETCD_ENDPOINTS environment variable is missing or empty.
var ErrMissingEtcdEndpoints = errors.New("ETCD_ENDPOINTS environment variable is required; Asgard runs exclusively with etcd")

// NewEtcdConfigManager connects to etcd and starts a ConfigManager for AsgardTreeConfig.
// It fails fast if ETCD_ENDPOINTS is not configured or if connection/initial fetch fails.
func NewEtcdConfigManager(ctx context.Context) (*configlib.ConfigManager[AsgardTreeConfig], *clientv3.Client, error) {
	endpointsStr := os.Getenv(EnvEtcdEndpoints)
	if strings.TrimSpace(endpointsStr) == "" {
		return nil, nil, ErrMissingEtcdEndpoints
	}

	rawEndpoints := strings.Split(endpointsStr, ",")
	endpoints := make([]string, 0, len(rawEndpoints))
	for _, ep := range rawEndpoints {
		ep = strings.TrimSpace(ep)
		if ep != "" {
			endpoints = append(endpoints, ep)
		}
	}
	if len(endpoints) == 0 {
		return nil, nil, ErrMissingEtcdEndpoints
	}

	prefix := os.Getenv(EnvEtcdPrefix)
	if strings.TrimSpace(prefix) == "" {
		prefix = DefaultEtcdPrefix
	}

	client, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create etcd client: %w", err)
	}

	source := configlib.NewEtcdSource(client, prefix)
	mgr := configlib.NewManager[AsgardTreeConfig](source)

	return mgr, client, nil
}

// ApplyDefaultsAndValidate validates and applies default values to the Config struct.
func (c *Config) ApplyDefaultsAndValidate() error {
	if c.Port <= 0 {
		c.Port = 8080
	}
	if c.InternalPort <= 0 {
		c.InternalPort = 8081
	}
	if c.ChatLang == "" {
		c.ChatLang = DefaultLanguage
	}
	if c.DocLang == "" {
		c.DocLang = DefaultLanguage
	}
	if c.CommentLang == "" {
		c.CommentLang = DefaultLanguage
	}
	if c.UILang == "" {
		c.UILang = "en"
	}
	if c.AgentDir == "" {
		c.AgentDir = paths.AgentsRoot()
	}

	if len(c.Providers) == 0 {
		c.Providers = append([]string(nil), SupportedProviders...)
	} else {
		seen := make(map[string]bool, len(c.Providers))
		deduped := make([]string, 0, len(c.Providers))
		for _, p := range c.Providers {
			if !seen[p] {
				seen[p] = true
				deduped = append(deduped, p)
			}
		}
		c.Providers = deduped
	}

	if c.Proxy != nil {
		c.Proxy.ApplyDefaults()
		if err := c.Proxy.Validate(); err != nil {
			return fmt.Errorf("proxy config error: %w", err)
		}
	}

	return c.validate()
}

// Clone returns a shallow copy of the Config with an independent mutex.
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}
	c.proxyMu.RLock()
	defer c.proxyMu.RUnlock()

	cp := &Config{
		Debug:                              c.Debug,
		DB:                                 c.DB,
		DSN:                                c.DSN,
		AgentDir:                           c.AgentDir,
		Port:                               c.Port,
		InternalPort:                       c.InternalPort,
		Host:                               c.Host,
		WebUIPath:                          c.WebUIPath,
		GeminiAPIKey:                       c.GeminiAPIKey,
		GeminiModelForChatTitle:            c.GeminiModelForChatTitle,
		GeminiModelForCommandResultCompass: c.GeminiModelForCommandResultCompass,
		TypesafeAPIKey:                     c.TypesafeAPIKey,
		FirebaseWebpushWeb:                 c.FirebaseWebpushWeb,
		ChatLang:                           c.ChatLang,
		DocLang:                            c.DocLang,
		CommentLang:                        c.CommentLang,
		UILang:                             c.UILang,
		Providers:                          append([]string(nil), c.Providers...),
		Proxy:                              c.Proxy,
		Auth:                               c.Auth,
		ConfigPath:                         c.ConfigPath,
	}
	return cp
}
