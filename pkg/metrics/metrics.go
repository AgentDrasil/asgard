// Package metrics reports command-output compression telemetry from the
// sandbox tools that produce it (fakebash, show-output) back to the Asgard
// backend, which aggregates it for the WebUI.
package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog/log"
)

// EndpointPath is the backend route that ingests metric events. Producer and
// consumer share this constant so the wire contract cannot drift.
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

const (
	defaultInternalHost = "http://127.0.0.1:8081"
	reportTimeout       = time.Second
)

// client is package-level so consecutive reports within one process reuse the
// connection instead of re-dialing.
var client = &http.Client{Timeout: reportTimeout}

// Report sends events to the backend's internal metrics endpoint. It is
// best-effort by design: telemetry must never change the exit status of the
// command that produced it, so failures are swallowed and only logged at debug
// level. An empty event slice is a no-op.
func Report(ctx context.Context, events ...Event) {
	if len(events) == 0 {
		return
	}

	body, err := json.Marshal(events)
	if err != nil {
		log.Debug().Err(err).Msg("metrics: failed to encode events")
		return
	}

	host := os.Getenv("ASGARD_INTERNAL_API_HOST")
	if host == "" {
		host = defaultInternalHost
	}

	reqCtx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, host+EndpointPath, bytes.NewReader(body))
	if err != nil {
		log.Debug().Err(err).Msg("metrics: failed to build request")
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		log.Debug().Err(err).Msg("metrics: report failed")
		return
	}
	_ = resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debug().Int("status", resp.StatusCode).Msg("metrics: report rejected by backend")
	}
}
