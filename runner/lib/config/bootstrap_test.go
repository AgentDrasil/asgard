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
