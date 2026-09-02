package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: Config{
				Debug:                   true,
				DB:                      "sqlite",
				DSN:                     "test.db",
				AgentDir:                "./agents",
				Host:                    "127.0.0.1",
				GeminiAPIKey:            "test-key",
				GeminiModelForChatTitle: "gemini-3.1-flash-lite",
			},
			wantErr: false,
		},
		{
			name: "invalid db",
			config: Config{
				DB:                      "mysql",
				DSN:                     "test.db",
				AgentDir:                "./agents",
				Host:                    "127.0.0.1",
				GeminiAPIKey:            "test-key",
				GeminiModelForChatTitle: "gemini-3.1-flash-lite",
			},
			wantErr: true,
			errMsg:  "invalid db: mysql",
		},
		{
			name: "missing db",
			config: Config{
				DSN:                     "test.db",
				AgentDir:                "./agents",
				Host:                    "127.0.0.1",
				GeminiAPIKey:            "test-key",
				GeminiModelForChatTitle: "gemini-3.1-flash-lite",
			},
			wantErr: true,
			errMsg:  "invalid db: , must be 'pg' or 'sqlite'",
		},
		{
			name: "missing dsn",
			config: Config{
				DB:                      "pg",
				AgentDir:                "./agents",
				Host:                    "127.0.0.1",
				GeminiAPIKey:            "test-key",
				GeminiModelForChatTitle: "gemini-3.1-flash-lite",
			},
			wantErr: true,
			errMsg:  "missing dsn",
		},
		{
			name: "missing agent_dir",
			config: Config{
				DB:                      "sqlite",
				DSN:                     "test.db",
				Host:                    "127.0.0.1",
				GeminiAPIKey:            "test-key",
				GeminiModelForChatTitle: "gemini-3.1-flash-lite",
			},
			wantErr: true,
			errMsg:  "missing agent_dir",
		},
		{
			name: "missing host",
			config: Config{
				DB:                      "sqlite",
				DSN:                     "test.db",
				AgentDir:                "./agents",
				GeminiAPIKey:            "test-key",
				GeminiModelForChatTitle: "gemini-3.1-flash-lite",
			},
			wantErr: true,
			errMsg:  "missing host",
		},
		{
			name: "missing gemini_api_key",
			config: Config{
				DB:                      "sqlite",
				DSN:                     "test.db",
				AgentDir:                "./agents",
				Host:                    "127.0.0.1",
				GeminiModelForChatTitle: "gemini-3.1-flash-lite",
			},
			wantErr: true,
			errMsg:  "missing gemini_api_key",
		},
		{
			name: "missing gemini_model_for_chat_title",
			config: Config{
				DB:           "sqlite",
				DSN:          "test.db",
				AgentDir:     "./agents",
				Host:         "127.0.0.1",
				GeminiAPIKey: "test-key",
			},
			wantErr: true,
			errMsg:  "missing gemini_model_for_chat_title",
		},
		{
			name: "invalid ui_lang",
			config: Config{
				DB:                      "sqlite",
				DSN:                     "test.db",
				AgentDir:                "./agents",
				Host:                    "127.0.0.1",
				GeminiAPIKey:            "test-key",
				GeminiModelForChatTitle: "gemini-3.1-flash-lite",
				UILang:                  "fr",
			},
			wantErr: true,
			errMsg:  `invalid ui_lang "fr", must be 'en' or 'zh-CN'`,
		},
		{
			name: "valid ui_lang zh-CN",
			config: Config{
				DB:                      "sqlite",
				DSN:                     "test.db",
				AgentDir:                "./agents",
				Host:                    "127.0.0.1",
				GeminiAPIKey:            "test-key",
				GeminiModelForChatTitle: "gemini-3.1-flash-lite",
				UILang:                  "zh-CN",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.errMsg)
				return
			}
			require.NoError(t, err)
			assert.True(t, filepath.IsAbs(tt.config.AgentDir))
		})
	}
}

func TestConfig_VerifyDirs(t *testing.T) {
	t.Parallel()

	t.Run("root dir missing", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		agentDir := filepath.Join(tempDir, "non_existent")
		cfg := Config{DB: "sqlite", AgentDir: agentDir}
		err := cfg.verifyDirs()
		require.Error(t, err)
		assert.ErrorContains(t, err, "directory verification failed")
	})

	t.Run("subdirs missing", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		agentDir := filepath.Join(tempDir, "agent_root")
		require.NoError(t, os.MkdirAll(agentDir, 0755))

		cfg := Config{DB: "sqlite", AgentDir: agentDir}
		err := cfg.verifyDirs()
		require.Error(t, err)
		assert.ErrorContains(t, err, "directory verification failed")
	})

	t.Run("required dirs exist", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		agentDir := filepath.Join(tempDir, "agent_root")
		require.NoError(t, os.MkdirAll(filepath.Join(agentDir, "agents"), 0755))

		cfg := Config{DB: "sqlite", AgentDir: agentDir}
		require.NoError(t, cfg.verifyDirs())
	})

	t.Run("path is a file not a directory", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		filePath := filepath.Join(tempDir, "not_a_dir")
		require.NoError(t, os.WriteFile(filePath, []byte("test"), 0644))

		cfg := Config{DB: "sqlite", AgentDir: filePath}
		err := cfg.verifyDirs()
		require.Error(t, err)
		assert.ErrorContains(t, err, "not a directory")
	})
}

func TestLoadConfig_Languages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		yamlContent     string
		wantChatLang    string
		wantDocLang     string
		wantCommentLang string
	}{
		{
			name: "default when language fields omitted",
			yamlContent: `
debug: true
db: sqlite
dsn: test.db
agent_dir: %s
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-3.1-flash-lite
`,
			wantChatLang:    DefaultLanguage,
			wantDocLang:     DefaultLanguage,
			wantCommentLang: DefaultLanguage,
		},
		{
			name: "default when language fields empty string",
			yamlContent: `
debug: true
db: sqlite
dsn: test.db
agent_dir: %s
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-3.1-flash-lite
chat_lang: ""
doc_lang: ""
comment_lang: ""
`,
			wantChatLang:    DefaultLanguage,
			wantDocLang:     DefaultLanguage,
			wantCommentLang: DefaultLanguage,
		},
		{
			name: "custom languages parsed correctly",
			yamlContent: `
debug: true
db: sqlite
dsn: test.db
agent_dir: %s
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-3.1-flash-lite
chat_lang: Chinese
doc_lang: zh-CN
comment_lang: English (US)
`,
			wantChatLang:    "Chinese",
			wantDocLang:     "zh-CN",
			wantCommentLang: "English (US)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tempDir := t.TempDir()
			agentDir := filepath.Join(tempDir, "agent_root")
			require.NoError(t, os.MkdirAll(filepath.Join(agentDir, "agents"), 0755))

			yamlData := fmt.Sprintf(tt.yamlContent, agentDir)
			configPath := filepath.Join(tempDir, "config.yaml")
			require.NoError(t, os.WriteFile(configPath, []byte(yamlData), 0644))

			cfg, err := LoadConfig(configPath)
			require.NoError(t, err)
			assert.Equal(t, tt.wantChatLang, cfg.ChatLang)
			assert.Equal(t, tt.wantDocLang, cfg.DocLang)
			assert.Equal(t, tt.wantCommentLang, cfg.CommentLang)
		})
	}
}

func TestLoadConfig_UILang(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		yamlContent string
		wantUILang  string
	}{
		{
			name: "default en when ui_lang omitted",
			yamlContent: `
debug: true
db: sqlite
dsn: test.db
agent_dir: %s
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-3.1-flash-lite
`,
			wantUILang: "en",
		},
		{
			name: "default en when ui_lang empty",
			yamlContent: `
debug: true
db: sqlite
dsn: test.db
agent_dir: %s
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-3.1-flash-lite
ui_lang: ""
`,
			wantUILang: "en",
		},
		{
			name: "custom ui_lang zh-CN",
			yamlContent: `
debug: true
db: sqlite
dsn: test.db
agent_dir: %s
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-3.1-flash-lite
ui_lang: zh-CN
`,
			wantUILang: "zh-CN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tempDir := t.TempDir()
			agentDir := filepath.Join(tempDir, "agent_root")
			require.NoError(t, os.MkdirAll(filepath.Join(agentDir, "agents"), 0755))

			yamlData := fmt.Sprintf(tt.yamlContent, agentDir)
			configPath := filepath.Join(tempDir, "config.yaml")
			require.NoError(t, os.WriteFile(configPath, []byte(yamlData), 0644))

			cfg, err := LoadConfig(configPath)
			require.NoError(t, err)
			assert.Equal(t, tt.wantUILang, cfg.UILang)
			assert.Equal(t, tt.wantUILang, cfg.GetUILang())
		})
	}
}

func TestConfig_GetUILang(t *testing.T) {
	t.Parallel()

	t.Run("nil config returns default en", func(t *testing.T) {
		t.Parallel()
		var cfg *Config
		assert.Equal(t, "en", cfg.GetUILang())
	})

	t.Run("empty config returns default en", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{}
		assert.Equal(t, "en", cfg.GetUILang())
	})

	t.Run("custom config returns configured ui_lang", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{UILang: "zh-CN"}
		assert.Equal(t, "zh-CN", cfg.GetUILang())
	})
}

func TestConfig_LanguageRules(t *testing.T) {
	t.Parallel()

	t.Run("nil config returns default language rules", func(t *testing.T) {
		t.Parallel()
		var cfg *Config
		assert.Equal(t, DefaultLanguage, cfg.GetChatLang())
		assert.Equal(t, DefaultLanguage, cfg.GetDocLang())
		assert.Equal(t, DefaultLanguage, cfg.GetCommentLang())

		rules := cfg.LanguageRules()
		assert.Contains(t, rules, "## Language Preferences")
		assert.Contains(t, rules, "- Responses/Conversations: English (US)")
		assert.Contains(t, rules, "- Documents and Artifacts: English (US)")
		assert.Contains(t, rules, "- Code Comments and Docstrings: English (US)")
	})

	t.Run("empty config returns default language rules", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{}
		assert.Equal(t, DefaultLanguage, cfg.GetChatLang())
		assert.Equal(t, DefaultLanguage, cfg.GetDocLang())
		assert.Equal(t, DefaultLanguage, cfg.GetCommentLang())

		rules := cfg.LanguageRules()
		assert.Contains(t, rules, "## Language Preferences")
		assert.Contains(t, rules, "- Responses/Conversations: English (US)")
		assert.Contains(t, rules, "- Documents and Artifacts: English (US)")
		assert.Contains(t, rules, "- Code Comments and Docstrings: English (US)")
	})

	t.Run("custom config returns formatted language rules", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			ChatLang:    "Chinese",
			DocLang:     "zh-CN",
			CommentLang: "English",
		}
		assert.Equal(t, "Chinese", cfg.GetChatLang())
		assert.Equal(t, "zh-CN", cfg.GetDocLang())
		assert.Equal(t, "English", cfg.GetCommentLang())

		rules := cfg.LanguageRules()
		assert.Contains(t, rules, "## Language Preferences")
		assert.Contains(t, rules, "- Responses/Conversations: Chinese")
		assert.Contains(t, rules, "- Documents and Artifacts: zh-CN")
		assert.Contains(t, rules, "- Code Comments and Docstrings: English")
	})
}

func TestConfig_Providers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		yamlContent   string
		wantProviders []string
		wantErr       bool
		errMsg        string
	}{
		{
			name: "default when providers omitted",
			yamlContent: `
debug: true
db: sqlite
dsn: test.db
agent_dir: %s
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-3.1-flash-lite
`,
			wantProviders: []string{"agy", "opencode", "simplest"},
		},
		{
			name: "default when explicit empty list",
			yamlContent: `
debug: true
db: sqlite
dsn: test.db
agent_dir: %s
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-3.1-flash-lite
providers: []
`,
			wantProviders: []string{"agy", "opencode", "simplest"},
		},
		{
			name: "custom subset single provider",
			yamlContent: `
debug: true
db: sqlite
dsn: test.db
agent_dir: %s
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-3.1-flash-lite
providers:
  - simplest
`,
			wantProviders: []string{"simplest"},
		},
		{
			name: "custom subset multiple providers",
			yamlContent: `
debug: true
db: sqlite
dsn: test.db
agent_dir: %s
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-3.1-flash-lite
providers:
  - agy
  - opencode
`,
			wantProviders: []string{"agy", "opencode"},
		},
		{
			name: "duplicates deduplicated preserving order",
			yamlContent: `
debug: true
db: sqlite
dsn: test.db
agent_dir: %s
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-3.1-flash-lite
providers:
  - agy
  - agy
  - simplest
  - agy
`,
			wantProviders: []string{"agy", "simplest"},
		},
		{
			name: "invalid unknown provider",
			yamlContent: `
debug: true
db: sqlite
dsn: test.db
agent_dir: %s
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-3.1-flash-lite
providers:
  - invalid-cli
`,
			wantErr: true,
			errMsg:  `unsupported provider "invalid-cli"`,
		},
		{
			name: "invalid uppercase provider",
			yamlContent: `
debug: true
db: sqlite
dsn: test.db
agent_dir: %s
host: 127.0.0.1
gemini_api_key: test-key
gemini_model_for_chat_title: gemini-3.1-flash-lite
providers:
  - AGY
`,
			wantErr: true,
			errMsg:  `unsupported provider "AGY"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tempDir := t.TempDir()
			agentDir := filepath.Join(tempDir, "agent_root")
			require.NoError(t, os.MkdirAll(filepath.Join(agentDir, "agents"), 0755))

			yamlData := fmt.Sprintf(tt.yamlContent, agentDir)
			cfg, err := ParseAndValidate([]byte(yamlData))
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.errMsg)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantProviders, cfg.Providers)
		})
	}
}

func TestConfig_IsProviderEnabled_And_GetProviders(t *testing.T) {
	t.Parallel()

	t.Run("nil receiver", func(t *testing.T) {
		t.Parallel()
		var cfg *Config
		assert.Equal(t, []string{"agy", "opencode", "simplest"}, cfg.GetProviders())
		assert.True(t, cfg.IsProviderEnabled("agy"))
		assert.True(t, cfg.IsProviderEnabled("opencode"))
		assert.True(t, cfg.IsProviderEnabled("simplest"))
		assert.True(t, cfg.IsProviderEnabled("unknown"))

		// Check copy isolation
		providers := cfg.GetProviders()
		providers[0] = "mutated"
		assert.Equal(t, "agy", SupportedProviders[0])
		assert.Equal(t, []string{"agy", "opencode", "simplest"}, cfg.GetProviders())
	})

	t.Run("empty config (no providers set)", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{}
		assert.Equal(t, []string{"agy", "opencode", "simplest"}, cfg.GetProviders())
		assert.True(t, cfg.IsProviderEnabled("agy"))
		assert.True(t, cfg.IsProviderEnabled("opencode"))
		assert.True(t, cfg.IsProviderEnabled("simplest"))
		assert.True(t, cfg.IsProviderEnabled("unknown"))

		// Check copy isolation
		providers := cfg.GetProviders()
		providers[0] = "mutated"
		assert.Equal(t, []string{"agy", "opencode", "simplest"}, cfg.GetProviders())
	})

	t.Run("custom providers configured", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Providers: []string{"simplest"},
		}
		assert.Equal(t, []string{"simplest"}, cfg.GetProviders())
		assert.True(t, cfg.IsProviderEnabled("simplest"))
		assert.False(t, cfg.IsProviderEnabled("agy"))
		assert.False(t, cfg.IsProviderEnabled("opencode"))

		// Check copy isolation
		providers := cfg.GetProviders()
		providers[0] = "mutated"
		assert.Equal(t, []string{"simplest"}, cfg.GetProviders())
		assert.Equal(t, "simplest", cfg.Providers[0])
	})
}
