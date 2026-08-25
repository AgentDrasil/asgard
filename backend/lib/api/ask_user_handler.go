package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
	"uuid"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

type askUserWaiter struct {
	chatID    string
	messageID string
	replyCh   chan string
}

var (
	askWaitersMu sync.Mutex
	askWaiters   = make(map[string]*askUserWaiter)
)

type AskUserRequest struct {
	ChatID    string `json:"chat_id"`
	AgentName string `json:"agent_name"`
	Question  string `json:"question"`
	MessageID string `json:"message_id"`
}

type AskUserResponse struct {
	Reply string `json:"reply"`
	Error string `json:"error,omitempty"`
}

type AskUserReplyRequest struct {
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id"`
	ReplyText string `json:"reply_text"`
}

func (s *Server) handleAskUser(w http.ResponseWriter, r *http.Request) {
	var req AskUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.ChatID == "" || req.Question == "" {
		http.Error(w, "chat_id and question are required", http.StatusBadRequest)
		return
	}

	if req.MessageID == "" {
		req.MessageID = fmt.Sprintf("ask-%s", uuid.NewV7().String())
	}

	log.Info().Str("chat_id", req.ChatID).Str("question", req.Question).Str("agent_name", req.AgentName).Msg("handleAskUser: received ask-user request from sandbox")

	if s.repo != nil {
		agentName := req.AgentName
		if agentName == "" {
			session, _ := s.repo.GetSession(req.ChatID)
			if session != nil {
				agentName = session.CurrentAgent
			}
		}
		msg := dbmodels.ChatMessage{
			ID:        req.MessageID,
			Role:      "ask_user",
			Content:   req.Question,
			AgentName: agentName,
			Timestamp: time.Now().UnixMilli(),
		}
		if err := s.repo.AppendMessage(req.ChatID, msg); err == nil {
			s.PublishSessionEvent(req.ChatID, SessionEvent{
				Type:    "message",
				Message: &msg,
			})
		} else {
			log.Error().Err(err).Str("chat_id", req.ChatID).Msg("failed to append ask_user message to repo")
		}
		s.SendPushNotification(req.ChatID, req.Question, agentName)
	} else {
		s.SendPushNotification(req.ChatID, req.Question, req.AgentName)
	}

	replyCh := make(chan string, 1)
	waiter := &askUserWaiter{
		chatID:    req.ChatID,
		messageID: req.MessageID,
		replyCh:   replyCh,
	}

	askWaitersMu.Lock()
	askWaiters[req.MessageID] = waiter
	askWaitersMu.Unlock()

	defer func() {
		askWaitersMu.Lock()
		delete(askWaiters, req.MessageID)
		askWaitersMu.Unlock()
	}()

	select {
	case reply := <-replyCh:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AskUserResponse{Reply: reply})
	case <-r.Context().Done():
		log.Warn().Str("chat_id", req.ChatID).Str("message_id", req.MessageID).Msg("ask-user HTTP context cancelled before user replied")
		http.Error(w, "client disconnected", http.StatusRequestTimeout)
	}
}

func (s *Server) handleAskUserReply(w http.ResponseWriter, r *http.Request) {
	var req AskUserReplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.ChatID == "" || req.ReplyText == "" {
		http.Error(w, "chat_id and reply_text are required", http.StatusBadRequest)
		return
	}

	if s.repo != nil {
		updatedMsg, err := s.repo.MarkAskUserReplied(req.ChatID, req.MessageID, req.ReplyText)
		if err != nil {
			log.Warn().Err(err).Str("chat_id", req.ChatID).Str("message_id", req.MessageID).Msg("failed to mark ask-user replied in repo")
		} else if updatedMsg != nil {
			s.PublishSessionEvent(req.ChatID, SessionEvent{
				Type:    "message",
				Message: updatedMsg,
			})
		}
	}

	// Route the reply into a suspended workflow run (WAITING_HUMAN), if any.
	s.tryResumeWorkflow(req.ChatID, req.MessageID, req.ReplyText)

	askWaitersMu.Lock()
	waiter, exists := askWaiters[req.MessageID]
	if !exists && req.MessageID == "" {
		// If no messageID is provided, fall back only if there is exactly 1 waiter for this chat.
		var matchedWaiter *askUserWaiter
		count := 0
		for _, w := range askWaiters {
			if w.chatID == req.ChatID {
				matchedWaiter = w
				count++
			}
		}
		if count == 1 {
			waiter = matchedWaiter
			exists = true
		} else if count > 1 {
			log.Warn().Str("chat_id", req.ChatID).Int("count", count).Msg("multiple ask-user waiters in chat; cannot disambiguate reply without message_id")
		}
	}
	askWaitersMu.Unlock()

	if exists && waiter != nil {
		select {
		case waiter.replyCh <- req.ReplyText:
		default:
		}
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
