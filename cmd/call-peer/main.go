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
	os.Exit(run(os.Args, os.Stdout))
}

// run executes call-peer (args includes the program name) and returns the
// exit code.
func run(args []string, stdout io.Writer) int {
	logger.SetupLogger("call-peer")
	log.Debug().Interface("args", args).Msg("call-peer: command started")

	if len(args) < 3 {
		log.Error().Msg("Usage: call-peer <agent-id> <message>")
		return 1
	}

	agentID := args[1]
	messageText := args[2]

	// The runner stamps the chat and the calling agent, and the backend
	// resolves the peer within the caller's chat.
	client, err := sandboxsock.New(0)
	if err != nil {
		log.Error().Err(err).Msg("Error preparing call-peer client")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	resp, err := client.PostJSON(ctx, sandboxsock.PathCallPeer, sandboxsock.CallPeerRequest{AgentID: agentID, Prompt: messageText})
	if err != nil {
		log.Error().Err(err).Msg("Error calling peer agent")
		return 1
	}
	defer func() { _ = resp.Body.Close() }()

	var result sandboxsock.CallPeerResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Error().Err(err).Int("http_status", resp.StatusCode).Msg("Error decoding peer agent response")
		return 1
	}

	if resp.StatusCode != http.StatusOK {
		log.Error().Int("http_status", resp.StatusCode).Str("status", result.Status).Str("error", result.Error).Msg("Peer agent returned non-200 status")
		return 1
	}

	if result.Status == "completed" {
		if result.Output != "" {
			_, _ = fmt.Fprintln(stdout, result.Output)
		}
		return 0
	}

	if result.Status == "waiting_human" {
		// Target workflow suspended awaiting human input; exit cleanly without error
		return 0
	}

	log.Error().Str("status", result.Status).Str("error", result.Error).Msg("Peer agent execution failed")
	return 1
}
