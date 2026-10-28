// Package sandboxsock is the sandbox side of the runner's per-run callback
// socket: HTTP over a unix socket that the runner bind mounts into the
// sandboxes (/fakebash/asgard.sock). Every sandbox tool that talks to the
// backend (ask-user, call-peer, find-peer, show-output and fakebash
// telemetry) goes through it; the runner stamps the identity of the run and
// forwards the call to the backend.
package sandboxsock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// EnvURL names the environment variable that addresses the socket as
	// unix:///path/to/asgard.sock.
	EnvURL = "ASGARD_STATUS_URL"
	// DefaultURL is where the runner mounts the socket inside the sandboxes.
	DefaultURL = "unix:///fakebash/asgard.sock"

	// Routes served by the runner.
	PathStatus   = "/agent-status"
	PathAskUser  = "/ask-user"
	PathCallPeer = "/call-peer"
	PathFindPeer = "/find-peer"
	PathMetrics  = "/metrics"

	// host is the placeholder request host; the dialer ignores it.
	host = "http://asgard"
)

// AskUserRequest is the body of POST /ask-user. The runner supplies the chat
// and run identity itself and ignores any the sandbox claims.
type AskUserRequest struct {
	Question  string `json:"question"`
	AgentName string `json:"agent_name,omitempty"`
	Model     string `json:"model,omitempty"`
	// MessageID is optional; the runner derives one when it is empty.
	MessageID string `json:"message_id,omitempty"`
}

// AskUserResponse is the answer of POST /ask-user.
type AskUserResponse struct {
	Reply string `json:"reply"`
	Error string `json:"error,omitempty"`
}

// CallPeerRequest is the body of POST /call-peer.
type CallPeerRequest struct {
	AgentID string `json:"agent_id"`
	Prompt  string `json:"prompt"`
}

// CallPeerResponse is the answer of POST /call-peer. Status is "completed",
// "waiting_human" or a failure status.
type CallPeerResponse struct {
	Status string `json:"status"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Client talks HTTP over the runner's unix socket.
type Client struct {
	http *http.Client
}

// New builds a Client for the socket named by ASGARD_STATUS_URL (default
// DefaultURL). timeout bounds each request; zero means no timeout.
func New(timeout time.Duration) (*Client, error) {
	raw := os.Getenv(EnvURL)
	if raw == "" {
		raw = DefaultURL
	}
	sock, ok := strings.CutPrefix(raw, "unix://")
	if !ok || sock == "" {
		return nil, fmt.Errorf("%s=%q: only unix:// socket addresses are supported", EnvURL, raw)
	}
	return &Client{http: &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		},
	}}, nil
}

// PostJSON posts in as JSON to path and returns the response; the caller
// closes its body.
func (c *Client) PostJSON(ctx context.Context, path string, in any) (*http.Response, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.http.Do(req)
}

// Get fetches path; the caller closes the response body.
func (c *Client) Get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+path, nil)
	if err != nil {
		return nil, err
	}
	return c.http.Do(req)
}
