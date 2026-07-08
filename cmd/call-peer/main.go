package main

import (
	"context"
	"fmt"
	"os"

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
	ctx := context.Background()

	client, err := a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{
		a2a.NewAgentInterface(targetURL, a2a.TransportProtocolHTTPJSON),
	})
	if err != nil {
		log.Error().Err(err).Msg("Error creating A2A client")
		os.Exit(1)
	}

	reqMsg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(messageText))
	reqMsg.ContextID = chatID

	res, err := client.SendMessage(ctx, &a2a.SendMessageRequest{
		Message: reqMsg,
	})
	if err != nil {
		log.Error().Err(err).Msg("Error calling peer agent")
		os.Exit(1)
	}

	task, ok := res.(*a2a.Task)
	if !ok {
		log.Error().Type("result_type", res).Msg("Expected *a2a.Task response")
		os.Exit(1)
	}

	if task.Status.Message != nil {
		for _, part := range task.Status.Message.Parts {
			if part != nil && part.Text() != "" {
				fmt.Print(part.Text())
			}
		}
	} else {
		if len(task.History) > 0 {
			lastMsg := task.History[len(task.History)-1]
			for _, part := range lastMsg.Parts {
				if part != nil && part.Text() != "" {
					fmt.Print(part.Text())
				}
			}
		}
	}
	fmt.Println()
}
