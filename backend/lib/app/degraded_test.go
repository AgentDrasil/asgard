package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	return ln.Addr().String()
}

func TestRetryConfig_RetriesUntilConfigLoads(t *testing.T) {
	addr := freeAddr(t)
	calls := 0
	want := &App{}
	statusSeen := make(chan string, 1)

	got, err := retryConfig(context.Background(), 50*time.Millisecond, addr, func() (*App, error) {
		calls++
		if calls < 3 {
			if calls == 2 {
				// The degraded endpoint is up while the configuration is missing.
				resp, err := http.Get("http://" + addr + "/api/system/status")
				if err == nil {
					b, _ := io.ReadAll(resp.Body)
					_ = resp.Body.Close()
					statusSeen <- string(b)
				}
			}
			return nil, fmt.Errorf("%w: sops signature mismatch", ErrConfigUnavailable)
		}
		return want, nil
	})

	require.NoError(t, err)
	assert.Same(t, want, got)
	assert.Equal(t, 3, calls)
	select {
	case body := <-statusSeen:
		assert.Contains(t, body, `"status":"degraded"`)
		assert.Contains(t, body, "sops signature mismatch", "the reason must be visible without the logs")
	default:
		t.Fatal("degraded status endpoint did not answer")
	}
	// The degraded endpoint released the port for the real server.
	ln, err := net.Listen("tcp", addr)
	require.NoError(t, err)
	_ = ln.Close()
}

func TestRetryConfig_OtherErrorsAreNotRetried(t *testing.T) {
	boom := errors.New("database broken")
	calls := 0
	_, err := retryConfig(context.Background(), time.Millisecond, freeAddr(t), func() (*App, error) {
		calls++
		return nil, boom
	})
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 1, calls)
}

func TestRetryConfig_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := retryConfig(ctx, time.Hour, freeAddr(t), func() (*App, error) {
		calls++
		cancel()
		return nil, ErrConfigUnavailable
	})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, calls)
}
