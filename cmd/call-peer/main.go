package main

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"os"
	"os/signal"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/goccy/go-yaml"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/lib/logger"
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

	host := os.Getenv("ASGARD_API_HOST")
	if host == "" {
		configPath := os.Getenv("CONFIG_PATH")
		if configPath == "" {
			if _, err := os.Stat("/home/user/config.yaml"); err == nil {
				configPath = "/home/user/config.yaml"
			} else {
				configPath = "config.yaml"
			}
		}

		port := 8080
		if data, err := os.ReadFile(configPath); err == nil {
			var cfg struct {
				Port int `yaml:"port"`
			}
			if err := yaml.Unmarshal(data, &cfg); err == nil && cfg.Port > 0 {
				port = cfg.Port
			}
		}
		host = fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "http://" + host
	}

	cardURL := fmt.Sprintf("%s/agents/%s/.well-known/agent-card.json?internal=true", host, agentID)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, cardURL, nil)
	if err != nil {
		log.Error().Err(err).Msg("Error creating request for agent card")
		os.Exit(1)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		log.Error().Err(err).Msg("Error fetching agent card")
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		log.Error().Int("status", resp.StatusCode).Msg("Failed to fetch agent card")
		os.Exit(1)
	}

	var card a2a.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		log.Error().Err(err).Msg("Error decoding agent card")
		os.Exit(1)
	}

	httpClient := &http.Client{Timeout: 0}
	client, err := a2aclient.NewFromCard(ctx, &card,
		a2aclient.WithRESTTransport(httpClient),
		a2aclient.WithJSONRPCTransport(httpClient),
	)
	if err != nil {
		log.Error().Err(err).Msg("Error creating A2A client")
		os.Exit(1)
	}

	reqMsg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(messageText))
	reqMsg.ContextID = chatID
	reqMsg.Metadata = map[string]any{
		"internal":          true,
		"source":            "call-peer",
		"caller_agent_id":   os.Getenv("ASGARD_AGENT_ID"),
		"caller_agent_name": os.Getenv("ASGARD_AGENT_NAME"),
	}

	req := &a2a.SendMessageRequest{
		Message: reqMsg,
	}

	// Use streaming to receive intermediate status updates in real-time.
	events := client.SendStreamingMessage(ctx, req)
	if err := drainEvents(events); err != nil {
		log.Error().Err(err).Msg("Error from agent stream")
		os.Exit(1)
	}
}

// drainEvents iterates over the SSE event stream, printing intermediate updates
// to stderr and the final response to stdout.
func drainEvents(events iter.Seq2[a2a.Event, error]) error {
	for evt, err := range events {
		if err != nil {
			return err
		}
		switch e := evt.(type) {
		case *a2a.TaskStatusUpdateEvent:
			if e.Status.State == a2a.TaskStateCompleted && e.Status.Message != nil {
				// Final response — print to stdout.
				fmt.Print(extractText(e.Status.Message))
				fmt.Println()
			}
		}
	}
	return nil
}

// extractText returns the concatenated text from all TextPart parts of a message.
func extractText(msg *a2a.Message) string {
	if msg == nil {
		return ""
	}
	var out string
	for _, part := range msg.Parts {
		if part != nil && part.Text() != "" {
			out += part.Text()
		}
	}
	return out
}
