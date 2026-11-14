package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/backend/lib/auth"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/llms"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/version"
)

var osRename = os.Rename

func checkManageOrigin(r *http.Request) error {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return nil
	}
	u, err := url.Parse(origin)
	if err != nil {
		return errors.New("invalid origin header")
	}
	if u.Host == r.Host {
		return nil
	}
	if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
		firstFwd := strings.TrimSpace(strings.Split(fwdHost, ",")[0])
		if u.Host == firstFwd {
			return nil
		}
	}
	originHost := u.Hostname()
	reqHost, _, _ := net.SplitHostPort(r.Host)
	if reqHost == "" {
		reqHost = r.Host
	}
	remoteHost, _, _ := net.SplitHostPort(r.RemoteAddr)
	if remoteHost == "" {
		remoteHost = r.RemoteAddr
	}
	isLoopback := func(h string) bool {
		return h == "localhost" || h == "127.0.0.1" || h == "::1"
	}
	if isLoopback(originHost) && (isLoopback(reqHost) || isLoopback(remoteHost)) {
		return nil
	}
	return errors.New("cross-origin manage request rejected")
}

// Reload reloads the agent configurations and refreshes the HTTP handlers.
func (s *Server) reload() error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	if s.agentSource == nil {
		return errors.New("no agent source configured")
	}
	agents, err := s.agentSource.LoadAll()
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.agents = agents
	if s.workflowEngine != nil {
		s.workflowEngine.SetAgents(agents)
	}
	s.mux = s.buildMuxLocked()
	s.mu.Unlock()

	// Only now is the new list being served, so only now may the source stop
	// protecting the previous one.
	if acc, ok := s.agentSource.(interface{ Accept([]*agentspec.Agent) }); ok {
		acc.Accept(agents)
	}

	if s.cronManager != nil {
		s.cronManager.Reload(s.cronAgents(agents))
	}

	return nil
}

// ReloadAgents reloads agents from the agent source (for example on an etcd
// watch event) and records the outcome in diagnostics.
func (s *Server) ReloadAgents() error {
	err := s.reload()
	if s.diagnostics != nil {
		s.diagnostics.ResetSource("agent_load")
		if err != nil {
			s.diagnostics.AddError("agent_load", err.Error())
		}
	}
	if err != nil {
		log.Error().Err(err).Msg("Failed to reload agents")
	}
	return err
}

// handleSystemStatus handles GET /api/system/status.
func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	var snap DiagnosticsSnapshot
	if s.diagnostics != nil {
		snap = s.diagnostics.Snapshot()
	} else {
		snap = DiagnosticsSnapshot{
			Status:   "ok",
			Errors:   []string{},
			Warnings: []string{},
		}
	}
	// Redact internal errors and warnings for non-admin users when auth is enabled.
	if s.auth != nil && !auth.HasRole(r.Context(), auth.AdminRole) {
		snap.Errors = []string{}
		snap.Warnings = []string{}
	}
	snap.Version = version.Get()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(snap)
}

// handleSystemLogs handles GET /api/system/logs.
func (s *Server) handleSystemLogs(w http.ResponseWriter, r *http.Request) {
	if s.auth != nil && !auth.HasRole(r.Context(), auth.AdminRole) {
		writeJSONError(w, http.StatusForbidden, "admin role required")
		return
	}
	level := r.URL.Query().Get("level")
	var logs []LogEntry
	if s.diagnostics != nil {
		logs = s.diagnostics.GetLogs(level)
	} else {
		logs = []LogEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(SystemLogsResponse{Logs: logs})
}

// handleClearSystemLogs handles DELETE /api/system/logs.
func (s *Server) handleClearSystemLogs(w http.ResponseWriter, r *http.Request) {
	if s.auth != nil && !auth.HasRole(r.Context(), auth.AdminRole) {
		writeJSONError(w, http.StatusForbidden, "admin role required")
		return
	}
	s.diagnostics.Clear()
	writeStatusOK(w, "system logs cleared")
}

func writeStatusOK(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success", "message": msg})
}

func writeConfigDirect(path, content string, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}

	if _, err := f.Write([]byte(content)); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// QuotaAllResponse is the body of GET /api/quota without ?runner= when more
// than one runner is online (or one answered with an error).
type QuotaAllResponse struct {
	// Runners maps an online runner's node id to its quota, keyed by CLI.
	Runners map[string]map[string][]llms.ModelUsage `json:"runners"`
	// Errors maps a runner node id to why its quota is missing.
	Errors map[string]string `json:"errors,omitempty"`
}

// quotaTimeout bounds the quota query of one runner (it contacts providers).
const quotaTimeout = 60 * time.Second

// handleQuota handles GET /api/quota[?runner=<node_id>].
//
// The quota lives on the runners (their CLIs and credentials). Response shape,
// chosen for backward compatibility with the web UI, which expects the former
// flat object {"<cli>": [ModelUsage...]}:
//   - ?runner=<node>: that runner's flat object. 404 for an unknown node, 503
//     when it is offline, 502 when the runner failed to answer.
//   - no parameter, exactly one online runner that answered: the same flat
//     object (the single-runner deployment is unchanged).
//   - no parameter otherwise: QuotaAllResponse, every online runner keyed by
//     node id plus per-node errors. With no runner online it is 503.
//
// A CLI's list may contain status-only entries (empty model, "status" is
// "no_credentials" or "error", "message" holds the text) next to or instead of
// model entries; the UI shows them as notices.
//
// The backend never queries the vendor CLIs itself: without runner support the
// endpoint answers 503.
func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	if s.runnerClient == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "no runner is configured")
		return
	}

	if node := r.URL.Query().Get("runner"); node != "" {
		known := s.runners == nil // without a registry the client lookup decides
		if s.runners != nil {
			for _, n := range s.runners.Nodes() {
				if n.NodeID == node {
					known = true
					break
				}
			}
		}
		if !known {
			writeJSONError(w, http.StatusNotFound, fmt.Sprintf("unknown runner %q", node))
			return
		}
		res, err := s.runnerQuota(r.Context(), node)
		if err != nil {
			status, msg := fsErrorStatus(err)
			if status != http.StatusServiceUnavailable {
				status = http.StatusBadGateway
			}
			writeJSONError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}

	if s.runners == nil {
		// No registry to enumerate (WithRunnerClient only): ask the
		// configured client, which resolves the default runner.
		res, err := s.runnerQuota(r.Context(), "")
		if err != nil {
			status, msg := fsErrorStatus(err)
			if status != http.StatusServiceUnavailable {
				status = http.StatusBadGateway
			}
			writeJSONError(w, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, res)
		return
	}
	var online []string
	if s.runners != nil {
		for _, n := range s.runners.Nodes() {
			if n.Online {
				online = append(online, n.NodeID)
			}
		}
	}
	if len(online) == 0 {
		writeJSONError(w, http.StatusServiceUnavailable, "no runner is online")
		return
	}
	type result struct {
		res map[string][]llms.ModelUsage
		err error
	}
	results := make([]result, len(online))
	var wg sync.WaitGroup
	for i, node := range online {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i].res, results[i].err = s.runnerQuota(r.Context(), node)
		}()
	}
	wg.Wait()
	all := QuotaAllResponse{Runners: map[string]map[string][]llms.ModelUsage{}, Errors: map[string]string{}}
	for i, node := range online {
		if results[i].err != nil {
			log.Warn().Err(results[i].err).Str("runner", node).Msg("Failed to fetch runner quota")
			all.Errors[node] = results[i].err.Error()
			continue
		}
		all.Runners[node] = results[i].res
	}
	if len(online) == 1 && len(all.Errors) == 0 {
		writeJSON(w, http.StatusOK, all.Runners[online[0]])
		return
	}
	if len(all.Errors) == 0 {
		all.Errors = nil
	}
	writeJSON(w, http.StatusOK, all)
}

// runnerQuota queries one runner's quota.
func (s *Server) runnerQuota(ctx context.Context, node string) (map[string][]llms.ModelUsage, error) {
	client, err := s.nodeClient(node)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, quotaTimeout)
	defer cancel()
	res, err := runnerfs.QueryQuotaWithStatus(ctx, client)
	if err != nil {
		return nil, err
	}
	// The runner reports every CLI it knows; only configured providers count.
	for cli := range res {
		if !s.conf.IsProviderEnabled(cli) {
			delete(res, cli)
		}
	}
	return res, nil
}

// ConfigResponse represents the public configuration sent to web clients.
type ConfigResponse struct {
	FirebaseWebpushWeb *config.FirebaseWebpushWebConfig `json:"firebase_webpush_web,omitempty"`
	DefaultUILang      string                           `json:"default_ui_lang,omitempty"`
	ProxyEnvs          []string                         `json:"proxy_envs,omitempty"`
}

// handleConfig handles GET /api/config.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	resp := ConfigResponse{
		FirebaseWebpushWeb: s.conf.FirebaseWebpushWeb,
		DefaultUILang:      s.conf.GetUILang(),
	}
	// The user's own keys; only those with a host rule can be served.
	if s.userProxyKeyRepo != nil {
		if metas, err := s.userProxyKeyRepo.ListKeyMetadata(r.Context(), s.userSubject(r)); err == nil {
			for _, m := range metas {
				if m.Host != "" {
					resp.ProxyEnvs = append(resp.ProxyEnvs, m.KeyName)
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// cronAgents returns the agents eligible for backend cron scheduling (global
// agents; runner-bound agents are scheduled and triggered on their own runners).
func (s *Server) cronAgents(global []*agentspec.Agent) []*agentspec.Agent {
	return global
}

func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(v)
}
