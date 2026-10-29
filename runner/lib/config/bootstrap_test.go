package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseBootstrap(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		EnvBackend: "backend.example:9311",
		EnvTLSCert: "/c.crt",
		EnvTLSKey:  "/c.key",
		EnvCA:      "/ca.crt",
	}
	getenv := func(k string) string { return env[k] }

	b, err := ParseBootstrap(nil, getenv)
	require.NoError(t, err)
	assert.Equal(t, "backend.example:9311", b.Backend)
	assert.Equal(t, "/ca.crt", b.CA)

	b, err = ParseBootstrap([]string{"-backend", "other:1", "-server-name", "x"}, getenv)
	require.NoError(t, err)
	assert.Equal(t, "other:1", b.Backend)
	assert.Equal(t, "x", b.ServerName)

	_, err = ParseBootstrap(nil, func(string) string { return "" })
	assert.ErrorContains(t, err, "-backend")

	_, err = ParseBootstrap([]string{"-backend", "nohostport"}, getenv)
	assert.ErrorContains(t, err, "invalid backend address")
}

func TestParseBootstrapUnsandboxedCommandsIsOptIn(t *testing.T) {
	t.Parallel()
	env := map[string]string{EnvBackend: "b:1", EnvTLSCert: "/c", EnvTLSKey: "/k", EnvCA: "/ca"}
	parse := func(extra map[string]string, args ...string) bool {
		getenv := func(k string) string {
			if v, ok := extra[k]; ok {
				return v
			}
			return env[k]
		}
		b, err := ParseBootstrap(args, getenv)
		require.NoError(t, err)
		return b.AllowUnsandboxedCommands
	}
	assert.False(t, parse(nil), "off by default")
	assert.False(t, parse(map[string]string{EnvAllowUnsandboxedCommands: "garbage"}))
	assert.True(t, parse(map[string]string{EnvAllowUnsandboxedCommands: "1"}))
	assert.True(t, parse(map[string]string{EnvAllowUnsandboxedCommands: "true"}))
	assert.True(t, parse(nil, "-allow-unsandboxed-commands"))
	assert.False(t, parse(map[string]string{EnvAllowUnsandboxedCommands: "1"}, "-allow-unsandboxed-commands=false"))
}
