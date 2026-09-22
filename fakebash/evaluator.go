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

// ModelUsage records whether a model was consulted for a command and how many
// tokens that consultation consumed. Issued is false when no call was made at
// all (no client configured, or the call failed before returning a response).
type ModelUsage struct {
	Issued bool
	Tokens int64
}

// Evaluator evaluates command output metadata and determines a compression strategy.
type Evaluator interface {
	Classify(ctx context.Context, cmd string, exitCode int, cmdID string) (Strategy, ModelUsage, error)
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

// Classify evaluates the command result and returns the selected Strategy along
// with the token cost of the call that produced it.
// If TYPESAFE_API_KEY is missing or the request fails/times out, it gracefully falls back to StrategyKeepRaw.
func (e *JevEvaluator) Classify(ctx context.Context, cmd string, exitCode int, cmdID string) (Strategy, ModelUsage, error) {
	if e.jevClient == nil {
		log.Debug().Msg("no jev client or TYPESAFE_API_KEY configured; falling back to keep_raw")
		return StrategyKeepRaw, ModelUsage{}, nil
	}

	head, tail, totalBytes, _, err := e.storage.GetHeadAndTail(cmdID, 1024, 1024)
	if err != nil {
		log.Debug().Err(err).Str("cmd_id", cmdID).Msg("failed to get head and tail from storage; falling back to keep_raw")
		return StrategyKeepRaw, ModelUsage{}, nil
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
		"Select how to handle the captured output of this finished command. Decide by what the caller needs next, not by output size.",
		map[string]any{
			string(StrategyKeepRaw):        "Exit code 0 and the output is itself the deliverable: data consumed verbatim downstream (file listings, diffs, query results, tables, structured output) or an interactive prompt. Return it unchanged.",
			string(StrategyDropOnSuccess):  "Exit code 0 and the output carries no information worth returning: progress or status chatter, build/install/download noise, acknowledgements, or a command that produced nothing notable. The success status alone is sufficient.",
			string(StrategyExtractFailure): "Exit code is non-zero: compilation or linker errors, failed tests, panics, or exceptions where the exact failure cause must be pinpointed.",
			string(StrategySummarize):      "Exit code 0 with genuinely long output (hundreds of lines) whose overall gist is useful but whose full detail is not; produce a concise high-level summary.",
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
		return StrategyKeepRaw, ModelUsage{}, nil
	}

	usage := ModelUsage{Issued: true, Tokens: int64(resp.Usage.InputTokens + resp.Usage.OutputTokens)}

	ans, ok := resp.Answers["compression_strategy"]
	if !ok || ans.Choice == "" {
		log.Debug().Str("cmd_id", cmdID).Msg("jev returned no answer for compression_strategy; falling back to keep_raw")
		return StrategyKeepRaw, usage, nil
	}

	switch Strategy(ans.Choice) {
	case StrategyKeepRaw, StrategyDropOnSuccess, StrategyExtractFailure, StrategySummarize:
		return Strategy(ans.Choice), usage, nil
	default:
		log.Debug().Str("choice", ans.Choice).Msg("unrecognized choice from jev; falling back to keep_raw")
		return StrategyKeepRaw, usage, nil
	}
}
