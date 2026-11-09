package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/logger"
	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
)

func main() {
	os.Exit(run(os.Stdout))
}

// run executes asgard-merge and returns the exit code: 0 when the work is in
// the main checkout (or there was nothing to merge), 1 when the agent has to
// act first. The message on stdout says what to do.
func run(stdout io.Writer) int {
	logger.SetupLogger("asgard-merge")
	log.Debug().Interface("args", os.Args).Msg("asgard-merge: command started")

	// The runner stamps the chat; the worktree is the one of this run.
	client, err := sandboxsock.New(0)
	if err != nil {
		log.Error().Err(err).Msg("Error preparing asgard-merge client")
		return 1
	}

	// The merge may wait a long time for the main checkout to become free.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	resp, err := client.PostJSON(ctx, sandboxsock.PathMerge, struct{}{})
	if err != nil {
		log.Error().Err(err).Msg("Error calling merge endpoint")
		return 1
	}
	defer func() { _ = resp.Body.Close() }()

	var result sandboxsock.MergeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Error().Err(err).Int("http_status", resp.StatusCode).Msg("Error decoding merge response")
		return 1
	}
	if result.Message != "" {
		_, _ = fmt.Fprintln(stdout, result.Message)
	}
	if resp.StatusCode != http.StatusOK || !result.OK {
		return 1
	}
	return 0
}
