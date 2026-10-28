package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/logger"
	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
)

func main() {
	os.Exit(run(os.Stdout))
}

// run executes find-peer and returns the exit code.
func run(stdout io.Writer) int {
	logger.SetupLogger("find-peer")
	log.Debug().Interface("args", os.Args).Msg("find-peer: command started")

	// The runner stamps the chat; the backend answers for the caller's chat.
	client, err := sandboxsock.New(0)
	if err != nil {
		log.Error().Err(err).Msg("Error preparing find-peer client")
		return 1
	}

	resp, err := client.Get(context.Background(), sandboxsock.PathFindPeer)
	if err != nil {
		log.Error().Err(err).Msg("Error calling find-peer endpoint")
		return 1
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Error().Int("status", resp.StatusCode).Str("body", string(body)).Msg("Error response from find-peer endpoint")
		return 1
	}

	var peers []string
	if err := json.NewDecoder(resp.Body).Decode(&peers); err != nil {
		log.Error().Err(err).Msg("Error decoding JSON from find-peer endpoint")
		return 1
	}
	if peers == nil {
		peers = []string{}
	}

	formatted, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		log.Error().Err(err).Msg("Error formatting JSON response")
		return 1
	}

	_, _ = fmt.Fprintln(stdout, string(formatted))
	return 0
}
