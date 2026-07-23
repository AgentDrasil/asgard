package agy

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/term"
)

// CleanExit performs the standard clean exit sequence for the agy process.
// The exit step is sending "/exit" followed by Enter.
func CleanExit(t *term.Term, done <-chan error) {
	log.Debug().Msg("agy: executing clean exit (/exit + Enter)")
	_ = t.SendString("/exit\n")

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
