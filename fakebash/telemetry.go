package fakebash

import (
	"context"

	"github.com/AgentDrasil/asgard/pkg/metrics"
)

// reportTelemetry translates a pipeline outcome into metric events and sends
// them to the backend. It is best-effort: telemetry never affects the exit
// status of the command being reported.
func reportTelemetry(ctx context.Context, out Outcome) {
	var events []metrics.Event
	if out.Jev.Issued {
		events = append(events, metrics.Event{Kind: metrics.KindJev, Tokens: out.Jev.Tokens})
	}
	if out.Compass.Issued {
		events = append(events, metrics.Event{Kind: metrics.KindCompass, Tokens: out.Compass.Tokens})
	}
	if out.Truncated {
		events = append(events, metrics.Event{Kind: metrics.KindTruncate})
	}
	metrics.Report(ctx, events...)
}
