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

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/logger"
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

func main() {
	logger.SetupLogger("ask-user")
	log.Info().Interface("args", os.Args).Msg("ask-user: command started")

	if len(os.Args) < 2 {
		log.Error().Msg("Usage: ask-user <question>")
		os.Exit(1)
	}

	questionText := strings.Join(os.Args[1:], " ")

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

	msgID := fmt.Sprintf("ask-%s", uuid.NewString())
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
