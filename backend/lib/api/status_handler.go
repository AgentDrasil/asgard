package api

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/workflow"
)

// AgentStatusUpdate is the JSON payload posted by aw to the internal status
// endpoint whenever the agent produces an incremental transcript update.
type AgentStatusUpdate = workflow.AgentStatusUpdate

// statusListener pairs a buffered update channel with an optional match
// predicate. When match is non-nil only updates it accepts are delivered;
// nil match delivers everything registered for the chatID.
type statusListener struct {
	ch      chan workflow.AgentStatusUpdate
	match   func(workflow.AgentStatusUpdate) bool
	dropped atomic.Int64
}

// statusListenersMu guards statusListeners.
var statusListenersMu sync.Mutex

// AddStatusListener registers a buffered channel that will receive
// AgentStatusUpdate events for the given chatID. match, when non-nil, filters
// which updates are delivered (e.g. by RunToken so parallel workflow nodes
// only receive their own updates). The returned cancel function must be called
// to deregister the channel and free resources.
func (s *Server) AddStatusListener(chatID string, match func(workflow.AgentStatusUpdate) bool) (<-chan workflow.AgentStatusUpdate, func()) {
	l := &statusListener{
		ch:    make(chan workflow.AgentStatusUpdate, 256),
		match: match,
	}

	statusListenersMu.Lock()
	if s.statusListeners == nil {
		s.statusListeners = make(map[string][]*statusListener)
	}
	s.statusListeners[chatID] = append(s.statusListeners[chatID], l)
	statusListenersMu.Unlock()

	cancel := func() {
		statusListenersMu.Lock()
		defer statusListenersMu.Unlock()
		listeners := s.statusListeners[chatID]
		for i, cand := range listeners {
			if cand == l {
				s.statusListeners[chatID] = append(listeners[:i], listeners[i+1:]...)
				break
			}
		}
		if len(s.statusListeners[chatID]) == 0 {
			delete(s.statusListeners, chatID)
		}
	}

	return l.ch, cancel
}

// handleAgentStatus handles POST /agent-status on the internal-only server.
// It decodes the update, logs it, and fans it out to all registered listeners
// for the update's ChatID whose match predicate (if any) accepts it.
func (s *Server) handleAgentStatus(w http.ResponseWriter, r *http.Request) {
	var update AgentStatusUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	log.Debug().
		Str("chat_id", update.ChatID).
		Str("node_id", update.NodeID).
		Str("run_token", update.RunToken).
		Int("step_index", update.StepIndex).
		Str("source", update.Source).
		Str("entry_type", update.EntryType).
		Msg("agent status update received")

	// The channel is intentionally never closed on cancel: fan-out sends below
	// run outside the mutex on a snapshot of the listener list, so closing in
	// cancel could race with an in-flight send and panic (send on closed
	// channel). Consumers stop draining via their run-result channel or
	// context instead; the channel itself is GC'd once unreferenced.
	statusListenersMu.Lock()
	listeners := make([]*statusListener, len(s.statusListeners[update.ChatID]))
	copy(listeners, s.statusListeners[update.ChatID])
	statusListenersMu.Unlock()

	for _, l := range listeners {
		if l.match != nil && !l.match(update) {
			continue
		}
		select {
		case l.ch <- update:
		default:
			// Drop update if listener buffer is full to avoid blocking the reporter.
			count := l.dropped.Add(1)
			log.Warn().
				Str("chat_id", update.ChatID).
				Int64("dropped_count", count).
				Msg("status listener buffer full, dropping update")
		}
	}

	w.WriteHeader(http.StatusNoContent)
}
