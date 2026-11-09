package commands

import (
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/agentwrapper/inject"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

// startInjectServer starts the mid-run injection socket when running inside
// the agent sandbox and hooks it into opts. The returned func closes the
// server and is always safe to call. Failure is only logged: a missing
// injection channel must never fail the run, the host just queues instead.
func startInjectServer(opts *types.PromptOptions) (closeFn func()) {
	path := inject.SocketPath()
	if path == "" {
		return func() {}
	}
	srv, err := inject.Listen(path)
	if err != nil {
		log.Warn().Err(err).Str("path", path).Msg("aw: mid-run message injection disabled")
		return func() {}
	}
	opts.OnInjectable = func(fn func(string) error) { srv.SetInject(fn) }
	return func() { _ = srv.Close() }
}
