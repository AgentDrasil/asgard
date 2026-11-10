// Package sandboxsock is the sandbox side of the runner's per-run callback
// socket: HTTP over a unix socket that the runner bind mounts into the
// sandboxes (/fakebash/asgard.sock). Every sandbox tool that talks to the
// backend (ask-user, call-peer, find-peer, subagent, show-output and fakebash
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
	// Subagent routes. Later subcommands (send, wait, reply) add theirs here.
	PathSubagentSpawn  = "/subagent/spawn"
	PathSubagentList   = "/subagent/list"
	PathSubagentStatus = "/subagent/status"
	PathSubagentKill   = "/subagent/kill"
	PathSubagentSend   = "/subagent/send"
	PathSubagentWait   = "/subagent/wait"
	PathSubagentReply  = "/subagent/reply"
	PathMetrics        = "/metrics"
	PathMerge          = "/merge"

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

// SubagentSpawnRequest is the body of POST /subagent/spawn.
type SubagentSpawnRequest struct {
	AgentID  string `json:"agent_id"`
	Model    string `json:"model,omitempty"`
	Readonly bool   `json:"readonly,omitempty"`
	Label    string `json:"label,omitempty"`
	Prompt   string `json:"prompt"`
}

// SubagentSpawnResponse is the answer of POST /subagent/spawn.
type SubagentSpawnResponse struct {
	ID string `json:"id"`
}

// SubagentIDRequest is the body of POST /subagent/status and /subagent/kill.
type SubagentIDRequest struct {
	ID string `json:"id"`
}

// SubagentSendRequest is the body of POST /subagent/send.
type SubagentSendRequest struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// SubagentSendResponse is the answer of POST /subagent/send. Delivery is
// injected, queued or started.
type SubagentSendResponse struct {
	Delivery string `json:"delivery"`
}

// SubagentWaitRequest is the body of POST /subagent/wait. No IDs means all
// running direct children; TimeoutMs 0 is the default (the backend caps it).
type SubagentWaitRequest struct {
	IDs       []string `json:"ids,omitempty"`
	Any       bool     `json:"any,omitempty"`
	TimeoutMs int64    `json:"timeout_ms,omitempty"`
}

// SubagentWaitResult is one subagent of a wait. StillRunning is true when it
// had not finished by the timeout; Notes says where its outputs are.
type SubagentWaitResult struct {
	Subagent     SubagentInfo `json:"subagent"`
	StillRunning bool         `json:"still_running,omitempty"`
	Notes        string       `json:"notes,omitempty"`
}

// SubagentWaitResponse is the answer of POST /subagent/wait.
type SubagentWaitResponse struct {
	Results []SubagentWaitResult `json:"results"`
}

// SubagentReplyRequest is the body of POST /subagent/reply.
type SubagentReplyRequest struct {
	Text string `json:"text"`
}

// SubagentReplyResponse is the answer of POST /subagent/reply. Delivery is
// injected, queued or started.
type SubagentReplyResponse struct {
	Delivery string `json:"delivery"`
}

// SubagentInfo describes one subagent. State is running, done, failed or
// killed; Summary (status only) is the tail of its last output.
type SubagentInfo struct {
	ID             string `json:"id"`
	Label          string `json:"label,omitempty"`
	Agent          string `json:"agent"`
	Model          string `json:"model,omitempty"`
	Readonly       bool   `json:"readonly,omitempty"`
	State          string `json:"state"`
	Summary        string `json:"summary,omitempty"`
	LastActivityMs int64  `json:"last_activity_ms"`
}

// SubagentListResponse is the answer of GET /subagent/list.
type SubagentListResponse struct {
	Subagents []SubagentInfo `json:"subagents"`
}

// SubagentStatusResponse is the answer of POST /subagent/status.
type SubagentStatusResponse struct {
	Subagent SubagentInfo `json:"subagent"`
}

// SubagentKillResponse is the answer of POST /subagent/kill: how many runs
// (the subagent and its descendants) were stopped.
type SubagentKillResponse struct {
	Stopped int `json:"stopped"`
}

// ErrorResponse is the JSON body of a failed subagent call; the HTTP status
// carries the kind (400 invalid, 403 denied, 404 not found, 429 limit, 503
// unavailable).
type ErrorResponse struct {
	Error string `json:"error"`
}

// MergeResponse is the answer of POST /merge. Status is a worktree merge
// status ("merged", "main_moved", ...); Message tells the agent what to do
// next. OK is true when the agent has nothing left to do for the merge.
type MergeResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	OK      bool   `json:"ok"`
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
