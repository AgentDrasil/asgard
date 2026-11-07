package config

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A runner whose etcd settings are unusable must stay alive and report why,
// instead of exiting and being restarted in a loop.
func TestNewRemote_UnavailableEtcdIsUnhealthyNotFatal(t *testing.T) {
	t.Setenv("ETCD_ENDPOINTS", "")

	r := NewRemote(context.Background(), "node-a", nil)
	t.Cleanup(r.Close)

	require.Error(t, r.Health())
	assert.Nil(t, r.Tree())
	_, err := r.Agents()
	assert.Error(t, err)
	_, err = r.SandboxConfig()
	assert.Error(t, err)
}
