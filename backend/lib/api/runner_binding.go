package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/agents/dispatch"
	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// WithRunnerHub makes the server execute every agent run on the runner its
// chat is bound to, through hub. It also turns on runner binding: new chats
// must name a runner.
func WithRunnerHub(hub *runnerhub.Hub) ServerOption {
	return func(s *Server) {
		s.runners = hub
		s.dispatcher = dispatch.NewRemote(dispatch.FromHub(hub), s.chatRunner, s.conf)
		// The sandbox calls the runners forward (ask-user, call-peer, ...).
		hub.SetCallbacks(newCallbackService(s, hub))
	}
}

// WithDispatcher replaces the run dispatcher (used when no runner hub is
// configured, and by tests).
func WithDispatcher(d dispatch.Dispatcher) ServerOption {
	return func(s *Server) {
		s.dispatcher = d
	}
}

// agentsForRunner is implemented by agent sources that can list the agents
// visible to one runner (global plus the runner's own).
type agentsForRunner interface {
	AgentsFor(nodeID string) ([]*agentspec.Agent, error)
}

// bindingEnforced reports whether chats must be bound to a runner, which is
// the case once a runner registry is configured.
func (s *Server) bindingEnforced() bool { return s.runners != nil }

// agentsFor returns the agents visible to runnerID: the global list plus the
// runner's own agents. Without a runner, or when the source cannot scope by
// runner, it is the global list.
func (s *Server) agentsFor(runnerID string) ([]*agentspec.Agent, error) {
	if runnerID != "" {
		if src, ok := s.agentSource.(agentsForRunner); ok {
			return src.AgentsFor(runnerID)
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]*agentspec.Agent(nil), s.agents...), nil
}

// findAgentFor resolves an agent by id or name among those visible to runnerID.
func (s *Server) findAgentFor(runnerID, idOrName string) (*agentspec.Agent, error) {
	list, err := s.agentsFor(runnerID)
	if err != nil {
		return nil, err
	}
	for _, a := range list {
		if a.Config.ID == idOrName || a.Config.Name == idOrName {
			return a, nil
		}
	}
	return nil, nil
}

// bindingError is a request-level runner binding failure.
type bindingError struct {
	status int
	msg    string
}

func (e *bindingError) Error() string { return e.msg }

// resolveRunner determines the runner of a chat for a request that names
// requested (possibly empty). The stored binding always wins; naming a
// different runner is a conflict, and a chat without a binding must name one.
// bind reports that the returned runner is not stored yet. Without runner
// binding enforcement it just returns the stored runner.
func (s *Server) resolveRunner(chatID, requested string) (runnerID string, bind bool, err *bindingError) {
	stored := ""
	if s.repo != nil && chatID != "" {
		sess, serr := s.repo.GetSession(chatID)
		if serr != nil {
			return "", false, &bindingError{http.StatusInternalServerError, "failed to query session: " + serr.Error()}
		}
		if sess != nil {
			stored = sess.RunnerNodeID
		}
	}
	if !s.bindingEnforced() {
		return stored, false, nil
	}
	if stored != "" {
		if requested != "" && requested != stored {
			return "", false, &bindingError{http.StatusConflict, fmt.Sprintf("chat is bound to runner %q; runnerId cannot be changed", stored)}
		}
		return stored, false, nil
	}
	if requested == "" {
		return "", false, &bindingError{http.StatusBadRequest, "runnerId is required to start a new chat"}
	}
	if !s.runnerKnown(requested) {
		return "", false, &bindingError{http.StatusBadRequest, fmt.Sprintf("unknown runner %q", requested)}
	}
	return requested, true, nil
}

// persistRunnerBinding stores the binding decided by resolveRunner. It fails
// when a concurrent request bound the chat to another runner first.
func (s *Server) persistRunnerBinding(chatID, runnerID string) *bindingError {
	if s.repo == nil || runnerID == "" {
		return nil
	}
	bound, err := s.repo.BindRunner(chatID, runnerID)
	if err != nil {
		return &bindingError{http.StatusInternalServerError, "failed to bind runner: " + err.Error()}
	}
	if bound != runnerID {
		return &bindingError{http.StatusConflict, fmt.Sprintf("chat is bound to runner %q; runnerId cannot be changed", bound)}
	}
	return nil
}

func (s *Server) runnerKnown(id string) bool {
	if s.runners == nil {
		return false
	}
	for _, n := range s.runners.Nodes() {
		if n.NodeID == id {
			return true
		}
	}
	return false
}

// runnerOnline reports whether runner id is connected.
func (s *Server) runnerOnline(id string) bool {
	if s.runners == nil || id == "" {
		return true
	}
	for _, n := range s.runners.Nodes() {
		if n.NodeID == id {
			return n.Online
		}
	}
	return false
}

// requireRunnerOnline answers 503 and returns false when the runner of the
// request is offline.
func (s *Server) requireRunnerOnline(w http.ResponseWriter, runnerID string) bool {
	if s.bindingEnforced() && runnerID != "" && !s.runnerOnline(runnerID) {
		writeJSONError(w, http.StatusServiceUnavailable, fmt.Sprintf("runner %q is offline; try again when it reconnects", runnerID))
		return false
	}
	return true
}

// chatRunner is the dispatcher's resolver: the runner the chat is bound to.
func (s *Server) chatRunner(chatID string) (string, error) {
	if s.repo == nil {
		return "", errors.New("database unavailable: cannot determine the chat's runner")
	}
	sess, err := s.repo.GetSession(chatID)
	if err != nil {
		return "", fmt.Errorf("loading chat %s: %w", chatID, err)
	}
	if sess == nil || sess.RunnerNodeID == "" {
		return "", fmt.Errorf("chat %s is not bound to a runner", chatID)
	}
	return sess.RunnerNodeID, nil
}

// sessionAgents returns the agents visible to a chat (its runner's effective
// list) for workflow agent lookup; ok=false when the chat has no runner.
func (s *Server) sessionAgents(chatID string) ([]*agentspec.Agent, bool) {
	if s.repo == nil {
		return nil, false
	}
	sess, err := s.repo.GetSession(chatID)
	if err != nil || sess == nil || sess.RunnerNodeID == "" {
		return nil, false
	}
	list, err := s.agentsFor(sess.RunnerNodeID)
	if err != nil {
		log.Warn().Err(err).Str("runner", sess.RunnerNodeID).Msg("failed to list runner agents")
		return nil, false
	}
	return list, true
}

// chatRunnerOffline reports whether the chat is bound to a runner that is not
// connected, so its queued messages must wait.
func (s *Server) chatRunnerOffline(chatID string) bool {
	offline, _ := s.chatRunnerState(chatID)
	return offline
}

// chatRunnerState reports, from one registry snapshot, whether the chat's
// runner is offline and its online generation (see runnerhub.NodeInfo).
// Without enforcement or a binding the runner counts as available.
func (s *Server) chatRunnerState(chatID string) (offline bool, gen uint64) {
	if !s.bindingEnforced() || s.repo == nil {
		return false, 0
	}
	sess, err := s.repo.GetSession(chatID)
	if err != nil || sess == nil || sess.RunnerNodeID == "" {
		return false, 0
	}
	for _, n := range s.runners.Nodes() {
		if n.NodeID == sess.RunnerNodeID {
			return !n.Online, n.OnlineGen
		}
	}
	return true, 0
}

// queuePark records why a chat's queue is not auto-restarted. hard: the
// consumer stopped on an error a retry would repeat; it needs a new enqueue,
// trigger or a RunnerOnline event. Otherwise the consumer stopped because the
// runner was unavailable and waits for a reconnect newer than gen.
type queuePark struct {
	hard bool
	gen  uint64
}

func (s *Server) parkQueue(chatID string) { s.queueParks.Store(chatID, queuePark{hard: true}) }
func (s *Server) parkQueueUntilReconnect(chatID string, gen uint64) {
	s.queueParks.Store(chatID, queuePark{gen: gen})
}
func (s *Server) unparkQueue(chatID string) { s.queueParks.Delete(chatID) }

// queueParkedNow reports whether the chat's queue must not be restarted now.
func (s *Server) queueParkedNow(chatID string) bool {
	v, ok := s.queueParks.Load(chatID)
	if !ok {
		return false
	}
	p := v.(queuePark)
	if p.hard {
		return true
	}
	offline, gen := s.chatRunnerState(chatID)
	return offline || gen <= p.gen
}

// drainQueueAfterRelease starts a consumer for the chat when it has queued
// messages, is not parked and its runner is online.
func (s *Server) drainQueueAfterRelease(chatID string) {
	if s == nil || s.repo == nil || s.queueParkedNow(chatID) || s.chatRunnerOffline(chatID) {
		return
	}
	if head, err := s.repo.PeekNextQueuedMessage(chatID); err != nil || head == nil {
		return
	}
	s.resumeQueuedChat(chatID)
}

// RunnerOnline resumes the queued messages of chats bound to nodeID after the
// runner (re)connected. Hook it to the runner hub's node-online notification.
func (s *Server) RunnerOnline(nodeID string) {
	if s == nil || s.repo == nil {
		return
	}
	chatIDs, err := s.repo.ChatIDsForRunnerWithQueue(nodeID)
	if err != nil {
		log.Error().Err(err).Str("runner", nodeID).Msg("failed to list queued chats of runner")
		return
	}
	for _, chatID := range chatIDs {
		// A real reconnect lifts any park of the chats waiting for the runner.
		s.unparkQueue(chatID)
		s.resumeQueuedChat(chatID)
	}
}

// resumeQueuedChat starts the queue consumer of a chat with lingering queued
// messages, unless one is running already.
func (s *Server) resumeQueuedChat(chatID string) {
	sess, err := s.repo.GetSession(chatID)
	if err != nil || sess == nil || sess.IsArchived {
		return
	}
	list, err := s.agentsFor(sess.RunnerNodeID)
	if err != nil {
		log.Warn().Err(err).Str("chat_id", chatID).Msg("cannot resume queue: agent list unavailable")
		return
	}
	for _, a := range list {
		if (a.Config.ID == sess.CurrentAgent || a.Config.Name == sess.CurrentAgent) && a.Config.Type != "workflow" {
			s.startQueueConsumerIfIdle(chatID, a, sess.RunDir)
			return
		}
	}
}

// ResumeQueuesOfOnlineRunners resumes the queued messages of every connected
// runner. The app calls it once the server exists, covering runners that
// registered before the hub's online notification could reach it.
func (s *Server) ResumeQueuesOfOnlineRunners() {
	if s == nil || s.runners == nil {
		return
	}
	for _, n := range s.runners.Nodes() {
		if n.Online {
			s.RunnerOnline(n.NodeID)
		}
	}
}
