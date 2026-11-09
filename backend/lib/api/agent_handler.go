package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// AgentInfo holds details about an agent for the frontend UI.
type AgentInfo struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Icon        string   `json:"icon"`
	RunDirs     []string `json:"run_dirs"`
	MainAgent   bool     `json:"main_agent"`
	Models      []string `json:"models"`
	// Worktree is true when a chat with this agent can opt in to running in its
	// own git worktree.
	Worktree bool `json:"worktree,omitempty"`
	// RunnerID is the owning runner of a runner-scoped agent (empty for
	// global ones).
	RunnerID string `json:"runner_id,omitempty"`
}

// handleAgents handles GET /api/agents to list loaded agent metadata.
//
// With ?runner=<id> it lists the agents that runner can run (global plus the
// runner's own); without it, the global agents.
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	list := func() []*agentspec.Agent {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return append([]*agentspec.Agent(nil), s.agents...)
	}()
	if runner := r.URL.Query().Get("runner"); runner != "" {
		var err error
		if list, err = s.agentsFor(runner); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to list agents of runner: "+err.Error())
			return
		}
	}

	rFS, _ := s.runnerOnlyFS(r)

	richAgents := make([]AgentInfo, 0, len(list))
	for _, agent := range list {
		models := make([]string, 0, len(agent.Config.CLI))
		for _, target := range agent.Config.CLI {
			if target.Model != "" && s.conf.IsProviderEnabled(target.CLI) {
				models = append(models, target.Model)
			}
		}

		agentType := agent.Config.Type
		if agentType == "" {
			agentType = "agent"
		}

		richAgents = append(richAgents, AgentInfo{
			ID:          agent.Config.ID,
			Type:        agentType,
			Name:        agent.Config.Name,
			Description: agent.Config.Description,
			Icon:        agent.Config.Icon,
			RunDirs:     s.expandAgentRunDirs(r, agent, rFS),
			MainAgent:   agent.Config.IsMainAgent(),
			Models:      models,
			Worktree:    worktreeOptInError(agent) == "",
			RunnerID:    agent.NodeID,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(richAgents)
}

func (s *Server) expandAgentRunDirs(r *http.Request, agent *agentspec.Agent, defaultFS runnerfs.FS) []string {
	if len(agent.Config.RunDirs) == 0 {
		return nil
	}
	hasGlob := false
	for _, d := range agent.Config.RunDirs {
		if strings.ContainsAny(d, "*?[") {
			hasGlob = true
			break
		}
	}
	if !hasGlob {
		return agent.Config.RunDirs
	}

	fs := defaultFS
	if fs == nil && agent.NodeID != "" && s.runnerClient != nil {
		if !s.bindingEnforced() || s.authorizeRunner(r, agent.NodeID) == nil {
			if cl, err := s.nodeClient(agent.NodeID); err == nil {
				fs = runnerfs.NewRemote(cl, "")
			}
		}
	}

	if fs == nil {
		return agent.Config.RunDirs
	}

	var expanded []string
	for _, d := range agent.Config.RunDirs {
		if strings.ContainsAny(d, "*?[") {
			matches, err := fs.Glob(r.Context(), d)
			if err != nil {
				// Fallback on error: keep configured pattern
				expanded = append(expanded, d)
			} else {
				expanded = append(expanded, matches...)
			}
		} else {
			expanded = append(expanded, d)
		}
	}

	seen := make(map[string]bool)
	unique := make([]string, 0, len(expanded))
	for _, d := range expanded {
		clean := filepath.Clean(d)
		if !seen[clean] {
			seen[clean] = true
			unique = append(unique, clean)
		}
	}
	return unique
}
