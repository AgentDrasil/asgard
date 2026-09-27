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

// FullModelName returns the model identifier prefixed with provider if not already present.
func FullModelName(provider, id string) string {
	if provider == "" {
		return id
	}
	if strings.HasPrefix(strings.ToLower(id), strings.ToLower(provider)+"/") {
		return id
	}
	return provider + "/" + id
}

// ProviderConfig defines configuration for an LLM provider.
type ProviderConfig struct {
	API     string            `yaml:"api" json:"api"`
	APIKey  string            `yaml:"apiKey,omitempty" json:"apiKey,omitempty"`
	BaseURL string            `yaml:"baseUrl,omitempty" json:"baseUrl,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
}

// ModelConfig defines configuration for a specific model endpoint.
// The API wire protocol is defined once on the referenced ProviderConfig.
type ModelConfig struct {
	ID               string                  `yaml:"id" json:"id"`
	Model            string                  `yaml:"model,omitempty" json:"model,omitempty"`
	Name             string                  `yaml:"name,omitempty" json:"name,omitempty"`
	Type             string                  `yaml:"type,omitempty" json:"type,omitempty"`
	Provider         string                  `yaml:"provider" json:"provider"`
	BaseURL          string                  `yaml:"baseUrl,omitempty" json:"baseUrl,omitempty"`
	ContextWindow    int64                   `yaml:"contextWindow,omitempty" json:"contextWindow,omitempty"`
	MaxTokens        int64                   `yaml:"maxTokens,omitempty" json:"maxTokens,omitempty"`
	Reasoning        bool                    `yaml:"reasoning,omitempty" json:"reasoning,omitempty"`
	ReasoningEffort  []string                `yaml:"reasoningEffort,omitempty" json:"reasoningEffort,omitempty"`
	ThinkingLevelMap map[string]*string      `yaml:"thinkingLevelMap,omitempty" json:"thinkingLevelMap,omitempty"`
	Cost             types.ModelCost         `yaml:"cost,omitempty" json:"cost,omitempty"`
	PromptCache      *types.ModelPromptCache `yaml:"promptCache,omitempty" json:"promptCache,omitempty"`
	SamplingParams   map[string]any          `yaml:"samplingParams,omitempty" json:"samplingParams,omitempty"`
	InputLimits      *types.ModelInputLimits `yaml:"inputLimits,omitempty" json:"inputLimits,omitempty"`
	Input            []string                `yaml:"input,omitempty" json:"input,omitempty"`
	Headers          map[string]string       `yaml:"headers,omitempty" json:"headers,omitempty"`
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

	// DocToolAllowedDirs restricts the doc tools (write_doc, edit_doc) to
	// markdown files under these absolute directories. Empty means any .md
	// path is allowed.
	DocToolAllowedDirs []string `yaml:"docToolAllowedDirs,omitempty" json:"docToolAllowedDirs,omitempty"`
}

// DefaultConfigPath resolves the configuration (keys/providers) file path by precedence:
// 1. $SIMPLEST_KEY_PATH / $SIMPLEST_CONFIG_PATH
// 2. $XDG_CONFIG_HOME/simplest/key.yaml (or ~/.config/simplest/key.yaml)
// 3. $XDG_CONFIG_HOME/simplest/key.yml (or ~/.config/simplest/key.yml)
// 4. ~/.simplest/key.yaml
// 5. ~/.simplest/key.yml
// 6. $XDG_CONFIG_HOME/simplest/config.yaml (or ~/.config/simplest/config.yaml)
// 7. $XDG_CONFIG_HOME/simplest/config.yml (or ~/.config/simplest/config.yml)
// 8. ~/.simplest/config.yaml
// 9. ~/.simplest/config.yml
func DefaultConfigPath() string {
	if envPath := os.Getenv("SIMPLEST_KEY_PATH"); envPath != "" {
		return envPath
	}
	if envPath := os.Getenv("SIMPLEST_CONFIG_PATH"); envPath != "" {
		return envPath
	}

	homeDir, _ := os.UserHomeDir()
	xdgConfigHome := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfigHome == "" && homeDir != "" {
		xdgConfigHome = filepath.Join(homeDir, ".config")
	}

	candidates := make([]string, 0, 8)
	if xdgConfigHome != "" {
		candidates = append(candidates,
			filepath.Join(xdgConfigHome, "simplest", "key.yaml"),
			filepath.Join(xdgConfigHome, "simplest", "key.yml"),
		)
	}
	if homeDir != "" {
		candidates = append(candidates,
			filepath.Join(homeDir, ".simplest", "key.yaml"),
			filepath.Join(homeDir, ".simplest", "key.yml"),
		)
	}
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

	// Default fallback path if none exist on filesystem: ~/.config/simplest/key.yaml
	if len(candidates) > 0 {
		return candidates[0]
	}
	return ""
}

// DefaultProvidersPath resolves the providers configuration file path (defining api, baseUrl, etc.)
// corresponding to a config path, or discovers it from standard candidate directories if configDir is empty.
func DefaultProvidersPath(configDir string) string {
	if configDir != "" {
		for _, name := range []string{"providers.yaml", "providers.yml"} {
			cand := filepath.Join(configDir, name)
			if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
				return cand
			}
		}
		return filepath.Join(configDir, "providers.yaml")
	}

	homeDir, _ := os.UserHomeDir()
	xdgConfigHome := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfigHome == "" && homeDir != "" {
		xdgConfigHome = filepath.Join(homeDir, ".config")
	}

	candidates := make([]string, 0, 4)
	if xdgConfigHome != "" {
		candidates = append(candidates,
			filepath.Join(xdgConfigHome, "simplest", "providers.yaml"),
			filepath.Join(xdgConfigHome, "simplest", "providers.yml"),
		)
	}
	if homeDir != "" {
		candidates = append(candidates,
			filepath.Join(homeDir, ".simplest", "providers.yaml"),
			filepath.Join(homeDir, ".simplest", "providers.yml"),
		)
	}

	for _, cand := range candidates {
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}

	if len(candidates) > 0 {
		return candidates[0]
	}
	return ""
}

// DefaultModelsPath resolves the models configuration file path corresponding to a config path,
// or discovers it from standard candidate directories if configDir is empty.
func DefaultModelsPath(configDir string) string {
	if envPath := os.Getenv("SIMPLEST_MODELS_PATH"); envPath != "" {
		return envPath
	}

	if configDir != "" {
		for _, name := range []string{"models.yaml", "models.yml"} {
			cand := filepath.Join(configDir, name)
			if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
				return cand
			}
		}
		return filepath.Join(configDir, "models.yaml")
	}

	homeDir, _ := os.UserHomeDir()
	xdgConfigHome := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfigHome == "" && homeDir != "" {
		xdgConfigHome = filepath.Join(homeDir, ".config")
	}

	candidates := make([]string, 0, 4)
	if xdgConfigHome != "" {
		candidates = append(candidates,
			filepath.Join(xdgConfigHome, "simplest", "models.yaml"),
			filepath.Join(xdgConfigHome, "simplest", "models.yml"),
		)
	}
	if homeDir != "" {
		candidates = append(candidates,
			filepath.Join(homeDir, ".simplest", "models.yaml"),
			filepath.Join(homeDir, ".simplest", "models.yml"),
		)
	}

	for _, cand := range candidates {
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}

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

// LoadFrom loads and parses configuration files.
// Under the split configuration policy:
//  1. key.yaml only defines provider secrets (apiKey / key) and security settings (docToolAllowedDirs).
//     Defining api, baseUrl, or models in key.yaml is rejected with an error instructing migration.
//  2. providers.yaml defines provider metadata (api, baseUrl, headers).
//  3. models.yaml defines model catalog.
func LoadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	// Expand environment variables across the YAML content.
	expanded := os.ExpandEnv(string(data))

	var rawKeyDoc struct {
		Providers          map[string]ProviderConfig `yaml:"providers"`
		Models             []ModelConfig             `yaml:"models"`
		DocToolAllowedDirs []string                  `yaml:"docToolAllowedDirs"`
	}
	if err := yaml.Unmarshal([]byte(expanded), &rawKeyDoc); err != nil {
		return nil, fmt.Errorf("parse yaml config %s: %w", path, err)
	}

	if len(rawKeyDoc.Models) > 0 {
		return nil, fmt.Errorf("config %s defines models inline; inline models are deprecated, please run migrate-config to split into key.yaml, providers.yaml, and models.yaml", path)
	}

	// Check if key.yaml defines api or baseUrl
	for provName, prov := range rawKeyDoc.Providers {
		if prov.API != "" || prov.BaseURL != "" {
			return nil, fmt.Errorf("provider %q in %s defines api or baseUrl; provider endpoints and api types must be defined in providers.yaml, please run migrate-config", provName, path)
		}
	}

	cfg := Config{
		Providers:          make(map[string]ProviderConfig),
		Models:             make([]ModelConfig, 0),
		DocToolAllowedDirs: rawKeyDoc.DocToolAllowedDirs,
	}

	// Load providers from standalone providers.yaml/providers.yml
	configDir := filepath.Dir(path)
	providersPath := DefaultProvidersPath(configDir)
	if providersPath != "" {
		if provData, readErr := os.ReadFile(providersPath); readErr == nil {
			expandedProv := os.ExpandEnv(string(provData))
			var provDoc struct {
				Providers map[string]ProviderConfig `yaml:"providers"`
			}
			if err := yaml.Unmarshal([]byte(expandedProv), &provDoc); err != nil {
				return nil, fmt.Errorf("parse providers file %s: %w", providersPath, err)
			}
			for k, v := range provDoc.Providers {
				cfg.Providers[k] = v
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return nil, fmt.Errorf("read providers file %s: %w", providersPath, readErr)
		}
	}

	// Merge keys from rawKeyDoc into cfg.Providers
	for provName, keyProv := range rawKeyDoc.Providers {
		baseProv, exists := cfg.Providers[provName]
		if !exists {
			baseProv = ProviderConfig{}
		}
		if keyProv.APIKey != "" {
			baseProv.APIKey = keyProv.APIKey
		}
		if len(keyProv.Headers) > 0 {
			if baseProv.Headers == nil {
				baseProv.Headers = make(map[string]string, len(keyProv.Headers))
			}
			for hk, hv := range keyProv.Headers {
				baseProv.Headers[hk] = hv
			}
		}
		cfg.Providers[provName] = baseProv
	}

	// Load models from standalone models.yaml/models.yml
	modelsPath := DefaultModelsPath(configDir)
	if modelsPath != "" {
		if modelsData, readErr := os.ReadFile(modelsPath); readErr == nil {
			expandedModels := os.ExpandEnv(string(modelsData))
			var modelsDoc struct {
				Models []ModelConfig `yaml:"models"`
			}
			// Support both `models: [...]` wrapper or top-level `[...]` list
			if err := yaml.Unmarshal([]byte(expandedModels), &modelsDoc); err == nil && len(modelsDoc.Models) > 0 {
				cfg.Models = modelsDoc.Models
			} else {
				var rawList []ModelConfig
				if errList := yaml.Unmarshal([]byte(expandedModels), &rawList); errList == nil && len(rawList) > 0 {
					cfg.Models = rawList
				} else if err != nil {
					return nil, fmt.Errorf("parse models file %s: %w", modelsPath, err)
				}
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return nil, fmt.Errorf("read models file %s: %w", modelsPath, readErr)
		}
	}

	return &cfg, nil
}

func defaultFallbackConfig() *Config {
	cfg := &Config{
		Providers: make(map[string]ProviderConfig),
		Models:    make([]ModelConfig, 0),
	}

	if geminiKey := os.Getenv("GEMINI_API_KEY"); geminiKey != "" {
		cfg.Providers["gemini"] = ProviderConfig{
			API:    types.APIGemini,
			APIKey: geminiKey,
		}
		cfg.Models = append(cfg.Models, ModelConfig{
			ID:            "gemini-3.7-flash",
			Name:          "Gemini 3.7 Flash",
			Type:          "chat",
			Provider:      "gemini",
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
			Type:          "chat",
			Provider:      "openai",
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
		api := ""
		if hasProv {
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

		// Default value normalization: when mc.Type is empty, normalize to types.ModelTypeChat.
		modelType := types.ModelType(mc.Type)
		if modelType == "" {
			modelType = types.ModelTypeChat
		}

		var thinkingLevelMap types.ThinkingLevelMap
		if mc.ThinkingLevelMap != nil {
			thinkingLevelMap = make(types.ThinkingLevelMap, len(mc.ThinkingLevelMap))
			for k, v := range mc.ThinkingLevelMap {
				normK := strings.ToLower(k)
				isStandard := false
				for _, lvl := range types.OrderedThinkingLevels {
					if string(lvl) == normK {
						isStandard = true
						break
					}
				}
				if !isStandard {
					fmt.Fprintf(os.Stderr, "Warning: model %q specifies unknown thinkingLevelMap key %q; canonical levels are: %v\n", mc.ID, k, types.OrderedThinkingLevels)
				}
				thinkingLevelMap[types.ThinkingLevel(normK)] = v
			}
		}

		var samplingParams types.ModelSamplingParams
		if mc.SamplingParams != nil {
			samplingParams = types.ModelSamplingParams(mc.SamplingParams)
		}

		m := &types.Model{
			ID:               mc.ID,
			Model:            mc.Model,
			Name:             mc.Name,
			Type:             modelType,
			API:              api,
			Provider:         mc.Provider,
			BaseURL:          baseURL,
			Reasoning:        mc.Reasoning,
			ReasoningEffort:  mc.ReasoningEffort,
			ThinkingLevelMap: thinkingLevelMap,
			Input:            mc.Input,
			Cost:             mc.Cost,
			PromptCache:      mc.PromptCache,
			SamplingParams:   samplingParams,
			InputLimits:      mc.InputLimits,
			ContextWindow:    mc.ContextWindow,
			MaxTokens:        mc.MaxTokens,
			Headers:          mergedHeaders,
		}
		res = append(res, m)
	}

	return res
}

// ResolveModelAndProvider resolves a model and constructs its corresponding Provider instance.
// If modelID is empty, the first allowed model in the configuration is selected.
// If modelID is specified, it matches against allowed models by:
// 1. Exact or case-insensitive match on FullModelName (e.g. "gemini/gemini-3.5-flash-lite", "zai-coding-plan/glm-5.3")
// 2. Exact or case-insensitive match on "Provider/ModelID"
// 3. Match by splitting modelID at first '/' into provider and model ID
// 4. Exact or case-insensitive match on Model ID (e.g. "gemini-3.5-flash-lite", "glm-5.3")
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
		// 1. Try full model name or provider/id match
		for _, m := range available {
			fullName := FullModelName(m.Provider, m.ID)
			if strings.EqualFold(fullName, modelID) {
				matched = m
				break
			}
			if m.Provider != "" && strings.EqualFold(m.Provider+"/"+m.ID, modelID) {
				matched = m
				break
			}
		}
		// 2. Try matching provider and ID parts if modelID contains '/'
		if matched == nil && strings.Contains(modelID, "/") {
			parts := strings.SplitN(modelID, "/", 2)
			for _, m := range available {
				if strings.EqualFold(m.Provider, parts[0]) && strings.EqualFold(m.ID, parts[1]) {
					matched = m
					break
				}
			}
		}
		// 3. Fallback: match by bare model ID
		if matched == nil {
			for _, m := range available {
				if strings.EqualFold(m.ID, modelID) {
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
	if matched.API == "" {
		return nil, nil, fmt.Errorf("provider %q for model %q has no api configured", matched.Provider, matched.ID)
	}

	var p types.Provider
	switch strings.ToLower(matched.API) {
	case types.APIGemini, "google-gemini":
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

// GetGlobalConfig returns the cached or loaded global configuration.
func GetGlobalConfig() (*Config, error) {
	return getOrLoadGlobalConfig()
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
