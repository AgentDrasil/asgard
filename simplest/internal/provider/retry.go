package provider

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// IsRetryableError returns true if err is a transient network error or a
// server error (429, 500, 502, 503, 504) suitable for retrying. Context
// cancellations and client errors (e.g. 400, 401) are not retryable.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) && httpErr != nil {
		switch httpErr.StatusCode {
		case http.StatusTooManyRequests, // 429
			http.StatusInternalServerError, // 500
			http.StatusBadGateway,          // 502
			http.StatusServiceUnavailable,  // 503
			http.StatusGatewayTimeout:      // 504
			return true
		default:
			return false
		}
	}

	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case 429, 500, 502, 503, 504:
			return true
		default:
			return false
		}
	}
	var apiErrPtr *genai.APIError
	if errors.As(err, &apiErrPtr) && apiErrPtr != nil {
		switch apiErrPtr.Code {
		case 429, 500, 502, 503, 504:
			return true
		default:
			return false
		}
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return true
		}
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}

	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "broken pipe") {
		return true
	}

	return false
}

// ParseRetryAfter parses a Retry-After header string into a time.Duration.
// It supports both integer seconds (e.g. "120") and HTTP-date formats.
// If the header is invalid or in the past, it returns 0.
func ParseRetryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if secs, err := strconv.Atoi(header); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	for _, layout := range []string{http.TimeFormat, time.RFC850, time.ANSIC, time.RFC1123, time.RFC1123Z} {
		if t, err := time.Parse(layout, header); err == nil {
			d := time.Until(t)
			if d <= 0 {
				return 0
			}
			return d
		}
	}
	return 0
}

// CalculateDelay computes the sleep duration for the given attempt (0-indexed).
// If err is an HTTPStatusError with a valid Retry-After header, that duration is used.
// Otherwise, exponential backoff (base * 2^attempt) capped by MaxDelayMs is applied.
func CalculateDelay(attempt int, policy *types.RetryPolicy, err error) time.Duration {
	if policy == nil {
		p := types.DefaultRetryPolicy()
		policy = &p
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) && httpErr != nil && httpErr.RetryAfter != "" {
		if d := ParseRetryAfter(httpErr.RetryAfter); d > 0 {
			return d
		}
	}
	base := policy.BaseDelayMs
	if base <= 0 {
		base = 500
	}
	shift := attempt
	if shift < 0 {
		shift = 0
	} else if shift > 30 {
		shift = 30
	}
	delay := time.Duration(base*(1<<shift)) * time.Millisecond
	if policy.MaxDelayMs > 0 && delay > time.Duration(policy.MaxDelayMs)*time.Millisecond {
		delay = time.Duration(policy.MaxDelayMs) * time.Millisecond
	}
	return delay
}

// ExecuteWithRetry runs op with retry logic according to policy.
// If policy is nil, DefaultRetryPolicy() is used.
// Retries are triggered only for retryable errors (see IsRetryableError).
func ExecuteWithRetry[T any](ctx context.Context, policy *types.RetryPolicy, op func() (T, error)) (T, error) {
	if policy == nil {
		p := types.DefaultRetryPolicy()
		policy = &p
	}
	var zero T
	maxRetries := 0
	if policy.Enabled {
		maxRetries = policy.MaxRetries
	}

	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}

		res, err := op()
		if err == nil {
			return res, nil
		}

		if ctx.Err() != nil {
			return zero, ctx.Err()
		}

		if !policy.Enabled || attempt >= maxRetries || !IsRetryableError(err) {
			return zero, err
		}

		delay := CalculateDelay(attempt, policy, err)

		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(delay):
		}
	}
}
