package types

// RetryPolicy defines the retry behavior for provider API calls.
type RetryPolicy struct {
	Enabled     bool `json:"enabled"`
	MaxRetries  int  `json:"maxRetries"`
	BaseDelayMs int  `json:"baseDelayMs"`
	MaxDelayMs  int  `json:"maxDelayMs"`
}

// DefaultRetryPolicy returns the standard default retry configuration:
// enabled, 3 retries, exponential backoff from 500ms up to 30000ms.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		Enabled:     true,
		MaxRetries:  3,
		BaseDelayMs: 500,
		MaxDelayMs:  30000,
	}
}
