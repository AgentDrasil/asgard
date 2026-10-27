package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
		{"http 408", &HTTPStatusError{StatusCode: 408}, true},
		{"http 409", &HTTPStatusError{StatusCode: 409}, true},
		{"http 429", &HTTPStatusError{StatusCode: 429}, true},
		{"http 500", &HTTPStatusError{StatusCode: 500}, true},
		{"http 502", &HTTPStatusError{StatusCode: 502}, true},
		{"http 503", &HTTPStatusError{StatusCode: 503}, true},
		{"http 504", &HTTPStatusError{StatusCode: 504}, true},
		{"http 520", &HTTPStatusError{StatusCode: 520}, true},
		{"http 524", &HTTPStatusError{StatusCode: 524}, true},
		{"http 400", &HTTPStatusError{StatusCode: 400}, false},
		{"http 401", &HTTPStatusError{StatusCode: 401}, false},
		{"http 404", &HTTPStatusError{StatusCode: 404}, false},
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
		{"nan", "nan", 0},
		{"infinity", "Infinity", 0},
		{"partial number", "12sec", 0},
		{"float seconds", "1.5", 0},
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

// TestCalculateDelay_UnparseableRetryAfter_FallsBackToExponential guards the
// pi fix 2bbfcca4 (use exponential backoff when Retry-After is unparseable):
// an unparseable header must yield 0 so the caller falls through to
// exponential backoff instead of propagating an invalid delay.
func TestCalculateDelay_UnparseableRetryAfter_FallsBackToExponential(t *testing.T) {
	t.Parallel()

	policy := &types.RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelayMs: 100, MaxDelayMs: 60_000}

	for _, header := range []string{"", "not-a-number", "nan", "Infinity", "12sec"} {
		err := &HTTPStatusError{StatusCode: http.StatusTooManyRequests, RetryAfter: header}
		got := CalculateDelay(0, policy, err)
		assert.Equal(t, 100*time.Millisecond, got, "header %q", header)
	}

	// attempt index still drives exponential backoff when the header is bad
	err := &HTTPStatusError{StatusCode: http.StatusServiceUnavailable, RetryAfter: "later"}
	assert.Equal(t, 400*time.Millisecond, CalculateDelay(2, policy, err))

	// a valid header still wins over exponential backoff
	err = &HTTPStatusError{StatusCode: http.StatusTooManyRequests, RetryAfter: "30"}
	assert.Equal(t, 30*time.Second, CalculateDelay(0, policy, err))
}

func TestIsRetryableError_MessagePatterns(t *testing.T) {
	t.Parallel()

	retryable := []string{
		"Overloaded",
		"server_busy",
		"Servers are currently busy",
		"currently experiencing high demand",
		"model is at capacity",
		"rate limit exceeded",
		"Too Many Requests",
		"The service is temporarily unavailable",
		"500 Internal Server Error",
		"provider returned error",
		"upstream request failed",
		"upstream_stream_read_error",
		"stream was interrupted",
		"HTTP/2 stream failed",
		"http2 stream failed",
		"network error",
		"connection error",
		"request to https://api.example/v1 failed, reason: boom",
		"getaddrinfo ENOTFOUND api.example",
		"EAI_AGAIN",
		"upstream connect error",
		"reset before headers",
		"socket hang up",
		"socket connection was closed",
		"i/o timed out",
		"request timeout",
		"connection terminated",
		"websocket closed unexpectedly",
		"websocket error",
		"stream ended without a terminal event",
		"stream ended before message_stop",
		"stream disconnected before completion",
		"pending stream has been canceled",
		"retry delay exceeded",
		"You can retry your request",
		"Try your request again",
		"Please retry your request",
		"ResourceExhausted",
		"subscription_sharing_usage_unavailable",
		"subscription_sharing_user_unavailable",
	}
	for _, msg := range retryable {
		msg := msg
		t.Run(msg, func(t *testing.T) {
			t.Parallel()
			assert.True(t, IsRetryableError(errors.New(msg)), "message %q must be retryable", msg)
		})
	}

	nonRetryable := []string{
		"GoUsageLimitError",
		"FreeUsageLimitError",
		"Monthly usage limit reached",
		"available balance is zero",
		"insufficient_quota",
		"out of budget",
		"quota exceeded",
		"billing error",
		"subscription_sharing_usage_limit_exceeded",
		"subscription_sharing_workplace_individual_usage_limit_exceeded",
	}
	for _, msg := range nonRetryable {
		msg := msg
		t.Run("non-retryable "+msg, func(t *testing.T) {
			t.Parallel()
			assert.False(t, IsRetryableError(errors.New(msg)), "message %q must not be retryable", msg)
		})
	}
}

func TestIsRetryableError_QuotaPatternsOverrideStatuses(t *testing.T) {
	t.Parallel()

	patterns := []string{
		"GoUsageLimitError",
		"FreeUsageLimitError",
		"Monthly usage limit reached",
		"available balance",
		"insufficient_quota",
		"out of budget",
		"quota exceeded",
		"billing",
		"subscription_sharing_usage_limit_exceeded",
		"subscription_sharing_workplace_individual_usage_limit_exceeded",
	}
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		for _, body := range patterns {
			body := body
			err := &HTTPStatusError{StatusCode: status, Body: body}
			assert.False(t, IsRetryableError(err), "status %d body %q must not retry", status, body)
		}
	}
}

func TestIsRetryableError_ShouldRetryHeaderOverridesStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		should string
		want   bool
	}{
		{name: "false overrides 503", status: 503, should: "false", want: false},
		{name: "FALSE overrides 429", status: 429, should: "FALSE", want: false},
		{name: "true overrides 400", status: 400, should: "true", want: true},
		{name: "TRUE overrides 401", status: 401, should: " TRUE ", want: true},
		{name: "empty leaves status retryable", status: 503, should: "", want: true},
		{name: "invalid leaves status non-retryable", status: 400, should: "yes", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := &HTTPStatusError{StatusCode: tt.status, ShouldRetry: tt.should}
			assert.Equal(t, tt.want, IsRetryableError(err))
		})
	}
}

func TestCalculateDelay_RetryAfterMs(t *testing.T) {
	t.Parallel()

	policy := &types.RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelayMs: 100, MaxDelayMs: 60_000}

	// retry-after-ms wins over both Retry-After and exponential backoff.
	err := &HTTPStatusError{StatusCode: 429, RetryAfterMs: "250", RetryAfter: "30"}
	assert.Equal(t, 250*time.Millisecond, CalculateDelay(0, policy, err))
	assert.Equal(t, 250*time.Millisecond, CalculateDelay(3, policy, err))

	// Whitespace is tolerated.
	err = &HTTPStatusError{RetryAfterMs: " 150 "}
	assert.Equal(t, 150*time.Millisecond, CalculateDelay(0, policy, err))

	// Invalid or non-positive values fall back to Retry-After.
	for _, bad := range []string{"", "abc", "1.5", "0", "-5"} {
		err := &HTTPStatusError{RetryAfterMs: bad, RetryAfter: "30"}
		assert.Equal(t, 30*time.Second, CalculateDelay(0, policy, err), "retry-after-ms %q", bad)
	}

	// Invalid retry-after-ms with no Retry-After falls back to backoff.
	err = &HTTPStatusError{RetryAfterMs: "nope"}
	assert.Equal(t, 100*time.Millisecond, CalculateDelay(0, policy, err))
	assert.Equal(t, 400*time.Millisecond, CalculateDelay(2, policy, err))
}

func TestRetry_StatusHeadersCapturedEndToEnd(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.Header().Set("retry-after-ms", "250")
		w.Header().Set("x-should-retry", "false")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("service unavailable"))
	}))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	_, err := ExecuteWithRetry(ctx, &types.RetryPolicy{Enabled: false}, func() (*http.Response, error) {
		return postSSE(ctx, srv.Client(), srv.URL, nil, []byte(`{}`), nil)
	})

	var httpErr *HTTPStatusError
	require.ErrorAs(t, err, &httpErr)
	assert.Equal(t, http.StatusServiceUnavailable, httpErr.StatusCode)
	assert.Equal(t, "2", httpErr.RetryAfter)
	assert.Equal(t, "250", httpErr.RetryAfterMs)
	assert.Equal(t, "false", httpErr.ShouldRetry)
	assert.False(t, IsRetryableError(httpErr), "x-should-retry false must override 503")
	assert.Equal(t, 250*time.Millisecond, CalculateDelay(0, nil, httpErr))
}

func TestIsRetryableError_SeparatorVariants(t *testing.T) {
	t.Parallel()

	retryable := []string{
		"rate_limit_exceeded",
		"server_error",
		"internal_error",
		"network_error",
		"connection_refused",
		"connection_lost",
		"websocket_error",
		"websocket_closed",
		"upstream_connect",
		"provider_returned_error",
		"provider returned an error",
		"upstream_request_failed",
		"service_unavailable",
		"Service is temporarily unavailable",
		"broken pipe",
		"exceeded request buffer limit while retrying upstream",
		"unable to forward upstream",
		"other side closed",
		"fetch failed",
		"stream ended before a terminal response event",
		"http2 request did not get a response",
		"http/2 stream failed",
		"http2 stream failed",
		"request to https://api.example/v1 failed, reason: boom",
	}
	for _, msg := range retryable {
		msg := msg
		t.Run(msg, func(t *testing.T) {
			t.Parallel()
			assert.True(t, IsRetryableError(errors.New(msg)), "message %q must be retryable", msg)
		})
	}

	notRetryable := []string{
		"validation failed, reason: bad schema",
		"req_011CU503xyz",
		"5316784 bytes > 5242880 bytes",
	}
	for _, msg := range notRetryable {
		msg := msg
		t.Run("not retryable "+msg, func(t *testing.T) {
			t.Parallel()
			assert.False(t, IsRetryableError(errors.New(msg)), "message %q must not be retryable", msg)
		})
	}
}

func TestIsRetryableError_StandaloneStatusDigits(t *testing.T) {
	t.Parallel()

	assert.True(t, IsRetryableError(errors.New("520 status code (no body)")))
	assert.True(t, IsRetryableError(errors.New("524 status code (no body)")))
	assert.True(t, IsRetryableError(errors.New("upstream failed with 503")))
	assert.False(t, IsRetryableError(errors.New("req_011CU503xyz")))
	assert.False(t, IsRetryableError(errors.New("5316784 bytes > 5242880 bytes")))
}

func TestIsRetryableError_ShouldRetryCombinedPrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
		header string
		want   bool
	}{
		{name: "quota beats header true on 429", status: 429, body: "insufficient_quota", header: "true", want: false},
		{name: "header false on 503", status: 503, body: "", header: "false", want: false},
		{name: "header true on 400", status: 400, body: "", header: "true", want: true},
		{name: "quota beats header true on 503", status: 503, body: "quota exceeded", header: "true", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := &HTTPStatusError{StatusCode: tt.status, Body: tt.body, ShouldRetry: tt.header}
			assert.Equal(t, tt.want, IsRetryableError(err))
		})
	}
}

func TestIsRetryableError_GenaiPointerCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		code int
		want bool
	}{
		{code: 429, want: true},
		{code: 503, want: true},
		{code: 400, want: false},
		{code: 404, want: false},
	}
	for _, tt := range tests {
		err := &genai.APIError{Code: tt.code}
		assert.Equal(t, tt.want, IsRetryableError(err), "code %d", tt.code)
	}
}

func TestRetry_ShouldRetryFalseStopsAfterOneCall(t *testing.T) {
	t.Parallel()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("x-should-retry", "false")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("service unavailable"))
	}))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	_, err := ExecuteWithRetry(ctx, &types.RetryPolicy{
		Enabled: true, MaxRetries: 3, BaseDelayMs: 10,
	}, func() (*http.Response, error) {
		return postSSE(ctx, srv.Client(), srv.URL, nil, []byte(`{}`), nil)
	})

	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls),
		"x-should-retry false must stop retries after the first call")
}
