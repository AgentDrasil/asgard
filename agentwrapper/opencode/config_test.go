package opencode

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEnvConfig_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    map[string]string
		wantErr bool
	}{
		{
			name:  "empty content",
			input: "",
			want:  nil,
		},
		{
			name:  "whitespace only",
			input: "   \n\t  \n",
			want:  nil,
		},
		{
			name:  "comments only",
			input: "# This is a comment\n# Another comment\n",
			want:  nil,
		},
		{
			name: "nested env mapping",
			input: `
env:
  ANTHROPIC_API_KEY: "sk-ant-test"
  OPENAI_API_KEY: "sk-openai-test"
`,
			want: map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant-test",
				"OPENAI_API_KEY":    "sk-openai-test",
			},
		},
		{
			name: "flat key-value pairs",
			input: `
ANTHROPIC_API_KEY: "sk-ant-flat"
OPENAI_API_KEY: "sk-openai-flat"
`,
			want: map[string]string{
				"ANTHROPIC_API_KEY": "sk-ant-flat",
				"OPENAI_API_KEY":    "sk-openai-flat",
			},
		},
		{
			name: "mixed format with env taking precedence",
			input: `
COMMON_KEY: "from-flat"
ANTHROPIC_API_KEY: "flat-ant"
env:
  ANTHROPIC_API_KEY: "nested-ant"
  EXTRA_KEY: "nested-extra"
`,
			want: map[string]string{
				"COMMON_KEY":        "from-flat",
				"ANTHROPIC_API_KEY": "nested-ant",
				"EXTRA_KEY":         "nested-extra",
			},
		},
		{
			name: "scalar conversions for numbers and booleans",
			input: `
env:
  PORT: 8080
  DEBUG: true
  RATIO: 1.5
`,
			want: map[string]string{
				"PORT":  "8080",
				"DEBUG": "true",
				"RATIO": "1.5",
			},
		},
		{
			name: "flat scalar conversions for numbers and booleans",
			input: `
PORT: 9000
ENABLED: false
`,
			want: map[string]string{
				"PORT":    "9000",
				"ENABLED": "false",
			},
		},
		{
			name: "valid keys with underscore and digits",
			input: `
_PRIVATE_KEY_1: "val1"
CUSTOM_2ND_KEY: "val2"
`,
			want: map[string]string{
				"_PRIVATE_KEY_1": "val1",
				"CUSTOM_2ND_KEY": "val2",
			},
		},
		{
			name: "invalid flat key with hyphen",
			input: `
INVALID-KEY: "bad"
`,
			wantErr: true,
		},
		{
			name: "invalid flat key with equal sign",
			input: `
FOO=BAR: "bad"
`,
			wantErr: true,
		},
		{
			name: "invalid flat key starting with digit",
			input: `
123_BAD: "bad"
`,
			wantErr: true,
		},
		{
			name: "invalid nested key with hyphen",
			input: `
env:
  INVALID-KEY: "bad"
`,
			wantErr: true,
		},
		{
			name:    "invalid yaml syntax",
			input:   ": this is not valid yaml :",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res, err := ParseEnvConfig([]byte(tt.input))
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, res)
		})
	}
}

func TestParseEnvConfig_SecretLeakage(t *testing.T) {
	t.Parallel()

	canary := "sk-ant-supersecret123"
	// Malformed YAML with unterminated string containing canary
	malformed := "env:\n  ANTHROPIC_API_KEY: \"" + canary + "\n  foo: bar"

	_, err := ParseEnvConfig([]byte(malformed))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), canary, "malformed YAML error message must never leak secret content")

	// Another malformed YAML syntax containing canary
	malformed2 := "ANTHROPIC_API_KEY: [ " + canary + " : bad"
	_, err2 := ParseEnvConfig([]byte(malformed2))
	require.Error(t, err2)
	assert.NotContains(t, err2.Error(), canary, "malformed YAML error message must never leak secret content")
}

func TestMergeEnv_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		base  []string
		extra map[string]string
		want  []string
	}{
		{
			name:  "nil extra returns base",
			base:  []string{"A=1", "B=2"},
			extra: nil,
			want:  []string{"A=1", "B=2"},
		},
		{
			name:  "empty extra returns base",
			base:  []string{"A=1"},
			extra: map[string]string{},
			want:  []string{"A=1"},
		},
		{
			name:  "empty base appends sorted extra",
			base:  nil,
			extra: map[string]string{"Z": "last", "A": "first"},
			want:  []string{"A=first", "Z=last"},
		},
		{
			name:  "overrides existing key and appends new key",
			base:  []string{"A=old", "B=keep"},
			extra: map[string]string{"A": "new", "C": "added"},
			want:  []string{"B=keep", "A=new", "C=added"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res := mergeEnv(tt.base, tt.extra)
			assert.Equal(t, tt.want, res)
		})
	}
}

func TestDiscoveryAndLoadEnv(t *testing.T) {
	// Not parallel because it mutates environment variables.
	tempDir := t.TempDir()

	t.Run("explicit OPENCODE_AW_CONFIG takes precedence", func(t *testing.T) {
		cfgPath := filepath.Join(tempDir, "custom-opencode.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte("env:\n  API_KEY: custom-val\n"), 0o600))

		t.Setenv(EnvOpencodeAWConfig, cfgPath)
		t.Setenv("AW_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", "")

		assert.Equal(t, cfgPath, ConfigPath())

		env, err := LoadEnv()
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"API_KEY": "custom-val"}, env)
	})

	t.Run("discovers opencode.aw.yaml alongside aw config", func(t *testing.T) {
		awDir := filepath.Join(tempDir, "aw-sandbox")
		require.NoError(t, os.MkdirAll(awDir, 0o755))
		awCfg := filepath.Join(awDir, "config.yaml")
		require.NoError(t, os.WriteFile(awCfg, []byte("agents: []\n"), 0o600))
		opencodeCfg := filepath.Join(awDir, "opencode.aw.yaml")
		require.NoError(t, os.WriteFile(opencodeCfg, []byte("OPENAI_API_KEY: alongside-val\n"), 0o600))

		t.Setenv(EnvOpencodeAWConfig, "")
		t.Setenv("AW_CONFIG", awCfg)
		t.Setenv("XDG_CONFIG_HOME", "")

		assert.Equal(t, opencodeCfg, ConfigPath())

		env, err := LoadEnv()
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"OPENAI_API_KEY": "alongside-val"}, env)
	})

	t.Run("alongside aw config takes priority over XDG config", func(t *testing.T) {
		awDir := filepath.Join(tempDir, "aw-priority")
		require.NoError(t, os.MkdirAll(awDir, 0o755))
		awCfg := filepath.Join(awDir, "config.yaml")
		require.NoError(t, os.WriteFile(awCfg, []byte("agents: []\n"), 0o600))
		alongsideCfg := filepath.Join(awDir, "opencode.aw.yaml")
		require.NoError(t, os.WriteFile(alongsideCfg, []byte("KEY: alongside-winner\n"), 0o600))

		xdgHome := filepath.Join(tempDir, "xdg-loser")
		xdgAwDir := filepath.Join(xdgHome, "aw")
		require.NoError(t, os.MkdirAll(xdgAwDir, 0o755))
		xdgCfg := filepath.Join(xdgAwDir, "opencode.aw.yaml")
		require.NoError(t, os.WriteFile(xdgCfg, []byte("KEY: xdg-loser\n"), 0o600))

		t.Setenv(EnvOpencodeAWConfig, "")
		t.Setenv("AW_CONFIG", awCfg)
		t.Setenv("XDG_CONFIG_HOME", xdgHome)

		assert.Equal(t, alongsideCfg, ConfigPath())

		env, err := LoadEnv()
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"KEY": "alongside-winner"}, env)
	})

	t.Run("falls back to XDG when AW_CONFIG directory has no opencode.aw.yaml", func(t *testing.T) {
		awDir := filepath.Join(tempDir, "aw-no-opencode")
		require.NoError(t, os.MkdirAll(awDir, 0o755))
		awCfg := filepath.Join(awDir, "config.yaml")
		require.NoError(t, os.WriteFile(awCfg, []byte("agents: []\n"), 0o600))

		xdgHome := filepath.Join(tempDir, "xdg-fallback")
		xdgAwDir := filepath.Join(xdgHome, "aw")
		require.NoError(t, os.MkdirAll(xdgAwDir, 0o755))
		opencodeCfg := filepath.Join(xdgAwDir, "opencode.aw.yaml")
		require.NoError(t, os.WriteFile(opencodeCfg, []byte("KEY: from-xdg-fallback\n"), 0o600))

		t.Setenv(EnvOpencodeAWConfig, "")
		t.Setenv("AW_CONFIG", awCfg)
		t.Setenv("XDG_CONFIG_HOME", xdgHome)

		assert.Equal(t, opencodeCfg, ConfigPath())

		env, err := LoadEnv()
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"KEY": "from-xdg-fallback"}, env)
	})

	t.Run("falls back to XDG_CONFIG_HOME", func(t *testing.T) {
		xdgHome := filepath.Join(tempDir, "xdg")
		xdgAwDir := filepath.Join(xdgHome, "aw")
		require.NoError(t, os.MkdirAll(xdgAwDir, 0o755))
		opencodeCfg := filepath.Join(xdgAwDir, "opencode.aw.yaml")
		require.NoError(t, os.WriteFile(opencodeCfg, []byte("env:\n  XDG_KEY: xdg-val\n"), 0o600))

		t.Setenv(EnvOpencodeAWConfig, "")
		t.Setenv("AW_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", xdgHome)

		assert.Equal(t, opencodeCfg, ConfigPath())

		env, err := LoadEnv()
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"XDG_KEY": "xdg-val"}, env)
	})

	t.Run("falls back to HOME/.config/aw when XDG_CONFIG_HOME is empty", func(t *testing.T) {
		homeDir := filepath.Join(tempDir, "home")
		homeAwDir := filepath.Join(homeDir, ".config", "aw")
		require.NoError(t, os.MkdirAll(homeAwDir, 0o755))
		opencodeCfg := filepath.Join(homeAwDir, "opencode.aw.yaml")
		require.NoError(t, os.WriteFile(opencodeCfg, []byte("HOME_KEY: home-val\n"), 0o600))

		t.Setenv(EnvOpencodeAWConfig, "")
		t.Setenv("AW_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", homeDir)

		assert.Equal(t, opencodeCfg, ConfigPath())

		env, err := LoadEnv()
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"HOME_KEY": "home-val"}, env)
	})

	t.Run("missing file returns nil without error", func(t *testing.T) {
		t.Setenv(EnvOpencodeAWConfig, filepath.Join(tempDir, "nonexistent.yaml"))
		t.Setenv("AW_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", "")

		env, err := LoadEnv()
		require.NoError(t, err)
		assert.Nil(t, env)
	})
}

func TestInjectOpencodeEnv(t *testing.T) {
	tempDir := t.TempDir()

	t.Run("injects extra environment variables into exec.Cmd", func(t *testing.T) {
		cfgPath := filepath.Join(tempDir, "inject.yaml")
		require.NoError(t, os.WriteFile(cfgPath, []byte("env:\n  INJECTED_VAR: secret123\n"), 0o600))
		t.Setenv(EnvOpencodeAWConfig, cfgPath)

		cmd := exec.Command("opencode", "models")
		cmd.Env = []string{"EXISTING_VAR=foo"}

		err := injectOpencodeEnv(cmd)
		require.NoError(t, err)
		assert.Contains(t, cmd.Env, "EXISTING_VAR=foo")
		assert.Contains(t, cmd.Env, "INJECTED_VAR=secret123")
	})

	t.Run("leaves cmd.Env untouched when no config file exists", func(t *testing.T) {
		t.Setenv(EnvOpencodeAWConfig, "")
		t.Setenv("AW_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(tempDir, "empty_xdg"))
		t.Setenv("HOME", filepath.Join(tempDir, "empty_home"))

		cmd := exec.Command("opencode", "models")
		require.NoError(t, injectOpencodeEnv(cmd))
		assert.Nil(t, cmd.Env)
	})
}
