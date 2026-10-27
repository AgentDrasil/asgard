package provider

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// Non-retryable subscription/quota/billing limits: permanent for the retry
// window even when the provider reports them with a retryable status (429/503).
var nonRetryableProviderPatterns = []string{
	"gousagelimiterror",
	"freeusagelimiterror",
	"monthly usage limit reached",
	"available balance",
	"insufficient_quota",
	"out of budget",
	"quota exceeded",
	"billing",
	"subscription_sharing_usage_limit_exceeded",
	"subscription_sharing_workplace_individual_usage_limit_exceeded",
}

// Transient provider/transport literals. Matched case-insensitively as
// substrings after status/code/network classification, mirroring upstream
// retry.ts. Separator variants live in retryableProviderRegexes.
var retryableProviderPatterns = []string{
	"overloaded",
	"server_busy",
	"servers are currently busy",
	"currently experiencing high demand",
	"model is at capacity",
	"too many requests",
	"exceeded request buffer limit while retrying upstream",
	"unable to forward upstream",
	"upstream_stream_read_error",
	"stream was interrupted",
	"other side closed",
	"fetch failed",
	"connection reset",
	"getaddrinfo",
	"enotfound",
	"eai_again",
	"reset before headers",
	"socket hang up",
	"socket connection was closed",
	"broken pipe",
	"timed out",
	"timeout",
	"terminated",
	"ended without",
	"stream ended before message_stop",
	"stream ended before a terminal response event",
	"http2 request did not get a response",
	"stream disconnected before completion",
	"pending stream has been canceled",
	"retry delay",
	"you can retry your request",
	"try your request again",
	"please retry your request",
	"resourceexhausted",
	"subscription_sharing_usage_unavailable",
	"subscription_sharing_user_unavailable",
}

// retryableProviderRegexes port upstream's separator-flexible patterns (spaces,
// underscores, hyphens, dots) plus the bounded standalone-status check.
var retryableProviderRegexes = []*regexp.Regexp{
	regexp.MustCompile(`rate.?limit`),
	regexp.MustCompile(`(^|[^a-z0-9])(429|500|502|503|504|520|524)([^a-z0-9]|$)`),
	regexp.MustCompile(`server.?error`),
	regexp.MustCompile(`internal.?error`),
	regexp.MustCompile(`network.?error`),
	regexp.MustCompile(`connection.?(error|refused|lost)`),
	regexp.MustCompile(`websocket.?(closed|error)`),
	regexp.MustCompile(`upstream.?connect`),
	regexp.MustCompile(`upstream.?request.?failed`),
	regexp.MustCompile(`provider.?returned.?(an.?)?error`),
	regexp.MustCompile(`service.?(is.?)?(temporarily.?)?unavailable`),
	regexp.MustCompile(`http/?2 stream failed`),
	regexp.MustCompile(`request to \S+ failed, reason:`),
}

func matchesAnyPattern(msg string, patterns []string) bool {
	msg = strings.ToLower(msg)
	for _, p := range patterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

func matchesAnyRegex(msg string, regexes []*regexp.Regexp) bool {
	msg = strings.ToLower(msg)
	for _, re := range regexes {
		if re.MatchString(msg) {
			return true
		}
	}
	return false
}

func retryableStatus(code int) bool {
	return code == http.StatusRequestTimeout || // 408
		code == http.StatusConflict || // 409
		code == http.StatusTooManyRequests || // 429
		code >= 500
}

// headerShouldRetry parses x-should-retry. ok is false when the header does not
// request an explicit decision.
func headerShouldRetry(v string) (retry bool, ok bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

// IsRetryableError reports whether err is transient and safe to retry.
// Context cancellations/deadlines are never retryable. x-should-retry wins over
// status; permanent quota/billing limits win over retryable 429/503 statuses.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) && httpErr != nil {
		shouldRetry, hasHeader := headerShouldRetry(httpErr.ShouldRetry)
		if hasHeader && !shouldRetry {
			return false
		}
		if matchesAnyPattern(httpErr.Body, nonRetryableProviderPatterns) {
			return false
		}
		if hasHeader {
			return true
		}
		return retryableStatus(httpErr.StatusCode)
	}

	msg := err.Error()
	if matchesAnyPattern(msg, nonRetryableProviderPatterns) {
		return false
	}

	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		return retryableStatus(apiErr.Code)
	}
	var apiErrPtr *genai.APIError
	if errors.As(err, &apiErrPtr) && apiErrPtr != nil {
		return retryableStatus(apiErrPtr.Code)
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}

	return matchesAnyPattern(msg, retryableProviderPatterns) ||
		matchesAnyRegex(msg, retryableProviderRegexes)
}

// ParseRetryAfter parses a Retry-After header string into a time.Duration.
// It supports both integer seconds (e.g. "120") and HTTP-date formats.
// If the header is invalid or in the past, it returns 0.
// Note: HTTP-date values are parsed with time.Parse, which assumes the
// server timestamp is UTC — valid per RFC 9110 (Retry-After HTTP-dates MUST
// be GMT), so no explicit zone handling is needed.
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
// A positive retry-after-ms header wins, then a valid Retry-After header.
// Otherwise, exponential backoff (base * 2^attempt) capped by MaxDelayMs is applied.
func CalculateDelay(attempt int, policy *types.RetryPolicy, err error) time.Duration {
	if policy == nil {
		p := types.DefaultRetryPolicy()
		policy = &p
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) && httpErr != nil {
		if ms, err := strconv.Atoi(strings.TrimSpace(httpErr.RetryAfterMs)); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
		if httpErr.RetryAfter != "" {
			if d := ParseRetryAfter(httpErr.RetryAfter); d > 0 {
				return d
			}
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
