// Command asgard-runner connects to the Asgard backend and executes the runs
// it dispatches.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/backend/lib/bwrap"
	"github.com/AgentDrasil/asgard/pkg/logger"
	"github.com/AgentDrasil/asgard/pkg/version"
	"github.com/AgentDrasil/asgard/runner/lib/config"
	"github.com/AgentDrasil/asgard/runner/lib/conn"
	"github.com/AgentDrasil/asgard/runner/lib/exec"
	"github.com/AgentDrasil/asgard/runner/lib/sandbox"
)

func main() {
	logger.SetupLogger("asgard-runner")
	if err := run(); err != nil {
		log.Fatal().Err(err).Msg("asgard-runner stopped with error")
	}
}

func run() error {
	boot, err := config.ParseBootstrap(os.Args[1:], os.Getenv)
	if err != nil {
		return err
	}
	nodeID, err := boot.NodeID()
	if err != nil {
		return err
	}

	// Models/usage queries invoke vendor CLIs non-interactively, which can
	// fire their hooks; run them inside the agent sandbox, not on the host.
	agentwrapper.SetQueryRunner(bwrap.RunQuery)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Probing the CLIs execs them, so do it once for Register and etcd parsing.
	clis := agentwrapper.GetSupportedCLIsAndModels()
	remote, err := config.NewRemote(ctx, nodeID, clis)
	if err != nil {
		return err
	}
	if agents, err := remote.Agents(); err != nil {
		log.Warn().Err(err).Msg("agent definitions not usable yet")
	} else {
		log.Info().Int("agents", len(agents)).Msg("agent definitions loaded")
	}

	log.Info().Str("node_id", nodeID).Str("backend", boot.Backend).Str("version", version.Get()).Msg("asgard-runner starting")
	sb := sandbox.Setup(remote, sandbox.Options{})
	// Stop the etcd watchers first so no change arrives while the sandbox
	// setup shuts down (Close also ignores late changes).
	defer func() {
		remote.Close()
		sb.Close()
	}()
	executor := exec.New(exec.Options{Agents: remote, Conf: sb.Conf, Ready: sb.Ready})
	client := conn.New(conn.Options{
		Executor:      executor,
		Addr:          boot.Backend,
		NodeID:        nodeID,
		TLSConfig:     boot.TLSConfig,
		SupportedCLIs: func() map[string][]string { return clis },
	})
	if err := client.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
