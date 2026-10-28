// Package socktest serves a fake runner callback socket for tests of the
// sandbox tools.
package socktest

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
)

// Serve serves h on a fresh unix socket and points ASGARD_STATUS_URL at it for
// the duration of the test (so the test must not be parallel). It returns the
// socket path.
func Serve(t *testing.T, h http.Handler) string {
	t.Helper()
	// A short directory keeps the path within sun_path.
	dir, err := os.MkdirTemp("", "sock")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "asgard.sock")
	ln, err := net.Listen("unix", path)
	require.NoError(t, err)
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv(sandboxsock.EnvURL, "unix://"+path)
	return path
}
