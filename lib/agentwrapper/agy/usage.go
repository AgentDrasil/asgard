// Package agy provides programmatic interaction helpers for the agy CLI tool.
package agy

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/agentwrapper"

	"github.com/AgentDrasil/asgard/lib/term"
)

const (
	termCols uint16 = 220
	termRows uint16 = 50
)

// Usage launches agy in a headless terminal, sends the "/usage" command,
// parses the output, exits cleanly, and returns the captured quota entries.
//
// The sequence performed is:
//  1. Open a headless PTY-backed terminal (220×50).
//  2. Launch `agy`.
//  3. Poll the statusline JSON every 200 ms until the statusbar last line's first
//     token is "idle" (or StartupDelay elapses).
//  4. Send "/usage\r" and wait for the response to render.
//  5. Press Esc, then Ctrl-D twice to exit.
//  6. Parse and return the scrollback as []agents.ModelUsage.
func Usage(ctx context.Context, opts agentwrapper.UsageOptions) ([]agentwrapper.ModelUsage, error) {
	t := term.NewTerm(termCols, termRows)
	defer t.Close()

	argv := []string{"agy"}

	awSessionID := uuid.NewString()
	done, err := t.RunCommandInDir(context.Background(), argv, opts.Dir, []string{"AW_SESSION_ID=" + awSessionID})
	if err != nil {
		return nil, fmt.Errorf("launching agy/usage: %w", err)
	}

	handleErr := func(err error) error {
		if ctx.Err() != nil {
			GratefulShutdown(t, done)
			return ctx.Err()
		}
		return err
	}

	// Poll until the statusbar last line reports "idle", up to startupDelay.
	log.Debug().Msg("agy/usage: waiting for state=idle")
	timedOut, err := pollUntilIdle(ctx, awSessionID, done, opts.StartupDelayOrDefault())
	if err != nil {
		return nil, handleErr(err)
	}
	if timedOut {
		log.Debug().Msg("agy/usage: startup idle timed out — proceeding anyway")
	} else {
		log.Debug().Msg("agy/usage: state=idle")
	}

	// Send the /usage command followed by Enter.
	if err := t.SendString("/usage"); err != nil {
		return nil, handleErr(fmt.Errorf("sending /usage: %w", err))
	}
	if err := t.SendKeys(term.KeyEnter); err != nil {
		return nil, handleErr(fmt.Errorf("sending Enter: %w", err))
	}

	// Wait for the response to render.
	select {
	case <-time.After(opts.ResponseDelayOrDefault()):
	case <-ctx.Done():
		return nil, handleErr(ctx.Err())
	case err := <-done:
		return nil, fmt.Errorf("agy exited unexpectedly waiting for /usage response: %w", err)
	}

	lines := t.Scrollback()
	log.Debug().Msg("agy/usage: got usage")

	// Exit: Esc, then Ctrl-D twice.
	CleanExit(t, done)

	return parseUsage(lines, time.Now())
}
