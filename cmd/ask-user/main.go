package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/logger"
	"github.com/AgentDrasil/asgard/pkg/paths/sandboxsock"
)

const askUserUsage = `Usage: ask-user <question>

Send a question to the human user and block until they reply.
The user's reply is printed to stdout, so capture it with command substitution, e.g.:

  ANSWER=$(ask-user "Which database should I use?")

Notes:
  - The call long-polls and may wait a long time until the user replies.
  - Ask ONE clear, specific question per call; do not use this for status updates.
  - -h, --help, or help prints this help and exits without asking the user.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes ask-user and returns the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if os.Getenv("ASGARD_HEADLESS") == "1" {
		_, _ = fmt.Fprintln(stderr, "ask-user is disabled in headless execution mode")
		return 1
	}

	logger.SetupLogger("ask-user")
	log.Info().Interface("args", args).Msg("ask-user: command started")

	if len(args) < 1 {
		_, _ = fmt.Fprint(stderr, askUserUsage)
		return 1
	}

	if first := args[0]; first == "-h" || first == "--help" || first == "help" {
		_, _ = fmt.Fprint(stdout, askUserUsage)
		return 0
	}

	nonFlagArgs := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "-h" || arg == "--help" || arg == "help" {
			_, _ = fmt.Fprint(stdout, askUserUsage)
			return 0
		}
		if strings.HasPrefix(arg, "-") {
			log.Error().Str("flag", arg).Msg("unknown flag (ask-user takes a plain question, not flags; see --help)")
			_, _ = fmt.Fprint(stderr, askUserUsage)
			return 2
		}
		nonFlagArgs = append(nonFlagArgs, arg)
	}
	if len(nonFlagArgs) == 0 {
		_, _ = fmt.Fprint(stderr, askUserUsage)
		return 1
	}

	questionText := strings.Join(nonFlagArgs, " ")

	// The runner stamps the chat and run identity of the call; only the
	// display fields come from the environment.
	// Send the question to the runner's socket and long-poll until the user
	// replies. The backend is the single authoritative writer of the ask_user
	// chat message.
	client, err := sandboxsock.New(0)
	if err != nil {
		log.Error().Err(err).Msg("Error preparing ask-user client")
		return 1
	}

	log.Info().Str("question", questionText).Msg("ask-user: question sent, waiting for user reply...")

	resp, err := client.PostJSON(context.Background(), sandboxsock.PathAskUser, sandboxsock.AskUserRequest{
		Question:  questionText,
		AgentName: os.Getenv("ASGARD_AGENT_NAME"),
		Model:     os.Getenv("ASGARD_MODEL"),
	})
	if err != nil {
		log.Error().Err(err).Msg("Error connecting to ask-user endpoint")
		return 1
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error().Err(err).Msg("Error reading response body from ask-user endpoint")
		return 1
	}

	if resp.StatusCode != http.StatusOK {
		log.Error().Int("status", resp.StatusCode).Str("body", string(respBody)).Msg("Error response from ask-user endpoint")
		return 1
	}

	var askResp sandboxsock.AskUserResponse
	if err := json.Unmarshal(respBody, &askResp); err != nil {
		log.Error().Err(err).Str("body", string(respBody)).Msg("Error decoding response from ask-user endpoint")
		return 1
	}

	if askResp.Error != "" {
		log.Error().Str("error", askResp.Error).Msg("Ask-user request error")
		return 1
	}

	log.Info().Str("reply", askResp.Reply).Msg("ask-user: user reply received, continuing execution")
	_, _ = fmt.Fprint(stdout, askResp.Reply)
	_, _ = fmt.Fprintln(stdout)
	return 0
}
