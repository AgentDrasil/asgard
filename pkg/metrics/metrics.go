// Package metrics reports command-output compression telemetry from the
// sandbox tools that produce it (fakebash, show-output) back to the Asgard
// backend through the runner, which aggregates it for the WebUI.
package metrics

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
)

// EndpointPath is the backend route that serves the aggregated tally to the
// WebUI.
const EndpointPath = "/api/compression-metrics"

// Kind identifies which part of the compression pipeline an event describes.
type Kind string

const (
	// KindJev counts a Level 1 Jev System One classification call that returned
	// a response.
	KindJev Kind = "jev"
	// KindCompass counts a Level 2 Gemini "compass" summarization call that
	// returned a response.
	KindCompass Kind = "compass"
	// KindTruncate counts a command whose output exceeded the overflow limit.
	KindTruncate Kind = "truncate"
	// KindCompressed counts a command whose output reached the agent without its
	// full raw contents, whether by summarization, drop-on-success, or truncation.
	// It is the denominator for how often the agent pulled raw output back.
	KindCompressed Kind = "compressed"
	// KindShowOutput counts an invocation of show-output that dumped raw output.
	KindShowOutput Kind = "show_output"
)

// Event is a single compression-pipeline observation. Tokens is set for the
// model-backed kinds; Bytes is set for KindShowOutput.
type Event struct {
	Kind   Kind  `json:"kind"`
	Tokens int64 `json:"tokens,omitempty"`
	Bytes  int64 `json:"bytes,omitempty"`
}

const reportTimeout = time.Second

// Report sends events to the runner's callback socket, which forwards them to
// the backend. It is best-effort by design: telemetry must never change the
// exit status of the command that produced it, so failures are swallowed and
// only logged at debug level. An empty event slice is a no-op.
func Report(ctx context.Context, events ...Event) {
	if len(events) == 0 {
		return
	}

	client, err := sandboxsock.New(reportTimeout)
	if err != nil {
		log.Debug().Err(err).Msg("metrics: no callback socket")
		return
	}

	reqCtx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()

	resp, err := client.PostJSON(reqCtx, sandboxsock.PathMetrics, events)
	if err != nil {
		log.Debug().Err(err).Msg("metrics: report failed")
		return
	}
	_ = resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Int("status", resp.StatusCode).Msg("metrics: report rejected by runner")
	}
}
