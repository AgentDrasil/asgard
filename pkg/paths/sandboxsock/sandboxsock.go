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
	"strconv"
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
	PathSubagentTree   = "/subagent/tree"
	PathSubagentStatus = "/subagent/status"
	PathSubagentKill   = "/subagent/kill"
	PathSubagentSend   = "/subagent/send"
	PathSubagentWait   = "/subagent/wait"
	PathSubagentReply  = "/subagent/reply"
	// Questions between chats of one spawn tree (ask blocks until answered).
	PathSubagentAsk       = "/subagent/ask"
	PathSubagentAnswer    = "/subagent/answer"
	PathSubagentQuestions = "/subagent/questions"
	// The routing table (topic -> chat id) of the caller's chat.
	PathSubagentRouteAdd  = "/subagent/route/add"
	PathSubagentRouteRm   = "/subagent/route/rm"
	PathSubagentRouteList = "/subagent/route/list"
	PathMetrics           = "/metrics"
	PathMerge             = "/merge"
	// Command output compass (fakebash): the runner holds the jev and Gemini
	// API keys and makes the model calls, so the keys never enter a sandbox.
	PathCompassClassify  = "/compass/classify"
	PathCompassSummarize = "/compass/summarize"

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
	// WokenByMessage is true when the wait ended early because a message from
	// another agent arrived; MessageFrom names the senders.
	WokenByMessage bool     `json:"woken_by_message,omitempty"`
	MessageFrom    []string `json:"message_from,omitempty"`
	// CheckoutHeldBy is, while a waited subagent is still running, a descendant that holds the
	// checkout lock: the caller must not modify files until it is released.
	CheckoutHeldBy string `json:"checkout_held_by,omitempty"`
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

// SubagentAskRequest is the body of POST /subagent/ask. To names the chat to
// ask, or Topic names a topic of the routing table (exactly one of them); with
// ResumeQID both are empty and the call keeps waiting for the earlier
// question. TimeoutMs 0 is the default (the backend caps it).
type SubagentAskRequest struct {
	To        string `json:"to,omitempty"`
	Topic     string `json:"topic,omitempty"`
	Text      string `json:"text,omitempty"`
	ResumeQID string `json:"resume_qid,omitempty"`
	TimeoutMs int64  `json:"timeout_ms,omitempty"`
}

// Question states reported by POST /subagent/ask.
const (
	QuestionAnswered = "answered"
	QuestionPending  = "pending"
)

// SubagentAskResponse is the answer of POST /subagent/ask: the answer, or
// state "pending" when the timeout passed first.
type SubagentAskResponse struct {
	QID    string `json:"qid"`
	State  string `json:"state"`
	Answer string `json:"answer,omitempty"`
	// Topic and the Target fields name the chat a topic ask resolved to.
	Topic       string `json:"topic,omitempty"`
	TargetID    string `json:"target,omitempty"`
	TargetAgent string `json:"target_agent,omitempty"`
	TargetLabel string `json:"target_label,omitempty"`
}

// SubagentRouteAddRequest is the body of POST /subagent/route/add.
type SubagentRouteAddRequest struct {
	Topic string `json:"topic"`
	To    string `json:"to"`
	Note  string `json:"note,omitempty"`
}

// SubagentRouteAddResponse is the answer of POST /subagent/route/add; Replaced
// is true when the topic already had an entry on the caller's chat.
type SubagentRouteAddResponse struct {
	Replaced bool `json:"replaced"`
}

// SubagentRouteRmRequest is the body of POST /subagent/route/rm.
type SubagentRouteRmRequest struct {
	Topic string `json:"topic"`
}

// SubagentRouteRmResponse is the answer of POST /subagent/route/rm.
type SubagentRouteRmResponse struct {
	Removed bool `json:"removed"`
}

// RouteDefaultOrigin is the DefinedAt of an implicit entry (the default `user`).
const RouteDefaultOrigin = "(default)"

// SubagentRouteInfo is one entry of the caller's effective routing table.
// DefinedAt is the chat that defines it, or RouteDefaultOrigin.
type SubagentRouteInfo struct {
	Topic     string `json:"topic"`
	To        string `json:"to"`
	Agent     string `json:"agent,omitempty"`
	Label     string `json:"label,omitempty"`
	State     string `json:"state"`
	DefinedAt string `json:"defined_at"`
	Note      string `json:"note,omitempty"`
	// Self is true when the entry resolves to the caller's own chat.
	Self bool `json:"self,omitempty"`
}

// SubagentRouteListResponse is the answer of GET /subagent/route/list.
type SubagentRouteListResponse struct {
	Routes []SubagentRouteInfo `json:"routes"`
}

// SubagentAnswerRequest is the body of POST /subagent/answer.
type SubagentAnswerRequest struct {
	QID  string `json:"qid"`
	Text string `json:"text"`
}

// SubagentAnswerResponse is the answer of POST /subagent/answer. Delivery is
// unblocked (the asker was blocked and got it through that call), injected,
// queued, started (message to the asker) or stored (kept for --resume only).
type SubagentAnswerResponse struct {
	Delivery string `json:"delivery"`
}

// SubagentQuestionInfo is one question of GET /subagent/questions. An incoming
// one has From* and FirstLine; an outgoing one has To and State.
type SubagentQuestionInfo struct {
	QID        string `json:"qid"`
	FromChatID string `json:"from,omitempty"`
	FromAgent  string `json:"from_agent,omitempty"`
	FromLabel  string `json:"from_label,omitempty"`
	CreatedMs  int64  `json:"created_ms"`
	FirstLine  string `json:"first_line,omitempty"`
	ToChatID   string `json:"to,omitempty"`
	State      string `json:"state,omitempty"`
}

// SubagentQuestionsResponse is the answer of GET /subagent/questions.
type SubagentQuestionsResponse struct {
	Incoming []SubagentQuestionInfo `json:"incoming"`
	Outgoing []SubagentQuestionInfo `json:"outgoing"`
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

// SubagentTreeNode is one chat of the caller's spawn tree. ParentID is empty
// for the root, whose State is running, queued (waiting for the checkout lock) or idle; Depth is 0 for the root; Self
// marks the caller's own chat.
type SubagentTreeNode struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Agent    string `json:"agent"`
	Label    string `json:"label,omitempty"`
	State    string `json:"state"`
	Readonly bool   `json:"readonly,omitempty"`
	Depth    int    `json:"depth"`
	Self     bool   `json:"self,omitempty"`
}

// SubagentTreeResponse is the answer of GET /subagent/tree, depth first.
type SubagentTreeResponse struct {
	Nodes []SubagentTreeNode `json:"nodes"`
	// Truncated is true when the tree is larger than the backend's cap and the
	// list is incomplete.
	Truncated bool `json:"truncated,omitempty"`
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
	if dl, ok := ctx.Deadline(); ok {
		if ms := time.Until(dl).Milliseconds(); ms > 0 {
			req.Header.Set(HeaderDeadlineMs, strconv.FormatInt(ms, 10))
		}
	}
	return c.http.Do(req)
}

// HeaderDeadlineMs carries the milliseconds the caller still waits for the
// answer, so the runner does not start work the caller will not see.
const HeaderDeadlineMs = "X-Asgard-Deadline-Ms"

// DeadlineBudget returns the caller's remaining budget from HeaderDeadlineMs,
// or 0 when absent or invalid.
func DeadlineBudget(r *http.Request) time.Duration {
	ms, err := strconv.ParseInt(r.Header.Get(HeaderDeadlineMs), 10, 64)
	if err != nil || ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// Get fetches path; the caller closes the response body.
func (c *Client) Get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, host+path, nil)
	if err != nil {
		return nil, err
	}
	return c.http.Do(req)
}

// CompassUsage records whether a model was consulted for a compass call and
// its token cost.
type CompassUsage struct {
	Issued bool  `json:"issued"`
	Tokens int64 `json:"tokens"`
}

// CompassClassifyRequest is the body of POST /compass/classify: the finished
// command and a head/tail sample of its output. The runner owns the question
// asked of the model; the sandbox only supplies data.
type CompassClassifyRequest struct {
	Command      string `json:"command"`
	ExitCode     int    `json:"exit_code"`
	TotalBytes   int64  `json:"total_bytes"`
	OutputSample string `json:"output_sample"`
}

// CompassClassifyResponse is the answer of POST /compass/classify. Enabled is
// false when the runner has no jev key (the sandbox then keeps the raw
// output). Strategy is one of keep_raw, drop_on_success, extract_failure,
// summarize.
type CompassClassifyResponse struct {
	Enabled  bool         `json:"enabled"`
	Strategy string       `json:"strategy,omitempty"`
	Usage    CompassUsage `json:"usage"`
}

// CompassSummarizeRequest is the body of POST /compass/summarize. Log is the
// full output (small outputs) or the inspector extract (large ones).
type CompassSummarizeRequest struct {
	Command       string `json:"command"`
	ExitCode      int    `json:"exit_code"`
	Strategy      string `json:"strategy"`
	InspectorMode bool   `json:"inspector_mode,omitempty"`
	Log           string `json:"log"`
}

// CompassSummarizeResponse is the answer of POST /compass/summarize. Enabled
// is false when the runner has no Gemini key; an empty Summary means the
// sandbox keeps the raw output.
type CompassSummarizeResponse struct {
	Enabled bool         `json:"enabled"`
	Summary string       `json:"summary,omitempty"`
	Usage   CompassUsage `json:"usage"`
}
