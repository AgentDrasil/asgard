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
	Summarize(ctx context.Context, cmd string, exitCode int, strategy Strategy, cmdID string, totalBytes int64) (string, error)
}

// GenAISummarizer implements Level 2 summarization using Google GenAI SDK.
type GenAISummarizer struct {
	storage Storage
	client  *genai.Client
	model   string
	timeout time.Duration
}

// SummarizerOption allows configuring a GenAISummarizer.
type SummarizerOption func(*GenAISummarizer)

// WithGenAIClient overrides the genai.Client (useful for testing).
func WithGenAIClient(client *genai.Client) SummarizerOption {
	return func(s *GenAISummarizer) {
		s.client = client
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
		apiKey := os.Getenv("GEMINI_API_KEY")
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
// If anything fails or client is unavailable, it gracefully returns raw output without error.
func (s *GenAISummarizer) Summarize(ctx context.Context, cmd string, exitCode int, strategy Strategy, cmdID string, totalBytes int64) (string, error) {
	rawPayload, err := s.storage.ReadPayload(cmdID)
	if err != nil {
		log.Debug().Err(err).Str("cmd_id", cmdID).Msg("failed to read raw payload from storage")
		return "", err
	}

	payloadStr := string(rawPayload)

	if s.client == nil {
		log.Debug().Msg("genai client not available; returning raw payload")
		return payloadStr, nil
	}

	var inputLog string
	var inspectorMode bool

	if totalBytes < TenKBThreshold {
		// Small log (< 10KB): pass full payload directly
		inputLog = payloadStr
		inspectorMode = false
	} else {
		// Large log (>= 10KB): strict inspector extraction without re-reading storage
		inputLog = extractInspectorSnippet(payloadStr)
		inspectorMode = true
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
		log.Debug().Err(err).Str("cmd_id", cmdID).Msg("gemini summarization failed or timed out; falling back to raw payload")
		return payloadStr, nil
	}

	text := resp.Text()
	if strings.TrimSpace(text) == "" {
		log.Debug().Str("cmd_id", cmdID).Msg("gemini returned empty summary; falling back to raw payload")
		return payloadStr, nil
	}

	return text, nil
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
