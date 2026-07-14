package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
	"gopkg.in/yaml.v3"
)

func main() {
	agentURL := flag.String("a", "", "Agent URL to connect to (resolves card)")
	message := flag.String("m", "", "Message to send to the agent")
	flag.Parse()

	if *agentURL == "" {
		log.Fatal("Agent URL (-a) is required")
	}
	if *message == "" {
		log.Fatal("Message (-m) is required")
	}

	ctx := context.Background()

	// Resolve the AgentCard from the URL.
	card, err := agentcard.DefaultResolver.Resolve(ctx, *agentURL)
	if err != nil {
		log.Fatalf("Failed to resolve AgentCard: %v", err)
	}

	// Create client from the resolved AgentCard.
	client, err := a2aclient.NewFromCard(ctx, card)
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}
	defer func() {
		if err := client.Destroy(); err != nil {
			log.Printf("Failed to destroy client: %v", err)
		}
	}()

	// Prepare request
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(*message))
	req := &a2a.SendMessageRequest{
		Message: msg,
	}

	// Use SendStreamingMessage to receive status updates.
	events := client.SendStreamingMessage(ctx, req)
	for event, err := range events {
		if err != nil {
			log.Fatalf("Error during streaming: %v", err)
		}

		y, err := yaml.Marshal(event)
		if err != nil {
			fmt.Printf("Failed to marshal event to YAML: %v\n", err)
			continue
		}
		fmt.Printf("--- Event (%T) ---\n%s\n", event, string(y))
	}
}
