// Package jev provides a client and types for the TypeSafe Jev System One evaluation API.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// DefaultBaseURL is the default TypeSafe API base URL.
	DefaultBaseURL = "https://api.typesafe.ai"
	// DefaultModel is the default flagship Jev model alias.
	DefaultModel = "jev-latest"
)

// QuestionType represents the kind of evaluation question.
type QuestionType string

const (
	QuestionTypeNoul   QuestionType = "noul"
	QuestionTypeChoice QuestionType = "choice"
	QuestionTypeScore  QuestionType = "score"
)

// NoulCriteria specifies optional descriptions for yes (true) and no (false) outcomes.
type NoulCriteria struct {
	True  any `json:"true,omitempty"`
	False any `json:"false,omitempty"`
}

// Question represents a typed question in a System One evaluation request.
type Question struct {
	Type         QuestionType `json:"type"`
	Instructions any          `json:"instructions"`
	Criteria     any          `json:"criteria,omitempty"`
}

// NewNoul creates a yes/no question returning the probability the answer is yes.
func NewNoul(instructions any, criteria ...NoulCriteria) Question {
	q := Question{
		Type:         QuestionTypeNoul,
		Instructions: instructions,
	}
	if len(criteria) > 0 {
		q.Criteria = criteria[0]
	}
	return q
}

// NewChoice creates a question that selects one option from a set of choices.
// Criteria maps option names to descriptions (or nil).
func NewChoice(instructions any, criteria map[string]any) Question {
	return Question{
		Type:         QuestionTypeChoice,
		Instructions: instructions,
		Criteria:     criteria,
	}
}

// NewScore creates a question that rates state along ordered criteria levels (2 to 10 levels).
func NewScore(instructions any, criteria []any) Question {
	return Question{
		Type:         QuestionTypeScore,
		Instructions: instructions,
		Criteria:     criteria,
	}
}

// EvaluateRequest defines the body for POST /v1/systemone.
type EvaluateRequest struct {
	State     any                 `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

// Answer represents the polymorphic answer returned by System One.
type Answer struct {
	Type QuestionType `json:"type"`

	// Noul fields
	Noul float64 `json:"noul,omitempty"`

	// Choice fields
	Choice string `json:"choice,omitempty"`

	// Score fields
	Score  float64           `json:"score,omitempty"`
	Legend map[string]string `json:"legend,omitempty"`

	// Common Choice and Score fields
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

// Usage reports token consumption for an evaluation request.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// EvaluateResponse is the response body returned by POST /v1/systemone.
type EvaluateResponse struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

// ModelInfo describes an available model or alias.
type ModelInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReleaseDate string `json:"release_date"`
}

// ListModelsResponse is returned by GET /v1/models.
type ListModelsResponse struct {
	Models []ModelInfo `json:"models"`
}

// APIError represents an error returned by the TypeSafe API.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("typesafe api error (status %d): %s", e.StatusCode, e.Body)
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the default base URL.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) {
		if baseURL != "" {
			c.baseURL = baseURL
		}
	}
}

// WithHTTPClient overrides the default http.Client.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

// WithDefaultModel overrides the default model for evaluation requests.
func WithDefaultModel(model string) Option {
	return func(c *Client) {
		if model != "" {
			c.defaultModel = model
		}
	}
}

// Client interacts with the TypeSafe Jev System One API.
type Client struct {
	apiKey       string
	baseURL      string
	defaultModel string
	httpClient   *http.Client
}

// NewClient creates a new TypeSafe Jev System One client.
func NewClient(apiKey string, opts ...Option) (*Client, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("api key is required")
	}

	c := &Client{
		apiKey:       apiKey,
		baseURL:      DefaultBaseURL,
		defaultModel: DefaultModel,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}

	for _, opt := range opts {
		opt(c)
	}

	return c, nil
}

// BaseURL returns the configured base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}

// DefaultModel returns the configured default model.
func (c *Client) DefaultModel() string {
	return c.defaultModel
}

// Evaluate evaluates state against questions using POST /v1/systemone.
func (c *Client) Evaluate(ctx context.Context, req EvaluateRequest) (*EvaluateResponse, error) {
	if req.Model == "" {
		req.Model = c.defaultModel
	}
	if len(req.Questions) == 0 {
		return nil, fmt.Errorf("questions map cannot be empty")
	}

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/systemone", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, &APIError{
			StatusCode: httpResp.StatusCode,
			Body:       string(respBody),
		}
	}

	var evalResp EvaluateResponse
	if err := json.Unmarshal(respBody, &evalResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &evalResp, nil
}

// ListModels queries GET /v1/models for available models.
func (c *Client) ListModels(ctx context.Context) (*ListModelsResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, &APIError{
			StatusCode: httpResp.StatusCode,
			Body:       string(respBody),
		}
	}

	var listResp ListModelsResponse
	if err := json.Unmarshal(respBody, &listResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &listResp, nil
}
