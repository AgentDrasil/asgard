package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/auth"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

// userSeenTTL throttles how often a user's login record is refreshed.
const userSeenTTL = 5 * time.Minute

// userTracker remembers when each subject was last written to the users table.
type userTracker struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// trackUser records the authenticated user of r so an admin can assign
// runners to it. Failures are logged and never block the request.
func (s *Server) trackUser(r *http.Request) {
	if s.access == nil {
		return
	}
	u, ok := auth.UserFromContext(r.Context())
	if !ok || u.Subject == "" {
		return
	}
	s.users.mu.Lock()
	if s.users.seen == nil {
		s.users.seen = map[string]time.Time{}
	}
	last, hit := s.users.seen[u.Subject]
	fresh := hit && time.Since(last) < userSeenTTL
	if !fresh {
		s.users.seen[u.Subject] = time.Now()
	}
	s.users.mu.Unlock()
	if fresh {
		return
	}
	if err := s.access.UpsertUser(u.Subject, u.Name, u.Email); err != nil {
		log.Warn().Err(err).Str("subject", u.Subject).Msg("failed to record user")
		s.users.mu.Lock()
		delete(s.users.seen, u.Subject)
		s.users.mu.Unlock()
	}
	if u.Picture != "" && s.auth != nil {
		_ = s.auth.EnsureAvatar(r.Context(), u.Subject, u.Picture)
	}
}

// requestSubject returns the authenticated subject of r, or "".
func requestSubject(r *http.Request) string {
	if u, ok := auth.UserFromContext(r.Context()); ok {
		return u.Subject
	}
	return ""
}

// assignedRunners returns the runners the requester may use. It fails closed:
// no database or no authenticated subject means no runners.
func (s *Server) assignedRunners(r *http.Request) (map[string]bool, error) {
	sub := requestSubject(r)
	if s.access == nil || sub == "" {
		return map[string]bool{}, nil
	}
	return s.access.RunnersForUser(sub)
}

// authorizeRunner checks that the requester may start a chat on runnerID.
func (s *Server) authorizeRunner(r *http.Request, runnerID string) *bindingError {
	sub := requestSubject(r)
	if s.access == nil || sub == "" {
		return &bindingError{http.StatusForbidden, "no runner is assigned to you; ask an administrator"}
	}
	ok, err := s.access.IsAssigned(runnerID, sub)
	if err != nil {
		log.Error().Err(err).Msg("failed to check runner assignment")
		return &bindingError{http.StatusInternalServerError, "failed to check runner assignment"}
	}
	if !ok {
		return &bindingError{http.StatusForbidden, "runner " + runnerID + " is not assigned to you"}
	}
	return nil
}

// authorizeSession checks that the requester may access chatID. When runner
// binding is enforced, the session must exist, be bound to a runner, and that
// runner must be assigned to the requester.
func (s *Server) authorizeSession(r *http.Request, chatID string) (*dbmodels.Session, *bindingError) {
	if !IsValidChatID(chatID) {
		return nil, &bindingError{http.StatusBadRequest, "invalid session_id format"}
	}
	if s.repo != nil {
		sess, err := s.repo.GetSessionMeta(chatID)
		if err != nil {
			return nil, &bindingError{http.StatusInternalServerError, "failed to query session: " + err.Error()}
		}
		if sess == nil {
			return nil, &bindingError{http.StatusNotFound, "session not found"}
		}
		if s.bindingEnforced() {
			if sess.RunnerNodeID == "" {
				return nil, &bindingError{http.StatusBadRequest, "chat " + chatID + " is not bound to a runner"}
			}
			if aerr := s.authorizeRunner(r, sess.RunnerNodeID); aerr != nil {
				return nil, aerr
			}
		}
		return sess, nil
	}
	return nil, nil
}

// canSeeChat reports whether a cross-chat event for otherChatID may be forwarded
// given the requester's assigned runner set. It fails closed.
func (s *Server) canSeeChat(assignedRunners map[string]bool, otherChatID string) bool {
	if s == nil || s.repo == nil || otherChatID == "" || len(assignedRunners) == 0 {
		return false
	}
	meta, err := s.repo.GetSessionMeta(otherChatID)
	if err != nil || meta == nil || meta.RunnerNodeID == "" {
		return false
	}
	return assignedRunners[meta.RunnerNodeID]
}

// ManageRunnerResponse is one runner in GET /api/manage/runners.
type ManageRunnerResponse struct {
	NodeID string   `json:"node_id"`
	Online bool     `json:"online"`
	Users  []string `json:"users"`
}

// ManageUserResponse is one known user in GET /api/manage/runners.
type ManageUserResponse struct {
	Subject   string `json:"subject"`
	Name      string `json:"name,omitempty"`
	Email     string `json:"email,omitempty"`
	AvatarURL string `json:"avatarUrl,omitempty"`
}

// ManageRunnersResponse is the body of GET /api/manage/runners.
type ManageRunnersResponse struct {
	Runners []ManageRunnerResponse `json:"runners"`
	Users   []ManageUserResponse   `json:"users"`
}

// handleManageRunners handles GET /api/manage/runners (admin only).
func (s *Server) handleManageRunners(w http.ResponseWriter, r *http.Request) {
	if !auth.HasRole(r.Context(), auth.AdminRole) {
		writeJSONError(w, http.StatusForbidden, "admin role required")
		return
	}
	if s.access == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	byNode, err := s.access.AssignmentsByNode()
	if err != nil {
		log.Error().Err(err).Msg("failed to load assignments")
		writeJSONError(w, http.StatusInternalServerError, "failed to load assignments")
		return
	}
	users, err := s.access.ListUsers()
	if err != nil {
		log.Error().Err(err).Msg("failed to load users")
		writeJSONError(w, http.StatusInternalServerError, "failed to load users")
		return
	}
	resp := ManageRunnersResponse{Runners: []ManageRunnerResponse{}, Users: []ManageUserResponse{}}
	if s.runners != nil {
		for _, n := range s.runners.Nodes() {
			u := byNode[n.NodeID]
			if u == nil {
				u = []string{}
			}
			resp.Runners = append(resp.Runners, ManageRunnerResponse{NodeID: n.NodeID, Online: n.Online, Users: u})
		}
	}
	for _, u := range users {
		item := ManageUserResponse{Subject: u.Subject, Name: u.Name, Email: u.Email}
		if s.auth != nil && s.auth.HasAvatar(u.Subject) {
			item.AvatarURL = "/api/manage/users/" + url.PathEscape(u.Subject) + "/avatar"
		}
		resp.Users = append(resp.Users, item)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// SetRunnerUsersRequest is the body of PUT /api/manage/runners/{node}/users.
type SetRunnerUsersRequest struct {
	Users []string `json:"users"`
}

// handleSetRunnerUsers handles PUT /api/manage/runners/{node}/users: it
// replaces the set of users the runner is assigned to (admin only).
func (s *Server) handleSetRunnerUsers(w http.ResponseWriter, r *http.Request) {
	if s.access == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	actor := requestSubject(r)
	if actor == "" || !auth.HasRole(r.Context(), auth.AdminRole) {
		writeJSONError(w, http.StatusForbidden, "admin role required")
		return
	}
	if err := checkManageOrigin(r); err != nil {
		writeJSONError(w, http.StatusForbidden, err.Error())
		return
	}
	node := r.PathValue("node")
	if !s.runnerKnown(node) {
		writeJSONError(w, http.StatusNotFound, "unknown runner")
		return
	}
	var req SetRunnerUsersRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	for i, sub := range req.Users {
		req.Users[i] = strings.TrimSpace(sub)
		ok, err := s.access.UserExists(req.Users[i])
		if err != nil {
			log.Error().Err(err).Msg("failed to check user")
			writeJSONError(w, http.StatusInternalServerError, "failed to check user")
			return
		}
		if !ok {
			writeJSONError(w, http.StatusBadRequest, "unknown user "+req.Users[i])
			return
		}
	}
	added, removed, err := s.access.SetAssignments(node, req.Users, actor)
	if err != nil {
		log.Error().Err(err).Msg("failed to save assignments")
		writeJSONError(w, http.StatusInternalServerError, "failed to save assignments")
		return
	}
	sort.Strings(added)
	sort.Strings(removed)
	log.Info().Str("actor", actor).Str("runner", node).
		Strs("added", added).Strs("removed", removed).Msg("audit: runner assignments changed")
	w.WriteHeader(http.StatusNoContent)
}
