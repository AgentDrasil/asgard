package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/logger"
)

func main() {
	logger.SetupLogger("find-peer")
	log.Debug().Interface("args", os.Args).Msg("find-peer: command started")

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

	resp, err := http.Get(host + "/team?chat_id=" + url.QueryEscape(chatID))
	if err != nil {
		log.Error().Err(err).Msg("Error calling /team API")
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Error().Int("status", resp.StatusCode).Str("body", string(body)).Msg("Error response from /team API")
		os.Exit(1)
	}

	var peers []string
	if err := json.NewDecoder(resp.Body).Decode(&peers); err != nil {
		log.Error().Err(err).Msg("Error decoding JSON from /team API")
		os.Exit(1)
	}
	if peers == nil {
		peers = []string{}
	}

	formatted, err := json.MarshalIndent(peers, "", "  ")
	if err != nil {
		log.Error().Err(err).Msg("Error formatting JSON response")
		os.Exit(1)
	}

	fmt.Println(string(formatted))
}
