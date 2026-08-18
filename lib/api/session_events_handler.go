package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
)

// handleSessionEvents handles GET /api/sessions/{id}/events as an SSE stream.
func (s *Server) handleSessionEvents(w http.ResponseWriter, r *http.Request) {
	if s.repo == nil {
		http.Error(w, `{"error":"session repository not initialized"}`, http.StatusInternalServerError)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		http.Error(w, `{"error":"session id is required"}`, http.StatusBadRequest)
		return
	}
	if !IsValidChatID(id) {
		http.Error(w, `{"error":"invalid session id format"}`, http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	var lastEventID int64
	if lastIDStr := r.Header.Get("Last-Event-ID"); lastIDStr != "" {
		if parsed, err := strconv.ParseInt(lastIDStr, 10, 64); err == nil {
			lastEventID = parsed
		}
	} else if q := r.URL.Query().Get("lastEventId"); q != "" {
		if parsed, err := strconv.ParseInt(q, 10, 64); err == nil {
			lastEventID = parsed
		}
	}

	if s.eventHub == nil {
		http.Error(w, `{"error":"event hub not initialized"}`, http.StatusInternalServerError)
		return
	}

	eventCh, doneCh, cancel := s.eventHub.Subscribe(id, lastEventID)
	defer cancel()

	// Initial flush to send headers immediately
	flusher.Flush()

	pingTicker := time.NewTicker(15 * time.Second)
	defer pingTicker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case <-doneCh:
			return

		case <-pingTicker.C:
			if _, err := fmt.Fprintf(w, ":ping\n\n"); err != nil {
				return
			}
			flusher.Flush()

		case ev, ok := <-eventCh:
			if !ok {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				log.Error().Err(err).Str("chat_id", id).Msg("failed to marshal session event")
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.EventID, ev.Type, string(data)); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
