package main

import (
	"context"
	"fmt"
	"iter"
	"os"
	"os/signal"

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

	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		if _, err := os.Stat("/home/user/config.yaml"); err == nil {
			configPath = "/home/user/config.yaml"
		} else {
			configPath = "config.yaml"
		}
	}

	host := "http://127.0.0.1:8080"
	if data, err := os.ReadFile(configPath); err == nil {
		var cfg struct {
			Host string `yaml:"host"`
		}
		if err := yaml.Unmarshal(data, &cfg); err == nil && cfg.Host != "" {
			host = cfg.Host
		}
	}

	targetURL := fmt.Sprintf("%s/agents/%s", host, agentID)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	client, err := a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{
		a2a.NewAgentInterface(targetURL, a2a.TransportProtocolHTTPJSON),
	})
	if err != nil {
		log.Error().Err(err).Msg("Error creating A2A client")
		os.Exit(1)
	}

	reqMsg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(messageText))
	reqMsg.ContextID = chatID

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
			if e.Status.Message != nil {
				text := extractText(e.Status.Message)
				switch e.Status.State {
				case a2a.TaskStateCompleted:
					// Final response — print to stdout.
					fmt.Print(text)
					fmt.Println()
				case a2a.TaskStateWorking:
					// Intermediate update — print entry type and short preview to stderr.
					entryType := "update"
					if e.Status.Message.Metadata != nil {
						if et, ok := e.Status.Message.Metadata["entry_type"].(string); ok && et != "" {
							entryType = et
						}
					}
					preview := text
					if len(preview) > 120 {
						preview = preview[:120] + "…"
					}
					if preview != "" {
						fmt.Fprintf(os.Stderr, "[%s] %s\n", entryType, preview)
					}
				}
			}
		case *a2a.Task:
			// Non-streaming fallback: the server returned a completed Task directly.
			if e.Status.Message != nil {
				fmt.Print(extractText(e.Status.Message))
				fmt.Println()
			} else if len(e.History) > 0 {
				lastMsg := e.History[len(e.History)-1]
				fmt.Print(extractText(lastMsg))
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
