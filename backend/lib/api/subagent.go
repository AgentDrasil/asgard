package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"uuid"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
)

// Subagents are child chats: an agent running in one chat spawns another agent
// into a new chat on the same runner, linked by Session.ParentChatID. Each
// child is a normal chat (own transcript, events and sandbox) so several can
// run in parallel, which the one-execution-per-chat guard would not allow
// inside a single chat.
//
// The methods below are the transport-independent service: callers (the
// AgentCallbackService, later) pass the caller identity stamped by the runner,
// never one taken from the sandbox.

const (
	// maxRunningSubagents caps the children of one chat that run at once.
	maxRunningSubagents = 4
	// maxSubagentDescendants bounds the walk down a (possibly corrupt) tree.
	maxSubagentDescendants = 1024
)

// SubagentErrorKind classifies a SubagentError so a transport can map it.
type SubagentErrorKind int

const (
	SubagentInvalid     SubagentErrorKind = iota // bad request
	SubagentNotFound                             // unknown chat or child
	SubagentDenied                               // not the caller's own child, or not a teammate
	SubagentLimit                                // depth or concurrency limit
	SubagentUnavailable                          // runner offline, no database
	SubagentInternal                             // anything else
)

// SubagentError is the error of the subagent service.
type SubagentError struct {
	Kind SubagentErrorKind
	Msg  string
}

func (e *SubagentError) Error() string { return e.Msg }

func subagentErr(kind SubagentErrorKind, format string, args ...any) *SubagentError {
	return &SubagentError{Kind: kind, Msg: fmt.Sprintf(format, args...)}
}

// Subagent states reported by SubagentStatus.
const (
	// SubagentStateQueued is a child that holds its execution guard but still
	// waits for the checkout directory lock (a writable child behind its
	// parent's run, or another writer).
	SubagentStateQueued  = "queued"
	SubagentStateRunning = "running"
	SubagentStateDone    = "done"
	SubagentStateFailed  = "failed"
	SubagentStateKilled  = "killed"
)

// SpawnSubagentRequest describes a spawn. CallerChat and CallerAgent come from
// the runner-stamped identity of the calling run.
type SpawnSubagentRequest struct {
	CallerChat  string
	CallerAgent string
	// TargetAgent is the id of the agent to run; it must be a teammate of the
	// caller's current agent.
	TargetAgent string
	// Model overrides the target agent's model for the child chat.
	Model string
	// Readonly mounts the checkout read-only for the child and lets it run
	// without the checkout lock.
	Readonly bool
	Label    string
	Prompt   string
}

// SubagentInfo describes one child chat.
type SubagentInfo struct {
	ID           string
	ParentChatID string
	Label        string
	Agent        string
	SpawnedBy    string
	Model        string
	Readonly     bool
	State        string
	// Summary is the tail of the child's last output (Status only).
	Summary      string
	LastActivity time.Time
}

// SpawnSubagent starts targetAgent in a new child chat of CallerChat with
// Prompt and returns the child's chat id without waiting for it. ctx is the
// caller's run: once it is done the spawn is denied.
func (s *Server) SpawnSubagent(ctx context.Context, req SpawnSubagentRequest) (string, error) {
	if s.repo == nil {
		return "", subagentErr(SubagentUnavailable, "database unavailable in degraded mode")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return "", subagentErr(SubagentInvalid, "prompt is required")
	}
	if req.TargetAgent == "" {
		return "", subagentErr(SubagentInvalid, "agent is required")
	}
	parent, err := s.repo.GetSessionMeta(req.CallerChat)
	if err != nil {
		return "", subagentErr(SubagentInternal, "failed to get session: %v", err)
	}
	if parent == nil {
		return "", subagentErr(SubagentNotFound, "caller chat not found")
	}

	// Same rule as call-peer / find-peer: agents of the current team.
	team, code, msg := s.teamAgentsFor(req.CallerChat)
	if msg != "" {
		kind := SubagentInternal
		if code == http.StatusNotFound {
			kind = SubagentNotFound
		}
		return "", subagentErr(kind, "%s", msg)
	}
	if !slices.Contains(team, req.TargetAgent) {
		return "", subagentErr(SubagentDenied, "agent %q is not on the caller's team", req.TargetAgent)
	}

	target, err := s.findAgentFor(parent.RunnerNodeID, req.TargetAgent)
	if err != nil {
		return "", subagentErr(SubagentInternal, "failed to list agents of runner: %v", err)
	}
	if target == nil {
		return "", subagentErr(SubagentNotFound, "agent %q not found", req.TargetAgent)
	}
	if target.Config.Type == "workflow" {
		return "", subagentErr(SubagentInvalid, "agent %q is a workflow; only agents can be spawned", req.TargetAgent)
	}
	// A child shares the parent's checkout. When that is a worktree, it is the
	// one of the root chat of the spawn tree (the chat that opted in).
	wtChat, wtRoot := parent.WorktreeChatID, parent.WorktreeRoot
	if parent.Worktree {
		wtChat = parent.ChatID
		owner, err := s.findAgentFor(parent.RunnerNodeID, req.CallerAgent)
		if err != nil {
			return "", subagentErr(SubagentInternal, "failed to list agents of runner: %v", err)
		}
		if owner == nil || !owner.Config.Worktree.Enabled {
			return "", subagentErr(SubagentInvalid, "the caller agent %q no longer offers git worktrees", req.CallerAgent)
		}
		wtRoot = owner.Config.Worktree.Root
	}
	if wtChat != "" {
		if !target.Config.Worktree.Enabled {
			// It would silently work in the main checkout instead.
			return "", subagentErr(SubagentInvalid, "agent %q does not offer git worktrees, but the caller chat works in one", req.TargetAgent)
		}
		if target.Config.Worktree.Root != wtRoot {
			// The child would derive a different worktree path than the owner's.
			return "", subagentErr(SubagentInvalid, "agent %q uses worktree root %q, but the caller chat's worktree is under %q", req.TargetAgent, target.Config.Worktree.Root, wtRoot)
		}
	}

	// Depth: the caller's own nesting (ancestor chats plus its call-peer chain)
	// plus the child may not pass the limit shared with call-peer.
	s.peerMu.Lock()
	chainLen := max(len(s.peerChains[req.CallerChat]), 1)
	s.peerMu.Unlock()
	base, err := s.chatBaseDepth(req.CallerChat)
	if err != nil {
		return "", subagentErr(SubagentInternal, "failed to read the caller's nesting depth: %v", err)
	}
	depth := base + chainLen + 1
	if depth > maxAgentNestingDepth {
		return "", subagentErr(SubagentLimit, "subagent nesting too deep: the caller is already %d agents deep (limit %d)", depth-1, maxAgentNestingDepth)
	}

	// The count and the start of the child are one critical section, or
	// concurrent spawns could all pass the cap.
	s.spawnMu.Lock()
	defer s.spawnMu.Unlock()
	// A stopped caller must not leave a new descendant behind (see
	// stopSessionExecution, which holds spawnMu across its cascade).
	if ctx.Err() != nil || s.sessionStopInProgress(req.CallerChat) {
		return "", subagentErr(SubagentDenied, "the caller chat is being stopped")
	}
	if n := s.runningChildren(req.CallerChat); n >= maxRunningSubagents {
		return "", subagentErr(SubagentLimit, "too many running subagents: %d of %d already running", n, maxRunningSubagents)
	}

	childID := uuid.NewV7().String()
	child := &dbmodels.Session{
		ChatID:            childID,
		CurrentAgent:      target.Config.ID,
		AgentIcon:         target.Config.Icon,
		RunDir:            parent.RunDir,
		RunnerNodeID:      parent.RunnerNodeID,
		UserSubject:       parent.UserSubject,
		AllowCrossSession: parent.AllowCrossSession,
		Title:             req.Label,
		ParentChatID:      req.CallerChat,
		SpawnedByAgent:    req.CallerAgent,
		SpawnLabel:        req.Label,
		SpawnDepth:        depth,
		SpawnReadonly:     req.Readonly,
		WorktreeChatID:    wtChat,
		WorktreeRoot:      wtRoot,
	}
	if err := s.repo.SaveSession(child); err != nil {
		return "", subagentErr(SubagentInternal, "failed to create child chat: %v", err)
	}

	// Dispatch through the async path of a normal trigger. The guard is taken
	// before triggerMessage returns, so the child counts as running at once.
	rec := newResponseCapture()
	tr := TriggerMessageRequest{
		Prompt:   req.Prompt,
		ChatID:   childID,
		RunnerID: parent.RunnerNodeID,
		RunDir:   parent.RunDir,
		Model:    req.Model,
		Metadata: map[string]any{
			"internal":        true,
			"source":          "subagent",
			"caller_agent_id": req.CallerAgent,
			"parent_chat_id":  req.CallerChat,
		},
	}
	s.triggerMessage(s.Context(), nil, rec, target.Config.ID, target, tr, false)
	if rec.code != http.StatusAccepted {
		if derr := s.repo.DeleteSessionRecord(childID); derr != nil {
			log.Warn().Err(derr).Str("chat_id", childID).Msg("failed to remove the child chat of a failed subagent spawn")
		}
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.body.Bytes(), &body)
		kind := SubagentInternal
		if rec.code == http.StatusServiceUnavailable {
			kind = SubagentUnavailable
		}
		return "", subagentErr(kind, "failed to start subagent: %s", firstNonEmpty(body.Error, fmt.Sprintf("HTTP status %d", rec.code)))
	}
	// The parent's process now has a reason to outlive its turn.
	s.syncHold(req.CallerChat)
	return childID, nil
}

// ListSubagents returns the direct children of callerChat.
func (s *Server) ListSubagents(callerChat string) ([]SubagentInfo, error) {
	if s.repo == nil {
		return nil, subagentErr(SubagentUnavailable, "database unavailable in degraded mode")
	}
	ids, err := s.repo.ChildChatIDs(callerChat)
	if err != nil {
		return nil, subagentErr(SubagentInternal, "failed to list subagents: %v", err)
	}
	out := make([]SubagentInfo, 0, len(ids))
	for _, id := range ids {
		sess, err := s.repo.GetSessionMeta(id)
		if err != nil || sess == nil {
			continue
		}
		out = append(out, s.subagentInfo(sess))
	}
	return out, nil
}

// SubagentStatus reports one subagent of callerChat or of its descendants.
func (s *Server) SubagentStatus(callerChat, childID string) (SubagentInfo, error) {
	sess, err := s.ownedSubagent(callerChat, childID)
	if err != nil {
		return SubagentInfo{}, err
	}
	info := s.subagentInfo(sess)
	info.Summary = s.lastOutputTail(childID, 2000)
	return info, nil
}

// lastOutputTail is the tail of the last assistant or error message of a chat.
func (s *Server) lastOutputTail(chatID string, n int) string {
	full, err := s.repo.GetSession(chatID)
	if err != nil || full == nil {
		return ""
	}
	for i := len(full.Messages) - 1; i >= 0; i-- {
		m := full.Messages[i]
		if m.Role == "error" || m.Role == "assistant" {
			return tailText(m.Content, n)
		}
	}
	return ""
}

// KillSubagent stops a subagent of callerChat (or of its descendants) and all
// of that subagent's own descendants. It returns how many runs were stopped;
// killing a subagent that already finished stops none and is not an error.
func (s *Server) KillSubagent(callerChat, childID string) (int, error) {
	if _, err := s.ownedSubagent(callerChat, childID); err != nil {
		return 0, err
	}
	s.spawnMu.Lock()
	defer s.spawnMu.Unlock()
	stopped := 0
	descendants, derr := s.descendantChats(childID)
	// The killer's chat is told synchronously, so none of the stopped chats
	// (all below the caller) pushes its finish up the chain.
	if c, _ := s.stopDescendant(childID); c == http.StatusOK {
		stopped++
	}
	for _, id := range descendants {
		if c, _ := s.stopDescendant(id); c == http.StatusOK {
			stopped++
		}
	}
	if derr != nil {
		return stopped, subagentErr(SubagentInternal, "stopped %d subagents but failed to enumerate the descendants, retry the kill: %v", stopped, derr)
	}
	return stopped, nil
}

// Deliveries reported by SendToSubagent.
const (
	// SubagentDeliveryInjected: pushed into the child's running turn.
	SubagentDeliveryInjected = "injected"
	// SubagentDeliveryQueued: waits behind the child's running turn (or its
	// wait for the checkout lock) and runs as its next turn.
	SubagentDeliveryQueued = "queued"
	// SubagentDeliveryStarted: the child was idle and a new turn started.
	SubagentDeliveryStarted = "started"
)

// maxSubagentMessageBytes bounds a message to a subagent (Hub.Inject has its
// own, larger, limit).
const maxSubagentMessageBytes = 256 << 10

// SendToSubagent delivers text from callerAgent (running in callerChat) to a
// subagent of callerChat or of its descendants. A child that is running a CLI
// with live injection (claude, agy) gets it in its current turn; otherwise
// the message goes through the normal trigger path: queued behind the running
// turn, or, for an idle, finished, failed or killed child, a new turn that
// resumes the child's CLI session. Restarting an idle child counts toward the
// running-children cap of its parent. The text is prefixed with its sender.
// ctx is the caller's run: once it is done the send is denied, so a stopped
// caller cannot restart or queue its killed child.
func (s *Server) SendToSubagent(ctx context.Context, callerChat, callerAgent, childID, text string) (string, error) {
	child, err := s.ownedSubagent(callerChat, childID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(text) == "" {
		return "", subagentErr(SubagentInvalid, "message is required")
	}
	if len(text) > maxSubagentMessageBytes {
		return "", subagentErr(SubagentInvalid, "message too large (limit %d bytes)", maxSubagentMessageBytes)
	}
	if s.sessionStopInProgress(callerChat) || s.sessionStopInProgress(childID) {
		return "", subagentErr(SubagentDenied, "the subagent or its caller is being stopped")
	}
	target, err := s.findAgentFor(child.RunnerNodeID, child.CurrentAgent)
	if err != nil {
		return "", subagentErr(SubagentInternal, "failed to list agents of runner: %v", err)
	}
	if target == nil || target.Config.Type == "workflow" {
		return "", subagentErr(SubagentNotFound, "the agent of subagent %q is not available", childID)
	}
	senderName := callerAgent
	if a, _ := s.findAgentFor(child.RunnerNodeID, callerAgent); a != nil {
		senderName = a.Config.Name
	}
	prompt := fmt.Sprintf("[message from parent agent %s]\n%s", senderName, text)

	// A running turn that can take input gets the message right away.
	if s.tryInject(childID, target.Config.ID, prompt) {
		return SubagentDeliveryInjected, nil
	}

	// The inject may have waited; a caller stopped meanwhile must not revive
	// the child.
	if ctx.Err() != nil {
		return "", subagentErr(SubagentDenied, "the caller is being stopped")
	}

	// Otherwise through the normal trigger path. The cap check and the start
	// of an idle child are one critical section, like in SpawnSubagent.
	s.spawnMu.Lock()
	defer s.spawnMu.Unlock()
	if ctx.Err() != nil || s.sessionStopInProgress(callerChat) || s.sessionStopInProgress(childID) {
		return "", subagentErr(SubagentDenied, "the subagent or its caller is being stopped")
	}
	if _, running := s.activeExecutions.Load(childID); !running && child.ParentChatID != "" {
		if n := s.runningChildren(child.ParentChatID); n >= maxRunningSubagents {
			return "", subagentErr(SubagentLimit, "too many running subagents: %d of %d already running", n, maxRunningSubagents)
		}
	}
	rec := newResponseCapture()
	s.triggerMessage(s.Context(), nil, rec, target.Config.ID, target, TriggerMessageRequest{
		Prompt:   prompt,
		ChatID:   childID,
		RunnerID: child.RunnerNodeID,
		RunDir:   child.RunDir,
		Metadata: map[string]any{
			"source":          "subagent-send",
			"caller_agent_id": callerAgent,
			"parent_chat_id":  callerChat,
		},
	}, false)
	var body struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	_ = json.Unmarshal(rec.body.Bytes(), &body)
	if rec.code != http.StatusAccepted {
		kind := SubagentInternal
		switch {
		case rec.code == http.StatusServiceUnavailable:
			kind = SubagentUnavailable
		case body.Error == queueLimitMessage:
			kind = SubagentLimit
		}
		return "", subagentErr(kind, "failed to send to subagent: %s", firstNonEmpty(body.Error, fmt.Sprintf("HTTP status %d", rec.code)))
	}
	if body.Status == "queued" {
		return SubagentDeliveryQueued, nil
	}
	return SubagentDeliveryStarted, nil
}

// tryInject pushes prompt into the running turn of agentID in chatID when its
// CLI takes live input, and records it in the transcript. False means nothing
// was injected and the caller falls back to the normal trigger path.
func (s *Server) tryInject(chatID, agentID, prompt string) bool {
	if s.activeRuns == nil || s.injector == nil {
		return false
	}
	nodeID, runID, ok := s.activeRuns.Lookup(chatID, agentID)
	if !ok {
		return false
	}
	// A parent waiting between turns for its children must give up its
	// waiting mark before a new turn can write; when it cannot, the message
	// is queued instead.
	ready, done := s.prepareHeldInject(chatID)
	if !ready {
		return false
	}
	injected, idleGen, err := s.inject(context.WithoutCancel(s.Context()), nodeID, runID, prompt)
	done(injected, idleGen)
	if injected {
		s.recordInjectedMessage(chatID, prompt)
		return true
	}
	if err != nil && !errors.Is(err, runnerhub.ErrUnknownRun) && !errors.Is(err, runnerhub.ErrRunnerOffline) {
		log.Warn().Err(err).Str("chat_id", chatID).Msg("subagent inject failed, queueing the message instead")
	}
	return false
}

// recordInjectedMessage shows an injected message in the child's transcript,
// as a run's incoming prompt.
func (s *Server) recordInjectedMessage(chatID, prompt string) {
	msg := dbmodels.ChatMessage{
		ID:        fmt.Sprintf("msg-%s", uuid.NewV7().String()),
		Role:      "user",
		Content:   prompt,
		Timestamp: time.Now().UnixMilli(),
	}
	if err := s.repo.AppendMessage(chatID, msg); err != nil {
		log.Error().Err(err).Str("chat_id", chatID).Msg("failed to append injected message to repo")
		return
	}
	s.PublishSessionEvent(chatID, SessionEvent{Type: EventTypeMessage, Message: &msg})
}

// ownedSubagent loads childID, which must be a descendant of callerChat.
func (s *Server) ownedSubagent(callerChat, childID string) (*dbmodels.Session, error) {
	if s.repo == nil {
		return nil, subagentErr(SubagentUnavailable, "database unavailable in degraded mode")
	}
	if !IsValidChatID(childID) {
		return nil, subagentErr(SubagentInvalid, "invalid subagent id")
	}
	sess, err := s.repo.GetSessionMeta(childID)
	if err != nil {
		return nil, subagentErr(SubagentInternal, "failed to get subagent: %v", err)
	}
	// An unknown id and somebody else's child look the same to the caller.
	if sess == nil || !s.isDescendantChat(callerChat, sess) {
		return nil, subagentErr(SubagentNotFound, "subagent %q not found", childID)
	}
	return sess, nil
}

// isDescendantChat reports whether sess is below ancestor in the spawn tree.
func (s *Server) isDescendantChat(ancestor string, sess *dbmodels.Session) bool {
	seen := map[string]bool{}
	for cur := sess; cur != nil && cur.ParentChatID != "" && len(seen) < maxSubagentDescendants; {
		if cur.ParentChatID == ancestor {
			return true
		}
		if seen[cur.ChatID] {
			return false
		}
		seen[cur.ChatID] = true
		next, err := s.repo.GetSessionMeta(cur.ParentChatID)
		if err != nil {
			return false
		}
		cur = next
	}
	return false
}

// spawnRoot is the root chat of the spawn tree chatID belongs to: the topmost
// ancestor, or chatID itself when it has no parent.
func (s *Server) spawnRoot(sess *dbmodels.Session) (string, error) {
	cur := sess
	seen := map[string]bool{}
	for cur.ParentChatID != "" {
		if seen[cur.ChatID] || len(seen) >= maxSubagentDescendants {
			return "", fmt.Errorf("spawn tree of chat %s is too deep or cyclic", sess.ChatID)
		}
		seen[cur.ChatID] = true
		next, err := s.repo.GetSessionMeta(cur.ParentChatID)
		if err != nil {
			return "", fmt.Errorf("read parent chat %s: %w", cur.ParentChatID, err)
		}
		if next == nil {
			// The parent was deleted: the child is the topmost chat left.
			return cur.ChatID, nil
		}
		cur = next
	}
	return cur.ChatID, nil
}

// descendantChats lists every chat below chatID, nearest first. A failing
// lookup is retried once; when it still fails the chats found so far are
// returned together with the error, so callers can act on them and report the
// truncation.
func (s *Server) descendantChats(chatID string) ([]string, error) {
	if s.repo == nil {
		return nil, nil
	}
	var firstErr error
	var out []string
	seen := map[string]bool{chatID: true}
	for queue := []string{chatID}; len(queue) > 0 && len(out) < maxSubagentDescendants; queue = queue[1:] {
		ids, err := s.repo.ChildChatIDs(queue[0])
		if err != nil {
			ids, err = s.repo.ChildChatIDs(queue[0])
		}
		if err != nil {
			log.Error().Err(err).Str("chat_id", queue[0]).Msg("failed to list subagent chats")
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
				queue = append(queue, id)
			}
		}
	}
	return out, firstErr
}

// runningChildren counts the direct children of chatID that are executing.
func (s *Server) runningChildren(chatID string) int {
	ids, err := s.repo.ChildChatIDs(chatID)
	if err != nil {
		return 0
	}
	n := 0
	for _, id := range ids {
		if _, running := s.activeExecutions.Load(id); running {
			n++
		}
	}
	return n
}

func (s *Server) subagentInfo(sess *dbmodels.Session) SubagentInfo {
	info := SubagentInfo{
		ID:           sess.ChatID,
		ParentChatID: sess.ParentChatID,
		Label:        sess.SpawnLabel,
		Agent:        sess.CurrentAgent,
		SpawnedBy:    sess.SpawnedByAgent,
		Readonly:     sess.SpawnReadonly,
		LastActivity: sess.UpdatedAt,
		State:        SubagentStateDone,
	}
	for _, a := range sess.Agents {
		if a.Name == sess.CurrentAgent {
			info.Model = a.Model
		}
	}
	if _, running := s.activeExecutions.Load(sess.ChatID); running {
		info.State = SubagentStateRunning
		if !sess.SpawnReadonly {
			if st := s.dirLockStatus(sess.ChatID); st != nil && st.IsWaitingForLock && !st.IsLocked {
				info.State = SubagentStateQueued
			}
		}
	} else {
		switch sess.SpawnOutcome {
		case dbmodels.SpawnOutcomeKilled:
			info.State = SubagentStateKilled
		case dbmodels.SpawnOutcomeFailed:
			info.State = SubagentStateFailed
		}
	}
	return info
}

// tailText keeps the last max bytes of s on a rune boundary.
func tailText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := len(s) - n
	for cut < len(s) && s[cut]&0xC0 == 0x80 {
		cut++
	}
	return "..." + s[cut:]
}
