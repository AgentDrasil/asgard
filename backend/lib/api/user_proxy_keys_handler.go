package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

// userSubject returns the authenticated subject from r, or "default" if auth is disabled.
func (s *Server) userSubject(r *http.Request) string {
	if r != nil {
		if sub := requestSubject(r); sub != "" {
			return sub
		}
	}
	return "default"
}

// handleGetUserProxyKeys handles GET /api/user/proxy-keys.
func (s *Server) handleGetUserProxyKeys(w http.ResponseWriter, r *http.Request) {
	if !s.requireRepo(w) {
		return
	}
	if s.userProxyKeyRepo == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "user proxy keys repository not initialized")
		return
	}

	sub := s.userSubject(r)
	metas, err := s.userProxyKeyRepo.ListKeyMetadata(r.Context(), sub)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list proxy keys: "+err.Error())
		return
	}
	if metas == nil {
		metas = []dbmodels.UserProxyKeyMeta{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(metas)
}

type saveUserProxyKeyRequest struct {
	KeyName string `json:"key_name"`
	Value   string `json:"value"`
}

// handleSaveUserProxyKey handles POST /api/user/proxy-keys.
func (s *Server) handleSaveUserProxyKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireRepo(w) {
		return
	}
	if s.userProxyKeyRepo == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "user proxy keys repository not initialized")
		return
	}

	var req saveUserProxyKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	req.KeyName = strings.TrimSpace(req.KeyName)
	if err := dbmodels.ValidateKeyName(req.KeyName); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Value = strings.TrimRight(req.Value, "\r\n")
	if req.Value == "" {
		writeJSONError(w, http.StatusBadRequest, "value is required")
		return
	}

	sub := s.userSubject(r)
	if err := s.userProxyKeyRepo.SaveKey(r.Context(), sub, req.KeyName, req.Value); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save proxy key: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
}

// handleDeleteUserProxyKey handles DELETE /api/user/proxy-keys/{key_name}.
func (s *Server) handleDeleteUserProxyKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireRepo(w) {
		return
	}
	if s.userProxyKeyRepo == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "user proxy keys repository not initialized")
		return
	}

	keyName := r.PathValue("key_name")
	if keyName == "" {
		writeJSONError(w, http.StatusBadRequest, "key_name is required")
		return
	}

	sub := s.userSubject(r)
	if err := s.userProxyKeyRepo.DeleteKey(r.Context(), sub, keyName); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to delete proxy key: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
}
