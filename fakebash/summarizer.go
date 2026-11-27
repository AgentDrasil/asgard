package fakebash

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"google.golang.org/genai"
)

const (
	DefaultGeminiModel   = "gemini-2.5-flash-lite"
	DefaultGeminiTimeout = 5000 * time.Millisecond
	TenKBThreshold       = 10 * 1024 // 10KB threshold
)

// failureKeywordRegex matches failure indicators in logs for heuristic context extraction.
var failureKeywordRegex = regexp.MustCompile(`(?i)(fail|panic:|error:|fatal:|build failed)`)

// Summarizer defines the interface for Level 2 intelligent command output summarization.
type Summarizer interface {
	Summarize(ctx context.Context, cmd string, exitCode int, strategy Strategy, cmdID string, totalBytes int64) (string, ModelUsage, error)
}

// GenAISummarizer implements Level 2 summarization using Google GenAI SDK.
type GenAISummarizer struct {
	storage Storage
	client  *genai.Client
	model   string
	timeout time.Duration
	apiKey  string
}

// SummarizerOption allows configuring a GenAISummarizer.
type SummarizerOption func(*GenAISummarizer)

// WithGenAIClient overrides the genai.Client (useful for testing).
func WithGenAIClient(client *genai.Client) SummarizerOption {
	return func(s *GenAISummarizer) {
		s.client = client
	}
}

// WithGeminiAPIKey sets the Gemini API key explicitly instead of reading
// GEMINI_API_KEY from the environment. The runner uses it with the key from its
// configuration.
func WithGeminiAPIKey(key string) SummarizerOption {
	return func(s *GenAISummarizer) {
		s.apiKey = key
	}
}

// WithModel overrides the Gemini model name.
func WithModel(model string) SummarizerOption {
	return func(s *GenAISummarizer) {
		s.model = model
	}
}

// WithSummarizerTimeout overrides the Gemini call timeout.
func WithSummarizerTimeout(timeout time.Duration) SummarizerOption {
	return func(s *GenAISummarizer) {
		s.timeout = timeout
	}
}

// NewGenAISummarizer creates a new Level 2 Summarizer.
func NewGenAISummarizer(storage Storage, opts ...SummarizerOption) *GenAISummarizer {
	model := os.Getenv("GEMINI_MODEL_FOR_COMMAND_RESULT_COMPASS")
	if model == "" {
		model = DefaultGeminiModel
	}

	timeout := DefaultGeminiTimeout
	if envTimeout := os.Getenv("ASGARD_GEMINI_TIMEOUT_MS"); envTimeout != "" {
		if ms, err := strconv.Atoi(envTimeout); err == nil && ms > 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
	}

	s := &GenAISummarizer{
		storage: storage,
		model:   model,
		timeout: timeout,
	}

	for _, opt := range opts {
		opt(s)
	}

	if s.client == nil {
		apiKey := s.apiKey
		if apiKey == "" {
			apiKey = os.Getenv("GEMINI_API_KEY")
		}
		if apiKey != "" {
			ctx := context.Background()
			client, err := genai.NewClient(ctx, &genai.ClientConfig{
				APIKey:  apiKey,
				Backend: genai.BackendGeminiAPI,
			})
			if err == nil {
				s.client = client
			} else {
				log.Debug().Err(err).Msg("failed to initialize genai client; will fallback to raw log")
			}
		} else {
			log.Debug().Msg("GEMINI_API_KEY is not set; will fallback to raw log")
		}
	}

	return s
}

// extractInspectorSnippet extracts an Inspector context slice from raw string content.
// 1. Regex match failure keywords: (?i)(fail|panic:|error:|fatal:|build failed)
// 2. Around matching lines, grab 15 lines before and 35 lines after
// 3. Combine with file's first 50 lines and last 50 lines
func extractInspectorSnippet(rawContent string) string {
	return ExtractInspectorLines(rawContent)
}

// Summarize performs Level 2 summarization based on strategy and log size.
// It returns the summary text plus the token cost of the call. If anything
// fails or the client is unavailable, it gracefully returns raw output and a
// zero ModelUsage without error.
func (s *GenAISummarizer) Summarize(ctx context.Context, cmd string, exitCode int, strategy Strategy, cmdID string, totalBytes int64) (string, ModelUsage, error) {
	rawPayload, err := s.storage.ReadPayload(cmdID)
	if err != nil {
		log.Debug().Err(err).Str("cmd_id", cmdID).Msg("failed to read raw payload from storage")
		return "", ModelUsage{}, err
	}

	payloadStr := string(rawPayload)

	if s.client == nil {
		log.Debug().Msg("genai client not available; returning raw payload")
		return payloadStr, ModelUsage{}, nil
	}

	inputLog, inspectorMode := SummaryInput(payloadStr, totalBytes)
	text, usage := s.SummarizeLog(ctx, cmd, exitCode, strategy, inputLog, inspectorMode)
	if text == "" {
		return payloadStr, usage, nil
	}
	return text, usage, nil
}

// SummaryInput picks what the summarizer is shown: the full payload for small
// outputs, the inspector extract (failure windows plus head and tail) for large
// ones.
func SummaryInput(payload string, totalBytes int64) (inputLog string, inspectorMode bool) {
	if totalBytes < TenKBThreshold {
		return payload, false
	}
	return extractInspectorSnippet(payload), true
}

// SummarizeLog is the storage-free half of Summarize, also used by the runner
// on behalf of a sandbox. It returns the model's summary and usage; an empty
// summary means the caller should keep the raw output (no client, the call
// failed or timed out, or the model said nothing).
func (s *GenAISummarizer) SummarizeLog(ctx context.Context, cmd string, exitCode int, strategy Strategy, inputLog string, inspectorMode bool) (string, ModelUsage) {
	if s.client == nil {
		log.Debug().Msg("genai client not available; keeping raw output")
		return "", ModelUsage{}
	}

	systemPrompt := `You are a high-signal developer CLI output summarizer.
Your goal is to extract concise, actionable diagnostic information from the command execution output.
- Strictly extract: failure root causes, compilation/linker error messages with file and line numbers, panic stack traces, and failed test case names.
- Strictly omit: successful test names, compilation progress bars, download percentages, non-error notices, and repetitive boilerplate.
- Keep the output concise and formatted for a developer terminal.`

	prompt := fmt.Sprintf("Command: %s\nExit Code: %d\nStrategy: %s\nInspector Mode: %t\n\nCommand Output:\n%s\n\nPlease provide the condensed diagnosis:",
		cmd, exitCode, strategy, inspectorMode, inputLog)

	evalCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Role: "user",
			Parts: []*genai.Part{
				{Text: systemPrompt},
			},
		},
	}

	contents := []*genai.Content{
		{
			Role: "user",
			Parts: []*genai.Part{
				{Text: prompt},
			},
		},
	}

	resp, err := s.client.Models.GenerateContent(evalCtx, s.model, contents, config)
	if err != nil {
		log.Debug().Err(err).Msg("gemini summarization failed or timed out; falling back to raw payload")
		return "", ModelUsage{}
	}

	usage := ModelUsage{Issued: true}
	if resp.UsageMetadata != nil {
		usage.Tokens = int64(resp.UsageMetadata.TotalTokenCount)
	}

	text := resp.Text()
	if strings.TrimSpace(text) == "" {
		log.Debug().Msg("gemini returned empty summary; falling back to raw payload")
		return "", usage
	}

	return text, usage
}

const (
	maxInspectorKeywordMatches = 20
	maxInspectorOutputBytes    = 32 * 1024
	maxInspectorLineBytes      = 4096
)

// ExtractInspectorLines exposes inspector line extraction for testing.
// For outputs >= 10KB, it extracts failure context windows (before 15, after 35 lines)
// combined with the first 50 and last 50 lines, capped strictly to prevent unbounded LLM prompts.
func ExtractInspectorLines(content string) string {
	if len(content) < TenKBThreshold {
		return content
	}

	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	// If the entire >=10KB content is a single or few massive lines without standard line breaks,
	// bound the output directly by slicing head and tail chunks to strictly honor token boundaries.
	if totalLines <= 5 {
		const singleLineChunk = 4096
		if len(content) > 2*singleLineChunk {
			return content[:singleLineChunk] + "\n... [Inspector truncated noisy inline data] ...\n" + content[len(content)-singleLineChunk:]
		}
		return content
	}

	included := make([]bool, totalLines)
	for i := 0; i < 50 && i < totalLines; i++ {
		included[i] = true
	}
	startLast50 := totalLines - 50
	if startLast50 < 0 {
		startLast50 = 0
	}
	for i := startLast50; i < totalLines; i++ {
		included[i] = true
	}

	matches := 0
	for i, line := range lines {
		if failureKeywordRegex.MatchString(line) {
			matches++
			if matches > maxInspectorKeywordMatches {
				break
			}
			start := i - 15
			if start < 0 {
				start = 0
			}
			end := i + 35
			if end >= totalLines {
				end = totalLines - 1
			}
			for j := start; j <= end; j++ {
				included[j] = true
			}
		}
	}

	var sb strings.Builder
	inEllipsis := false
	for i := range lines {
		if included[i] {
			if inEllipsis {
				sb.WriteString("\n... [Inspector truncated noisy lines] ...\n\n")
				inEllipsis = false
			}
			line := lines[i]
			if len(line) > maxInspectorLineBytes {
				line = line[:maxInspectorLineBytes] + " ...[truncated long line]"
			}
			sb.WriteString(line)
			sb.WriteString("\n")
			if sb.Len() >= maxInspectorOutputBytes {
				sb.WriteString("\n... [Inspector capped at maximum context threshold] ...\n")
				break
			}
		} else {
			inEllipsis = true
		}
	}
	return sb.String()
}
