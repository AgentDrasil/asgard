package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/auth"
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

// ManageRunnerResponse is one runner in GET /api/manage/runners.
type ManageRunnerResponse struct {
	NodeID string   `json:"node_id"`
	Online bool     `json:"online"`
	Users  []string `json:"users"`
}

// ManageUserResponse is one known user in GET /api/manage/runners.
type ManageUserResponse struct {
	Subject string `json:"subject"`
	Name    string `json:"name,omitempty"`
	Email   string `json:"email,omitempty"`
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
		resp.Users = append(resp.Users, ManageUserResponse{Subject: u.Subject, Name: u.Name, Email: u.Email})
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
