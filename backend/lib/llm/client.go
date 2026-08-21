package llm

import (
	"context"

	"google.golang.org/genai"
)

// GenerateOptions describes the parameters for a single text generation call.
type GenerateOptions struct {
	Model        string
	SystemPrompt string
	Prompt       string
	Temperature  *float32
}

// Client is the minimal interface for text generation used across asgard
// (session title generation today, workflow LLM nodes in later phases).
type Client interface {
	GenerateText(ctx context.Context, opts GenerateOptions) (string, error)
}

type genaiClientWrapper struct {
	client *genai.Client
}

// NewClient creates a genai-backed Client using the given API key.
func NewClient(ctx context.Context, apiKey string) (Client, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: apiKey})
	if err != nil {
		return nil, err
	}
	return &genaiClientWrapper{client: client}, nil
}

func (w *genaiClientWrapper) GenerateText(ctx context.Context, opts GenerateOptions) (string, error) {
	config := &genai.GenerateContentConfig{}
	if opts.SystemPrompt != "" {
		config.SystemInstruction = &genai.Content{
			Parts: []*genai.Part{{Text: opts.SystemPrompt}},
		}
	}
	if opts.Temperature != nil {
		config.Temperature = opts.Temperature
	}

	resp, err := w.client.Models.GenerateContent(ctx, opts.Model, genai.Text(opts.Prompt), config)
	if err != nil {
		return "", err
	}
	return resp.Text(), nil
}
