package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/logger"
)

func main() {
	logger.SetupLogger("call-peer")
	log.Debug().Interface("args", os.Args).Msg("call-peer: command started")

	if len(os.Args) < 3 {
		log.Error().Msg("Usage: call-peer <agent-id> <message>")
		os.Exit(1)
	}

	agentID := os.Args[1]
	messageText := os.Args[2]

	chatID := os.Getenv("ASGARD_CHAT_ID")
	if chatID == "" {
		log.Error().Msg("ASGARD_CHAT_ID environment variable is not set")
		os.Exit(1)
	}

	// Reach the API over the internal loopback listener: the public listener is
	// guarded by the auth middleware, which agent-to-agent traffic must bypass.
	host := os.Getenv("ASGARD_INTERNAL_API_HOST")
	if host == "" {
		host = "http://127.0.0.1:8081"
	}
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "http://" + host
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	reqPayload := map[string]any{
		"prompt": messageText,
		"chatId": chatID,
		"wait":   true,
		"metadata": map[string]any{
			"internal":          true,
			"source":            "call-peer",
			"caller_agent_id":   os.Getenv("ASGARD_AGENT_ID"),
			"caller_agent_name": os.Getenv("ASGARD_AGENT_NAME"),
		},
	}

	reqBody, err := json.Marshal(reqPayload)
	if err != nil {
		log.Error().Err(err).Msg("Error encoding call-peer request")
		os.Exit(1)
	}

	url := fmt.Sprintf("%s/api/agents/%s/message?wait=true", host, agentID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		log.Error().Err(err).Msg("Error creating request for peer agent")
		os.Exit(1)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		log.Error().Err(err).Msg("Error calling peer agent")
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Status string `json:"status"`
		Output string `json:"output"`
		Error  string `json:"error,omitempty"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Error().Err(err).Int("http_status", resp.StatusCode).Msg("Error decoding peer agent response")
		os.Exit(1)
	}

	if resp.StatusCode != http.StatusOK {
		log.Error().Int("http_status", resp.StatusCode).Str("status", result.Status).Str("error", result.Error).Msg("Peer agent returned non-200 status")
		os.Exit(1)
	}

	if result.Status == "completed" {
		if result.Output != "" {
			fmt.Println(result.Output)
		}
		return
	}

	if result.Status == "waiting_human" {
		// Target workflow suspended awaiting human input; exit cleanly without error
		return
	}

	log.Error().Str("status", result.Status).Str("error", result.Error).Msg("Peer agent execution failed")
	os.Exit(1)
}
