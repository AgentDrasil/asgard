package fakebash

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/jev"
	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
)

// The jev and Gemini API keys never enter a sandbox. The runner (CompassHost)
// holds them and answers the /compass/* routes of the per-run socket; the
// sandboxed fakebash uses RemoteEvaluator / RemoteSummarizer, which only ever
// send command output data and receive a strategy or a summary.

// maxCompassResponse bounds what fakebash reads back from the runner.
const maxCompassResponse = 1 << 20

// callMargin is added to the model timeout for the socket round trip.
const callMargin = time.Second

// envDuration reads a positive millisecond count from the environment.
func envDuration(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return def
}

// CompassHost makes the jev and Gemini calls on behalf of sandboxes, with keys
// from the host's own configuration. Clients are built on first use. A host
// with no key for a call reports it as disabled.
type CompassHost struct {
	typesafeKey string
	geminiKey   string
	model       string
	jevBaseURL  string

	evalOnce sync.Once
	eval     *JevEvaluator
	sumOnce  sync.Once
	sum      *GenAISummarizer
}

// NewCompassHost returns a CompassHost using the given keys and Gemini model.
// The timeouts come from ASGARD_JEV_TIMEOUT_MS / ASGARD_GEMINI_TIMEOUT_MS of
// the host process.
func NewCompassHost(typesafeKey, geminiKey, model string, opts ...CompassHostOption) *CompassHost {
	h := &CompassHost{typesafeKey: typesafeKey, geminiKey: geminiKey, model: model}
	for _, o := range opts {
		o(h)
	}
	return h
}

// CompassHostOption customizes a CompassHost.
type CompassHostOption func(*CompassHost)

// WithCompassJevBaseURL points the jev client at another base URL (tests).
func WithCompassJevBaseURL(url string) CompassHostOption {
	return func(h *CompassHost) { h.jevBaseURL = url }
}

func (h *CompassHost) evaluator() *JevEvaluator {
	h.evalOnce.Do(func() {
		if h.typesafeKey == "" {
			return
		}
		client, err := jev.NewClient(h.typesafeKey, jev.WithBaseURL(h.jevBaseURL))
		if err != nil {
			log.Debug().Err(err).Msg("failed to initialize jev client; compass classification disabled")
			return
		}
		h.eval = NewJevEvaluator(nil, WithJevClient(client))
	})
	return h.eval
}

func (h *CompassHost) summarizer() *GenAISummarizer {
	h.sumOnce.Do(func() {
		if h.geminiKey == "" {
			return
		}
		s := NewGenAISummarizer(nil, WithGeminiAPIKey(h.geminiKey), WithModelIfSet(h.model))
		if s.client != nil {
			h.sum = s
		}
	})
	return h.sum
}

// WithModelIfSet overrides the Gemini model unless model is empty.
func WithModelIfSet(model string) SummarizerOption {
	return func(s *GenAISummarizer) {
		if model != "" {
			s.model = model
		}
	}
}

// Classify answers a sandbox's classification request; ok is false when the
// host has no jev key.
func (h *CompassHost) Classify(ctx context.Context, req sandboxsock.CompassClassifyRequest) (resp sandboxsock.CompassClassifyResponse) {
	e := h.evaluator()
	if e == nil || e.jevClient == nil {
		return resp
	}
	strategy, usage, _ := e.ClassifyOutput(ctx, req.Command, req.ExitCode, req.TotalBytes, req.OutputSample, "")
	return sandboxsock.CompassClassifyResponse{
		Enabled:  true,
		Strategy: string(strategy),
		Usage:    sandboxsock.CompassUsage{Issued: usage.Issued, Tokens: usage.Tokens},
	}
}

// Summarize answers a sandbox's summarization request; Enabled is false when
// the host has no Gemini key.
func (h *CompassHost) Summarize(ctx context.Context, req sandboxsock.CompassSummarizeRequest) (resp sandboxsock.CompassSummarizeResponse) {
	s := h.summarizer()
	if s == nil {
		return resp
	}
	// The strategy comes from the sandbox and ends up in the prompt: only the
	// known values are passed on. An unknown one means "no summary" (the
	// sandbox keeps the raw output).
	switch st := Strategy(req.Strategy); st {
	case StrategyKeepRaw, StrategyDropOnSuccess, StrategyExtractFailure, StrategySummarize:
	default:
		return sandboxsock.CompassSummarizeResponse{Enabled: true}
	}
	text, usage := s.SummarizeLog(ctx, req.Command, req.ExitCode, Strategy(req.Strategy), req.Log, req.InspectorMode)
	return sandboxsock.CompassSummarizeResponse{
		Enabled: true,
		Summary: text,
		Usage:   sandboxsock.CompassUsage{Issued: usage.Issued, Tokens: usage.Tokens},
	}
}

// postCompass posts in to path on the runner socket and decodes the answer into out.
func postCompass(ctx context.Context, client *sandboxsock.Client, path string, in, out any) error {
	resp, err := client.PostJSON(ctx, path, in)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("compass %s: status %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxCompassResponse)).Decode(out); err != nil {
		return fmt.Errorf("compass %s: decoding response: %w", path, err)
	}
	return nil
}

// RemoteEvaluator is an Evaluator that asks the runner over the per-run socket.
type RemoteEvaluator struct {
	storage Storage
	client  *sandboxsock.Client
	timeout time.Duration
}

// NewRemoteEvaluator builds a RemoteEvaluator for the socket named by
// ASGARD_STATUS_URL. It returns an error when the socket address is unusable.
func NewRemoteEvaluator(storage Storage) (*RemoteEvaluator, error) {
	timeout := envDuration("ASGARD_JEV_TIMEOUT_MS", 2000*time.Millisecond) + callMargin
	client, err := sandboxsock.New(timeout)
	if err != nil {
		return nil, err
	}
	return &RemoteEvaluator{storage: storage, client: client, timeout: timeout}, nil
}

// Classify mirrors JevEvaluator.Classify. Any failure, or a runner without a
// jev key, yields keep_raw.
func (e *RemoteEvaluator) Classify(ctx context.Context, cmd string, exitCode int, cmdID string) (Strategy, ModelUsage, error) {
	head, tail, totalBytes, _, err := e.storage.GetHeadAndTail(cmdID, 1024, 1024)
	if err != nil {
		log.Debug().Err(err).Str("cmd_id", cmdID).Msg("failed to get head and tail from storage; falling back to keep_raw")
		return StrategyKeepRaw, ModelUsage{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	var resp sandboxsock.CompassClassifyResponse
	err = postCompass(ctx, e.client, sandboxsock.PathCompassClassify, sandboxsock.CompassClassifyRequest{
		Command:      cmd,
		ExitCode:     exitCode,
		TotalBytes:   totalBytes,
		OutputSample: BuildOutputSample(head, tail, totalBytes),
	}, &resp)
	if err != nil {
		log.Debug().Err(err).Str("cmd_id", cmdID).Msg("compass classify failed; falling back to keep_raw")
		return StrategyKeepRaw, ModelUsage{}, nil
	}
	usage := ModelUsage{Issued: resp.Usage.Issued, Tokens: resp.Usage.Tokens}
	if !resp.Enabled {
		return StrategyKeepRaw, ModelUsage{}, nil
	}
	switch s := Strategy(resp.Strategy); s {
	case StrategyKeepRaw, StrategyDropOnSuccess, StrategyExtractFailure, StrategySummarize:
		return s, usage, nil
	default:
		return StrategyKeepRaw, usage, nil
	}
}

// RemoteSummarizer is a Summarizer that asks the runner over the per-run socket.
type RemoteSummarizer struct {
	storage Storage
	client  *sandboxsock.Client
	timeout time.Duration
}

// NewRemoteSummarizer builds a RemoteSummarizer for the socket named by
// ASGARD_STATUS_URL.
func NewRemoteSummarizer(storage Storage) (*RemoteSummarizer, error) {
	timeout := envDuration("ASGARD_GEMINI_TIMEOUT_MS", DefaultGeminiTimeout) + callMargin
	client, err := sandboxsock.New(timeout)
	if err != nil {
		return nil, err
	}
	return &RemoteSummarizer{storage: storage, client: client, timeout: timeout}, nil
}

// Summarize mirrors GenAISummarizer.Summarize: on any failure, or a runner
// without a Gemini key, it returns the raw payload.
func (s *RemoteSummarizer) Summarize(ctx context.Context, cmd string, exitCode int, strategy Strategy, cmdID string, totalBytes int64) (string, ModelUsage, error) {
	raw, err := s.storage.ReadPayload(cmdID)
	if err != nil {
		log.Debug().Err(err).Str("cmd_id", cmdID).Msg("failed to read raw payload from storage")
		return "", ModelUsage{}, err
	}
	payload := string(raw)
	inputLog, inspector := SummaryInput(payload, totalBytes)
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var resp sandboxsock.CompassSummarizeResponse
	err = postCompass(ctx, s.client, sandboxsock.PathCompassSummarize, sandboxsock.CompassSummarizeRequest{
		Command:       cmd,
		ExitCode:      exitCode,
		Strategy:      string(strategy),
		InspectorMode: inspector,
		Log:           inputLog,
	}, &resp)
	if err != nil {
		log.Debug().Err(err).Str("cmd_id", cmdID).Msg("compass summarize failed; falling back to raw payload")
		return payload, ModelUsage{}, nil
	}
	usage := ModelUsage{Issued: resp.Usage.Issued, Tokens: resp.Usage.Tokens}
	if !resp.Enabled || resp.Summary == "" {
		return payload, usage, nil
	}
	return resp.Summary, usage, nil
}
