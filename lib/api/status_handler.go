package api

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/rs/zerolog/log"
)

// AgentStatusUpdate is the JSON payload posted by aw to the internal status
// endpoint whenever the agent produces an incremental transcript update.
type AgentStatusUpdate struct {
	ChatID    string         `json:"chat_id"`
	StepIndex int            `json:"step_index"`
	Source    string         `json:"source"`
	EntryType string         `json:"entry_type"`
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// statusListenersMu guards statusListeners.
var statusListenersMu sync.Mutex

// AddStatusListener registers a buffered channel that will receive all
// AgentStatusUpdate events for the given chatID. The returned cancel function
// must be called to deregister the channel and free resources.
func (s *Server) AddStatusListener(chatID string) (<-chan AgentStatusUpdate, func()) {
	ch := make(chan AgentStatusUpdate, 64)

	statusListenersMu.Lock()
	if s.statusListeners == nil {
		s.statusListeners = make(map[string][]chan AgentStatusUpdate)
	}
	s.statusListeners[chatID] = append(s.statusListeners[chatID], ch)
	statusListenersMu.Unlock()

	cancel := func() {
		statusListenersMu.Lock()
		defer statusListenersMu.Unlock()
		listeners := s.statusListeners[chatID]
		for i, l := range listeners {
			if l == ch {
				s.statusListeners[chatID] = append(listeners[:i], listeners[i+1:]...)
				close(ch)
				break
			}
		}
		if len(s.statusListeners[chatID]) == 0 {
			delete(s.statusListeners, chatID)
		}
	}

	return ch, cancel
}

// handleAgentStatus handles POST /agent-status on the internal-only server.
// It decodes the update, logs it, and fans it out to all registered listeners
// for the update's ChatID.
func (s *Server) handleAgentStatus(w http.ResponseWriter, r *http.Request) {
	var update AgentStatusUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	log.Debug().
		Str("chat_id", update.ChatID).
		Int("step_index", update.StepIndex).
		Str("source", update.Source).
		Str("entry_type", update.EntryType).
		Msg("agent status update received")

	statusListenersMu.Lock()
	listeners := make([]chan AgentStatusUpdate, len(s.statusListeners[update.ChatID]))
	copy(listeners, s.statusListeners[update.ChatID])
	statusListenersMu.Unlock()

	for _, ch := range listeners {
		select {
		case ch <- update:
		default:
			// Drop update if listener buffer is full to avoid blocking the reporter.
			log.Warn().Str("chat_id", update.ChatID).Msg("status listener buffer full, dropping update")
		}
	}

	w.WriteHeader(http.StatusNoContent)
}
