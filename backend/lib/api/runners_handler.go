package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/AgentDrasil/asgard/backend/lib/runnerhub"
	"github.com/AgentDrasil/asgard/pkg/version"
)

// RunnerRegistry is the runner hub view the API needs.
type RunnerRegistry interface {
	Nodes() []runnerhub.NodeInfo
}

// WithRunnerRegistry injects the runner registry served by GET /api/runners.
func WithRunnerRegistry(reg RunnerRegistry) ServerOption {
	return func(s *Server) {
		s.runners = reg
	}
}

// RunnerInfoResponse is one runner in the GET /api/runners response.
type RunnerInfoResponse struct {
	NodeID        string              `json:"node_id"`
	Online        bool                `json:"online"`
	LastSeen      *time.Time          `json:"last_seen,omitempty"`
	Version       string              `json:"version,omitempty"`
	ActiveRuns    int                 `json:"active_runs"`
	SupportedCLIs map[string][]string `json:"supported_clis"`
}

// RunnersResponse is the response body of GET /api/runners.
type RunnersResponse struct {
	BackendVersion string               `json:"backend_version"`
	Runners        []RunnerInfoResponse `json:"runners"`
}

// handleRunners handles GET /api/runners.
func (s *Server) handleRunners(w http.ResponseWriter, r *http.Request) {
	resp := RunnersResponse{BackendVersion: version.Get(), Runners: []RunnerInfoResponse{}}
	if s.runners != nil {
		for _, n := range s.runners.Nodes() {
			info := RunnerInfoResponse{
				NodeID:        n.NodeID,
				Online:        n.Online,
				Version:       n.Version,
				ActiveRuns:    n.ActiveRuns,
				SupportedCLIs: n.SupportedCLIs,
			}
			if info.SupportedCLIs == nil {
				info.SupportedCLIs = map[string][]string{}
			}
			if !n.LastSeen.IsZero() {
				ls := n.LastSeen
				info.LastSeen = &ls
			}
			resp.Runners = append(resp.Runners, info)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
