package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

func TestRetry_NilPolicy_DefaultsToEnabled(t *testing.T) {
	t.Parallel()

	calls := 0
	ctx := context.Background()
	res, err := ExecuteWithRetry(ctx, nil, func() (string, error) {
		calls++
		if calls == 1 {
			return "", &HTTPStatusError{StatusCode: http.StatusServiceUnavailable, Body: "service unavailable"}
		}
		return "ok", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "ok", res)
	assert.Equal(t, 2, calls)
}

func TestRetry_ExplicitDisabled(t *testing.T) {
	t.Parallel()

	calls := 0
	ctx := context.Background()
	policy := &types.RetryPolicy{Enabled: false}
	res, err := ExecuteWithRetry(ctx, policy, func() (string, error) {
		calls++
		return "", &HTTPStatusError{StatusCode: http.StatusServiceUnavailable, Body: "service unavailable"}
	})

	require.Error(t, err)
	var httpErr *HTTPStatusError
	require.True(t, errors.As(err, &httpErr))
	assert.Equal(t, http.StatusServiceUnavailable, httpErr.StatusCode)
	assert.Empty(t, res)
	assert.Equal(t, 1, calls)
}

func TestRetry_RespectRetryAfter(t *testing.T) {
	t.Parallel()

	calls := 0
	ctx := context.Background()
	start := time.Now()
	var capturedErr *HTTPStatusError

	res, err := ExecuteWithRetry(ctx, &types.RetryPolicy{
		Enabled:     true,
		MaxRetries:  2,
		BaseDelayMs: 10,
		MaxDelayMs:  5000,
	}, func() (string, error) {
		calls++
		if calls == 1 {
			capturedErr = &HTTPStatusError{
				StatusCode: http.StatusTooManyRequests,
				Body:       "rate limit exceeded",
				RetryAfter: "1",
			}
			return "", capturedErr
		}
		return "recovered", nil
	})

	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.Equal(t, "recovered", res)
	assert.Equal(t, 2, calls)
	require.NotNil(t, capturedErr)
	assert.Equal(t, "1", capturedErr.RetryAfter)
	assert.GreaterOrEqual(t, elapsed, 1*time.Second)
}

func TestRetry_After_HeaderCapturedEndToEnd(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limit exceeded"))
	}))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	policy := &types.RetryPolicy{Enabled: false}

	_, err := ExecuteWithRetry(ctx, policy, func() (*http.Response, error) {
		return postSSE(ctx, srv.Client(), srv.URL, nil, []byte(`{}`), nil)
	})

	var httpErr *HTTPStatusError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusTooManyRequests, httpErr.StatusCode)
	assert.Equal(t, "1", httpErr.RetryAfter)
	assert.Equal(t, time.Second, ParseRetryAfter(httpErr.RetryAfter))
}

func TestRetry_NonRetryableError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
	}{
		{"bad request", http.StatusBadRequest},
		{"unauthorized", http.StatusUnauthorized},
		{"forbidden", http.StatusForbidden},
		{"not found", http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls := 0
			ctx := context.Background()
			policy := &types.RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelayMs: 10}
			res, err := ExecuteWithRetry(ctx, policy, func() (string, error) {
				calls++
				return "", &HTTPStatusError{StatusCode: tt.statusCode, Body: "client error"}
			})

			require.Error(t, err)
			var httpErr *HTTPStatusError
			require.True(t, errors.As(err, &httpErr))
			assert.Equal(t, tt.statusCode, httpErr.StatusCode)
			assert.Empty(t, res)
			assert.Equal(t, 1, calls)
		})
	}
}

func TestRetry_ContextAborted(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel before executing

	calls := 0
	policy := &types.RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelayMs: 10}
	res, err := ExecuteWithRetry(ctx, policy, func() (string, error) {
		calls++
		return "ok", nil
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, res)
	assert.Equal(t, 0, calls)
}

type fakeTimeoutError struct{}

func (fakeTimeoutError) Error() string   { return "i/o timeout" }
func (fakeTimeoutError) Timeout() bool   { return true }
func (fakeTimeoutError) Temporary() bool { return true }

func TestIsRetryableError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"context canceled", context.Canceled, false},
		{"context deadline", context.DeadlineExceeded, false},
		{"http 429", &HTTPStatusError{StatusCode: 429}, true},
		{"http 500", &HTTPStatusError{StatusCode: 500}, true},
		{"http 502", &HTTPStatusError{StatusCode: 502}, true},
		{"http 503", &HTTPStatusError{StatusCode: 503}, true},
		{"http 504", &HTTPStatusError{StatusCode: 504}, true},
		{"http 400", &HTTPStatusError{StatusCode: 400}, false},
		{"http 401", &HTTPStatusError{StatusCode: 401}, false},
		{"genai 503 value", genai.APIError{Code: 503}, true},
		{"genai 429 value", genai.APIError{Code: 429}, true},
		{"genai 500 ptr", &genai.APIError{Code: 500}, true},
		{"genai 400 value", genai.APIError{Code: 400}, false},
		{"net timeout error", fakeTimeoutError{}, true},
		{"connection reset string", errors.New("read: connection reset by peer"), true},
		{"connection refused string", errors.New("dial tcp: connection refused"), true},
		{"generic error", errors.New("unexpected token"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := IsRetryableError(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"empty", "", 0},
		{"zero seconds", "0", 0},
		{"negative seconds", "-5", 0},
		{"positive seconds", "120", 120 * time.Second},
		{"whitespace seconds", "  5  ", 5 * time.Second},
		{"invalid string", "not-a-number", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ParseRetryAfter(tt.header)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("http-date in future", func(t *testing.T) {
		t.Parallel()
		future := time.Now().Add(10 * time.Second).UTC()
		header := future.Format(http.TimeFormat)
		got := ParseRetryAfter(header)
		assert.Greater(t, got, 8*time.Second)
		assert.LessOrEqual(t, got, 11*time.Second)
	})

	t.Run("http-date in past", func(t *testing.T) {
		t.Parallel()
		past := time.Now().Add(-10 * time.Second).UTC()
		header := past.Format(http.TimeFormat)
		got := ParseRetryAfter(header)
		assert.Equal(t, time.Duration(0), got)
	})
}
