package agy

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/term"
)

// GratefulShutdown performs the graceful shutdown sequence for the agy process when interrupted.
// The shutdown step is ctrl+c, then ctrl+d twice.
func GratefulShutdown(t *term.Term, done <-chan error) {
	log.Debug().Msg("agy: executing grateful shutdown (Ctrl+C, Ctrl+D, Ctrl+D)")
	_ = t.SendKeys(term.KeyCtrlC)
	time.Sleep(200 * time.Millisecond)
	_ = t.SendKeys(term.KeyCtrlD)
	time.Sleep(200 * time.Millisecond)
	_ = t.SendKeys(term.KeyCtrlD)

	// Wait for the process to exit with a 5s timeout
	exitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-done:
		log.Debug().Msg("agy: process exited after grateful shutdown")
	case <-exitCtx.Done():
		log.Warn().Msg("agy: process did not exit after grateful shutdown")
	}
}

// CleanExit performs the standard clean exit sequence for the agy process.
// The exit step is Esc, then Ctrl+D twice.
func CleanExit(t *term.Term, done <-chan error) {
	log.Debug().Msg("agy: executing clean exit (Esc, Ctrl+D, Ctrl+D)")
	_ = t.SendKeys(term.KeyEsc)
	time.Sleep(200 * time.Millisecond)
	_ = t.SendKeys(term.KeyCtrlD)
	time.Sleep(200 * time.Millisecond)
	_ = t.SendKeys(term.KeyCtrlD)

	// Wait for the process to exit with a 5s timeout
	exitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-done:
		log.Debug().Msg("agy: process exited after clean exit")
	case <-exitCtx.Done():
		log.Warn().Msg("agy: process did not exit after clean exit")
	}
}
