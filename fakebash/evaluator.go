package fakebash

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/jev"
)

// Strategy represents the compression strategy determined for command output.
type Strategy string

const (
	StrategyKeepRaw        Strategy = "keep_raw"
	StrategyDropOnSuccess  Strategy = "drop_on_success"
	StrategyExtractFailure Strategy = "extract_failure"
	StrategySummarize      Strategy = "summarize"
)

// Evaluator evaluates command output metadata and determines a compression strategy.
type Evaluator interface {
	Classify(ctx context.Context, cmd string, exitCode int, cmdID string) (Strategy, error)
}

// JevEvaluator implements Level 1 classification using TypeSafe Jev System One evaluation.
type JevEvaluator struct {
	storage   Storage
	jevClient *jev.Client
	timeout   time.Duration
}

// JevEvaluatorOption configures a JevEvaluator.
type JevEvaluatorOption func(*JevEvaluator)

// WithJevClient overrides the Jev client (useful in tests).
func WithJevClient(client *jev.Client) JevEvaluatorOption {
	return func(e *JevEvaluator) {
		e.jevClient = client
	}
}

// WithTimeout overrides the classification timeout duration.
func WithTimeout(d time.Duration) JevEvaluatorOption {
	return func(e *JevEvaluator) {
		e.timeout = d
	}
}

// NewJevEvaluator creates a new JevEvaluator with default timeout of 2 seconds (or ASGARD_JEV_TIMEOUT_MS env).
func NewJevEvaluator(storage Storage, opts ...JevEvaluatorOption) *JevEvaluator {
	timeout := 2000 * time.Millisecond
	if envTimeout := os.Getenv("ASGARD_JEV_TIMEOUT_MS"); envTimeout != "" {
		if ms, err := strconv.Atoi(envTimeout); err == nil && ms > 0 {
			timeout = time.Duration(ms) * time.Millisecond
		}
	}

	e := &JevEvaluator{
		storage: storage,
		timeout: timeout,
	}

	for _, opt := range opts {
		opt(e)
	}

	if e.jevClient == nil {
		apiKey := os.Getenv("TYPESAFE_API_KEY")
		if apiKey != "" {
			client, err := jev.NewClient(apiKey)
			if err == nil {
				e.jevClient = client
			} else {
				log.Debug().Err(err).Msg("failed to initialize jev client; will fallback to keep_raw")
			}
		}
	}

	return e
}

// Classify evaluates the command result and returns the selected Strategy.
// If TYPESAFE_API_KEY is missing or the request fails/times out, it gracefully falls back to StrategyKeepRaw.
func (e *JevEvaluator) Classify(ctx context.Context, cmd string, exitCode int, cmdID string) (Strategy, error) {
	if e.jevClient == nil {
		log.Debug().Msg("no jev client or TYPESAFE_API_KEY configured; falling back to keep_raw")
		return StrategyKeepRaw, nil
	}

	head, tail, totalBytes, _, err := e.storage.GetHeadAndTail(cmdID, 1024, 1024)
	if err != nil {
		log.Debug().Err(err).Str("cmd_id", cmdID).Msg("failed to get head and tail from storage; falling back to keep_raw")
		return StrategyKeepRaw, nil
	}

	var outputSample string
	if totalBytes <= 2048 {
		outputSample = string(head)
	} else {
		outputSample = fmt.Sprintf("%s\n... [truncated %d bytes] ...\n%s", string(head), totalBytes-int64(len(head))-int64(len(tail)), string(tail))
	}

	state := map[string]any{
		"command":       cmd,
		"exit_code":     exitCode,
		"output_length": totalBytes,
		"output_sample": outputSample,
	}

	question := jev.NewChoice(
		"Select the optimal output handling strategy for this command execution result.",
		map[string]any{
			string(StrategyKeepRaw):        "Output is short, critical interactive prompt, or should be preserved completely as-is without modification.",
			string(StrategyDropOnSuccess):  "Command succeeded with exit code 0 and generated verbose build/install logs, progress bars, or noisy output where success status is sufficient.",
			string(StrategyExtractFailure): "Command failed (non-zero exit code) with compilation errors, test failures, panic traces, or exception messages where pinpointing the exact failure cause is critical.",
			string(StrategySummarize):      "Command succeeded or had informational output that is excessively long and needs a high-level concise summary.",
		},
	)

	evalCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	resp, err := e.jevClient.Evaluate(evalCtx, jev.EvaluateRequest{
		State: state,
		Questions: map[string]jev.Question{
			"compression_strategy": question,
		},
	})
	if err != nil {
		log.Debug().Err(err).Str("cmd_id", cmdID).Msg("jev evaluation timed out or failed; falling back to keep_raw")
		return StrategyKeepRaw, nil
	}

	ans, ok := resp.Answers["compression_strategy"]
	if !ok || ans.Choice == "" {
		log.Debug().Str("cmd_id", cmdID).Msg("jev returned no answer for compression_strategy; falling back to keep_raw")
		return StrategyKeepRaw, nil
	}

	switch Strategy(ans.Choice) {
	case StrategyKeepRaw, StrategyDropOnSuccess, StrategyExtractFailure, StrategySummarize:
		return Strategy(ans.Choice), nil
	default:
		log.Debug().Str("choice", ans.Choice).Msg("unrecognized choice from jev; falling back to keep_raw")
		return StrategyKeepRaw, nil
	}
}
