package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"uuid"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/logger"
)

type AskUserRequest struct {
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id"`
	Question  string `json:"question"`
	AgentID   string `json:"agent_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
}

type AskUserResponse struct {
	Reply string `json:"reply"`
	Error string `json:"error,omitempty"`
}

type AgentStatusUpdate struct {
	ChatID    string         `json:"chat_id"`
	StepIndex int            `json:"step_index"`
	Source    string         `json:"source"`
	EntryType string         `json:"entry_type"`
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

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
	if os.Getenv("ASGARD_HEADLESS") == "1" {
		fmt.Fprintln(os.Stderr, "ask-user is disabled in headless execution mode")
		os.Exit(1)
	}

	logger.SetupLogger("ask-user")
	log.Info().Interface("args", os.Args).Msg("ask-user: command started")

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, askUserUsage)
		os.Exit(1)
	}

	if first := os.Args[1]; first == "-h" || first == "--help" || first == "help" {
		fmt.Print(askUserUsage)
		os.Exit(0)
	}

	nonFlagArgs := make([]string, 0, len(os.Args)-1)
	for _, arg := range os.Args[1:] {
		if arg == "-h" || arg == "--help" || arg == "help" {
			fmt.Print(askUserUsage)
			os.Exit(0)
		}
		if strings.HasPrefix(arg, "-") {
			log.Error().Str("flag", arg).Msg("unknown flag (ask-user takes a plain question, not flags; see --help)")
			fmt.Fprint(os.Stderr, askUserUsage)
			os.Exit(2)
		}
		nonFlagArgs = append(nonFlagArgs, arg)
	}
	if len(nonFlagArgs) == 0 {
		fmt.Fprint(os.Stderr, askUserUsage)
		os.Exit(1)
	}

	questionText := strings.Join(nonFlagArgs, " ")

	chatID := os.Getenv("ASGARD_CHAT_ID")
	if chatID == "" {
		log.Error().Msg("ASGARD_CHAT_ID environment variable is not set")
		os.Exit(1)
	}

	agentID := os.Getenv("ASGARD_AGENT_ID")
	agentName := os.Getenv("ASGARD_AGENT_NAME")

	internalHost := os.Getenv("ASGARD_INTERNAL_API_HOST")
	if internalHost == "" {
		statusURL := os.Getenv("ASGARD_STATUS_URL")
		if statusURL != "" {
			if u, err := url.Parse(statusURL); err == nil && u.Scheme != "" && u.Host != "" {
				internalHost = u.Scheme + "://" + u.Host
			}
		}
	}
	if internalHost == "" {
		internalHost = "http://127.0.0.1:8081"
	}

	askUserURL := internalHost + "/api/ask-user"
	statusURL := internalHost + "/agent-status"

	msgID := fmt.Sprintf("ask-%s", uuid.NewV7().String())
	sendAgentStatus(statusURL, chatID, msgID, questionText, agentID, agentName)

	// Send AskUser request and long-poll host until user replies
	reqBody, err := json.Marshal(AskUserRequest{
		ChatID:    chatID,
		MessageID: msgID,
		Question:  questionText,
		AgentID:   agentID,
		AgentName: agentName,
	})
	if err != nil {
		log.Error().Err(err).Msg("Error encoding ask-user request")
		os.Exit(1)
	}

	log.Info().Str("question", questionText).Str("host", internalHost).Msg("ask-user: question sent, waiting for user reply...")

	client := &http.Client{Timeout: 3 * time.Hour}
	resp, err := client.Post(askUserURL, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		log.Error().Err(err).Msg("Error connecting to ask-user endpoint")
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Error().Err(err).Msg("Error reading response body from ask-user endpoint")
		os.Exit(1)
	}

	if resp.StatusCode != http.StatusOK {
		log.Error().Int("status", resp.StatusCode).Str("body", string(respBody)).Msg("Error response from ask-user endpoint")
		os.Exit(1)
	}

	var askResp AskUserResponse
	if err := json.Unmarshal(respBody, &askResp); err != nil {
		log.Error().Err(err).Str("body", string(respBody)).Msg("Error decoding response from ask-user endpoint")
		os.Exit(1)
	}

	if askResp.Error != "" {
		log.Error().Str("error", askResp.Error).Msg("Ask-user request error")
		os.Exit(1)
	}

	log.Info().Str("reply", askResp.Reply).Msg("ask-user: user reply received, continuing execution")
	fmt.Print(askResp.Reply)
	fmt.Println()
}

func sendAgentStatus(statusURL string, chatID string, msgID string, questionText string, agentID string, agentName string) {
	update := AgentStatusUpdate{
		ChatID:    chatID,
		StepIndex: 0,
		Source:    "ask-user",
		EntryType: "ask_user",
		Content:   questionText,
		Metadata: map[string]any{
			"message_id": msgID,
			"agent_id":   agentID,
			"agent_name": agentName,
		},
	}
	data, err := json.Marshal(update)
	if err != nil {
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(statusURL, "application/json", bytes.NewBuffer(data))
	if err == nil {
		_ = resp.Body.Close()
	}
}
