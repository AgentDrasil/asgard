// Package config provides configuration loading, Fail-Closed validation,
// environment variable expansion, and model cataloging for the simplest module.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/goccy/go-yaml"

	"github.com/AgentDrasil/asgard/simplest/internal/provider"
	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// ProviderConfig defines configuration for an LLM provider.
type ProviderConfig struct {
	API     string            `yaml:"api" json:"api"`
	APIKey  string            `yaml:"apiKey" json:"apiKey"`
	BaseURL string            `yaml:"baseUrl" json:"baseUrl"`
	Headers map[string]string `yaml:"headers" json:"headers"`
}

// ModelConfig defines configuration for a specific model endpoint.
type ModelConfig struct {
	ID              string               `yaml:"id" json:"id"`
	Name            string               `yaml:"name" json:"name"`
	Provider        string               `yaml:"provider" json:"provider"`
	API             string               `yaml:"api" json:"api"`
	BaseURL         string               `yaml:"baseUrl" json:"baseUrl"`
	ContextWindow   int64                `yaml:"contextWindow" json:"contextWindow"`
	MaxTokens       int64                `yaml:"maxTokens" json:"maxTokens"`
	Reasoning       bool                 `yaml:"reasoning" json:"reasoning"`
	ReasoningEffort []string             `yaml:"reasoningEffort,omitempty" json:"reasoningEffort,omitempty"`
	Cost            types.ModelCostRates `yaml:"cost" json:"cost"`
	Input           []string             `yaml:"input" json:"input"`
	Headers         map[string]string    `yaml:"headers" json:"headers"`
}

// RawModelConfig is a type alias to ModelConfig used to prevent infinite recursion during UnmarshalYAML.
type RawModelConfig ModelConfig

// UnmarshalYAML implements custom unmarshaling to support both reasoningEffort and reasoning_effort keys.
func (m *ModelConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var raw struct {
		RawModelConfig       `yaml:",inline"`
		ReasoningEffortSnake []string `yaml:"reasoning_effort,omitempty"`
	}
	if err := unmarshal(&raw); err != nil {
		return err
	}
	*m = ModelConfig(raw.RawModelConfig)
	if len(raw.ReasoningEffortSnake) > 0 && len(m.ReasoningEffort) == 0 {
		m.ReasoningEffort = raw.ReasoningEffortSnake
	}
	return nil
}

// Config represents the top-level configuration structure.
type Config struct {
	Providers map[string]ProviderConfig `yaml:"providers" json:"providers"`
	Models    []ModelConfig             `yaml:"models" json:"models"`
}

// DefaultConfigPath resolves the configuration file path by precedence:
// 1. $SIMPLEST_CONFIG_PATH
// 2. $XDG_CONFIG_HOME/simplest/config.yaml (or ~/.config/simplest/config.yaml)
// 3. $XDG_CONFIG_HOME/simplest/config.yml (or ~/.config/simplest/config.yml)
// 4. ~/.simplest/config.yaml
// 5. ~/.simplest/config.yml
func DefaultConfigPath() string {
	if envPath := os.Getenv("SIMPLEST_CONFIG_PATH"); envPath != "" {
		return envPath
	}

	homeDir, _ := os.UserHomeDir()
	xdgConfigHome := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfigHome == "" && homeDir != "" {
		xdgConfigHome = filepath.Join(homeDir, ".config")
	}

	candidates := make([]string, 0, 4)
	if xdgConfigHome != "" {
		candidates = append(candidates,
			filepath.Join(xdgConfigHome, "simplest", "config.yaml"),
			filepath.Join(xdgConfigHome, "simplest", "config.yml"),
		)
	}
	if homeDir != "" {
		candidates = append(candidates,
			filepath.Join(homeDir, ".simplest", "config.yaml"),
			filepath.Join(homeDir, ".simplest", "config.yml"),
		)
	}

	for _, cand := range candidates {
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}

	// Default fallback path if none exist on filesystem.
	if len(candidates) > 0 {
		return candidates[0]
	}
	return ""
}

// Load loads the configuration from DefaultConfigPath().
// If the file exists, it is loaded with Fail-Closed semantics (errors cause Load to return an error).
// If the file does not exist (os.IsNotExist), it falls back to built-in default models
// derived from GEMINI_API_KEY and OPENAI_API_KEY.
func Load() (*Config, error) {
	path := DefaultConfigPath()
	if path == "" {
		return defaultFallbackConfig(), nil
	}

	cfg, err := LoadFrom(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultFallbackConfig(), nil
		}
		return nil, err
	}
	return cfg, nil
}

// LoadFrom loads and parses a YAML configuration file from the specified path.
// It expands environment variables and validates structure.
func LoadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	// Expand environment variables across the YAML content.
	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse yaml config %s: %w", path, err)
	}

	return &cfg, nil
}

func defaultFallbackConfig() *Config {
	cfg := &Config{
		Providers: make(map[string]ProviderConfig),
		Models:    make([]ModelConfig, 0),
	}

	if geminiKey := os.Getenv("GEMINI_API_KEY"); geminiKey != "" {
		cfg.Providers["google"] = ProviderConfig{
			API:    types.APIGoogleGemini,
			APIKey: geminiKey,
		}
		cfg.Models = append(cfg.Models, ModelConfig{
			ID:            "gemini-3.7-flash",
			Name:          "Gemini 3.7 Flash",
			Provider:      "google",
			API:           types.APIGoogleGemini,
			ContextWindow: 1_048_576,
			MaxTokens:     8192,
			Reasoning:     true,
			Input:         []string{"text", "image"},
		})
	}

	if openaiKey := os.Getenv("OPENAI_API_KEY"); openaiKey != "" {
		baseURL := os.Getenv("OPENAI_BASE_URL")
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		cfg.Providers["openai"] = ProviderConfig{
			API:     types.APIOpenAICompat,
			APIKey:  openaiKey,
			BaseURL: baseURL,
		}
		cfg.Models = append(cfg.Models, ModelConfig{
			ID:            "gpt-4o",
			Name:          "GPT-4o",
			Provider:      "openai",
			API:           types.APIOpenAICompat,
			BaseURL:       baseURL,
			ContextWindow: 128_000,
			MaxTokens:     4096,
			Reasoning:     false,
			Input:         []string{"text", "image"},
		})
	}

	return cfg
}

// GetAvailableModels returns all configured models.
func (c *Config) GetAvailableModels() []*types.Model {
	if c == nil {
		return nil
	}

	var res []*types.Model
	for _, mc := range c.Models {

		provCfg, hasProv := c.Providers[mc.Provider]
		api := mc.API
		if api == "" && hasProv {
			api = provCfg.API
		}
		baseURL := mc.BaseURL
		if baseURL == "" && hasProv {
			baseURL = provCfg.BaseURL
		}

		// Merge headers: provider headers then model headers override.
		var mergedHeaders map[string]string
		if hasProv && len(provCfg.Headers) > 0 {
			mergedHeaders = make(map[string]string, len(provCfg.Headers)+len(mc.Headers))
			for k, v := range provCfg.Headers {
				mergedHeaders[k] = v
			}
		}
		if len(mc.Headers) > 0 {
			if mergedHeaders == nil {
				mergedHeaders = make(map[string]string, len(mc.Headers))
			}
			for k, v := range mc.Headers {
				mergedHeaders[k] = v
			}
		}

		m := &types.Model{
			ID:              mc.ID,
			Name:            mc.Name,
			API:             api,
			Provider:        mc.Provider,
			BaseURL:         baseURL,
			Reasoning:       mc.Reasoning,
			ReasoningEffort: mc.ReasoningEffort,
			Input:           mc.Input,
			Cost:            mc.Cost,
			ContextWindow:   mc.ContextWindow,
			MaxTokens:       mc.MaxTokens,
			Headers:         mergedHeaders,
		}
		res = append(res, m)
	}

	return res
}

// ResolveModelAndProvider resolves a model and constructs its corresponding Provider instance.
// If modelID is empty, the first allowed model in the configuration is selected.
// If modelID is specified, it matches against allowed models by:
// 1. Exact or case-insensitive match on Model ID (e.g. "glm-5.3")
// 2. Exact or case-insensitive match on "Provider/ModelID" (e.g. "zai-coding-plan/glm-5.3")
// 3. If modelID contains a provider prefix (e.g. "zai-coding-plan/glm-5.3"), match provider and ID parts
func (c *Config) ResolveModelAndProvider(modelID string) (*types.Model, types.Provider, error) {
	if c == nil {
		return nil, nil, errors.New("nil configuration")
	}

	available := c.GetAvailableModels()
	if len(available) == 0 {
		return nil, nil, fmt.Errorf("no models configured")
	}

	var matched *types.Model
	if modelID == "" {
		matched = available[0]
	} else {
		for _, m := range available {
			if strings.EqualFold(m.ID, modelID) {
				matched = m
				break
			}
			if m.Provider != "" && strings.EqualFold(m.Provider+"/"+m.ID, modelID) {
				matched = m
				break
			}
		}
		if matched == nil && strings.Contains(modelID, "/") {
			parts := strings.SplitN(modelID, "/", 2)
			for _, m := range available {
				if strings.EqualFold(m.Provider, parts[0]) && strings.EqualFold(m.ID, parts[1]) {
					matched = m
					break
				}
			}
		}
	}

	if matched == nil {
		return nil, nil, fmt.Errorf("model %q not found in configuration", modelID)
	}

	provCfg, hasProv := c.Providers[matched.Provider]
	apiKey := ""
	if hasProv {
		apiKey = provCfg.APIKey
	}

	var p types.Provider
	switch matched.API {
	case types.APIGoogleGemini:
		p = provider.NewGemini(apiKey)
	case types.APIOpenAICompat:
		oa := provider.NewOpenAICompat(apiKey)
		if matched.BaseURL != "" {
			oa.BaseURL = matched.BaseURL
		} else if hasProv && provCfg.BaseURL != "" {
			oa.BaseURL = provCfg.BaseURL
		}
		p = oa
	default:
		return nil, nil, fmt.Errorf("unsupported API wire protocol %q for model %q", matched.API, matched.ID)
	}

	return matched, p, nil
}

// Package-level global configuration singleton and mutex.
var (
	globalMu  sync.RWMutex
	globalCfg *Config
)

// GetAvailableModels returns the list of available models from the global configuration.
func GetAvailableModels() ([]*types.Model, error) {
	cfg, err := getOrLoadGlobalConfig()
	if err != nil {
		return nil, err
	}
	return cfg.GetAvailableModels(), nil
}

// ResolveModelAndProvider resolves a model and provider from the global configuration.
func ResolveModelAndProvider(modelID string) (*types.Model, types.Provider, error) {
	cfg, err := getOrLoadGlobalConfig()
	if err != nil {
		return nil, nil, err
	}
	return cfg.ResolveModelAndProvider(modelID)
}

// SetGlobalConfig overrides the global configuration (useful for testing or programmatic init).
func SetGlobalConfig(c *Config) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalCfg = c
}

// ResetGlobalConfig clears the cached global configuration.
func ResetGlobalConfig() {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalCfg = nil
}

func getOrLoadGlobalConfig() (*Config, error) {
	globalMu.RLock()
	if globalCfg != nil {
		defer globalMu.RUnlock()
		return globalCfg, nil
	}
	globalMu.RUnlock()

	globalMu.Lock()
	defer globalMu.Unlock()
	if globalCfg != nil {
		return globalCfg, nil
	}

	loaded, err := Load()
	if err != nil {
		return nil, err
	}
	globalCfg = loaded
	return globalCfg, nil
}
