package proxy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_ApplyDefaults(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	cfg.ApplyDefaults()

	assert.Equal(t, "127.0.0.1:8082", cfg.Server.Addr)
	assert.NotEmpty(t, cfg.Server.CACert)
	assert.NotEmpty(t, cfg.Server.CAKey)
	assert.NotContains(t, cfg.Server.CACert, "~")
	assert.NotContains(t, cfg.Server.CAKey, "~")
	assert.Equal(t, int64(4096), cfg.Debug.MaxBodyBytes)
	assert.Equal(t, "http://127.0.0.1:8082", cfg.ProxyHost())
}

func TestConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         *Config
		wantErr     bool
		errContains string
	}{
		{
			name:        "nil config",
			cfg:         nil,
			wantErr:     true,
			errContains: "config is nil",
		},
		{
			name: "disabled with empty rules is valid",
			cfg: &Config{
				Enable: false,
				Rules:  []Rule{},
			},
			wantErr: false,
		},
		{
			name: "enabled with empty rules fails closed",
			cfg: &Config{
				Enable: true,
				Rules:  []Rule{},
			},
			wantErr:     true,
			errContains: "proxy is enabled but rules list is empty",
		},
		{
			name: "rule missing host",
			cfg: &Config{
				Enable: true,
				Rules: []Rule{
					{Host: "", HeaderKey: "Authorization", RealSecret: "sk-1234"},
				},
			},
			wantErr:     true,
			errContains: "missing host",
		},
		{
			name: "rule missing header key",
			cfg: &Config{
				Enable: true,
				Rules: []Rule{
					{Host: "api.openai.com", HeaderKey: "  ", RealSecret: "sk-1234"},
				},
			},
			wantErr:     true,
			errContains: "missing header_key",
		},
		{
			name: "rule missing real secret",
			cfg: &Config{
				Enable: true,
				Rules: []Rule{
					{Host: "api.openai.com", HeaderKey: "Authorization", RealSecret: ""},
				},
			},
			wantErr:     true,
			errContains: "missing real_secret",
		},
		{
			name: "valid rules list",
			cfg: &Config{
				Enable: true,
				Rules: []Rule{
					{
						Host:        "api.openai.com",
						PathPrefix:  "/v1/",
						HeaderKey:   "Authorization",
						DummySecret: "dummy-key",
						RealSecret:  "real-key-123456",
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if tt.wantErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestConfig_DefaultAndResolvedPaths(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	require.NotNil(t, cfg)
	assert.False(t, cfg.Enable)
	assert.NotEmpty(t, cfg.ResolvedCACertPath())
	assert.NotEmpty(t, cfg.ResolvedCAKeyPath())
	assert.NotContains(t, cfg.ResolvedCACertPath(), "~")
	assert.NotContains(t, cfg.ResolvedCAKeyPath(), "~")
}

func TestConfig_EnvVars(t *testing.T) {
	t.Parallel()

	t.Run("nil config", func(t *testing.T) {
		t.Parallel()
		var cfg *Config
		assert.Nil(t, cfg.EnvVars())
	})

	t.Run("empty rules", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Rules: []Rule{}}
		assert.Nil(t, cfg.EnvVars())
	})

	t.Run("rules without env", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Rules: []Rule{
				{Host: "api.openai.com", HeaderKey: "Authorization", RealSecret: "sk-real", DummySecret: "dummy"},
			},
		}
		assert.Nil(t, cfg.EnvVars())
	})

	t.Run("rules with env and dummy secrets", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Rules: []Rule{
				{
					Host:        "api.openai.com",
					HeaderKey:   "Authorization",
					RealSecret:  "sk-real-openai",
					DummySecret: "dummy-openai-key",
					Env:         "OPENAI_API_KEY",
				},
				{
					Host:        "api.anthropic.com",
					HeaderKey:   "x-api-key",
					RealSecret:  "sk-real-anthropic",
					DummySecret: "dummy-anthropic-key",
					Env:         "ANTHROPIC_API_KEY",
				},
				{
					Host:        "api.noenv.com",
					HeaderKey:   "Authorization",
					RealSecret:  "sk-noenv",
					DummySecret: "dummy-noenv",
				},
			},
		}
		envVars := cfg.EnvVars()
		require.NotNil(t, envVars)
		assert.Equal(t, map[string]string{
			"OPENAI_API_KEY":    "dummy-openai-key",
			"ANTHROPIC_API_KEY": "dummy-anthropic-key",
		}, envVars)
	})
}
