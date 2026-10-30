package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createFakeCerts(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(EnvCertsDir, dir)
	require.NoError(t, os.WriteFile(TLSCertPath(), []byte("fake cert"), 0644))
	require.NoError(t, os.WriteFile(TLSKeyPath(), []byte("fake key"), 0600))
	require.NoError(t, os.WriteFile(CAPath(), []byte("fake ca"), 0644))
}

func TestParseBootstrap(t *testing.T) {
	createFakeCerts(t)

	env := map[string]string{
		EnvBackend: "backend.example:9311",
	}
	getenv := func(k string) string { return env[k] }

	b, err := ParseBootstrap(nil, getenv)
	require.NoError(t, err)
	assert.Equal(t, "backend.example:9311", b.Backend)
	assert.Equal(t, TLSCertPath(), b.TLSCert)
	assert.Equal(t, TLSKeyPath(), b.TLSKey)
	assert.Equal(t, CAPath(), b.CA)

	b, err = ParseBootstrap([]string{"-backend", "other:1", "-server-name", "x"}, getenv)
	require.NoError(t, err)
	assert.Equal(t, "other:1", b.Backend)
	assert.Equal(t, "x", b.ServerName)

	_, err = ParseBootstrap(nil, func(string) string { return "" })
	assert.ErrorContains(t, err, "-backend")

	_, err = ParseBootstrap([]string{"-backend", "nohostport"}, getenv)
	assert.ErrorContains(t, err, "invalid backend address")
}

func TestParseBootstrap_MissingTLSFiles(t *testing.T) {
	tmpDir := t.TempDir()
	b := &Bootstrap{
		Backend: "backend.example:9311",
		TLSCert: filepath.Join(tmpDir, "tls.crt"),
		TLSKey:  filepath.Join(tmpDir, "tls.key"),
		CA:      filepath.Join(tmpDir, "ca.crt"),
	}
	err := b.Validate()
	require.Error(t, err)
	assert.ErrorContains(t, err, "missing required TLS file")
}

func TestParseBootstrapUnsandboxedCommandsIsOptIn(t *testing.T) {
	createFakeCerts(t)

	env := map[string]string{EnvBackend: "b:1"}
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
