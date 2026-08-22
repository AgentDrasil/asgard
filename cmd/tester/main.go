package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
)

func main() {
	agentID := flag.String("a", "", "Agent ID or URL to connect to")
	message := flag.String("m", "", "Message to send to the agent")
	host := flag.String("server", "http://localhost:8080", "Asgard server host URL")
	flag.Parse()

	if *agentID == "" {
		log.Fatal("Agent ID (-a) is required")
	}
	if *message == "" {
		log.Fatal("Message (-m) is required")
	}

	target := *agentID
	if strings.Contains(target, "/agents/") {
		parts := strings.Split(target, "/agents/")
		target = parts[len(parts)-1]
		target = strings.Trim(target, "/")
	}

	ctx := context.Background()
	reqBody, err := json.Marshal(map[string]any{
		"prompt": *message,
		"wait":   true,
	})
	if err != nil {
		log.Fatalf("Failed to marshal request: %v", err)
	}

	url := fmt.Sprintf("%s/api/agents/%s/message?wait=true", strings.TrimRight(*host, "/"), target)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		log.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("Request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Status string `json:"status"`
		Output string `json:"output"`
		Error  string `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Fatalf("Failed to decode response: %v", err)
	}

	fmt.Printf("Status: %s\n", result.Status)
	if result.Output != "" {
		fmt.Printf("Output: %s\n", result.Output)
	}
	if result.Error != "" {
		fmt.Printf("Error: %s\n", result.Error)
	}
}
