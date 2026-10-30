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
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/proxy"
	"github.com/AgentDrasil/asgard/backend/lib/runnerfs"
	"github.com/AgentDrasil/asgard/llms"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/paths"
	"github.com/AgentDrasil/asgard/pkg/version"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

const agentFatherID = "agent_father"

var osRename = os.Rename

const defaultConfigTemplate = `# Asgard Configuration Template
debug: false
port: 8080
host: "127.0.0.1"
db: "sqlite"
# dsn is only used when db is "pg" (PostgreSQL connection string).
# SQLite always lives at ~/asgard/data/data.db.
gemini_api_key: "<your-gemini-api-key>"
gemini_model_for_chat_title: "gemini-2.5-flash"
chat_lang: "English (US)"
doc_lang: "English (US)"
comment_lang: "English (US)"
providers:
  - agy
  - claude
  - opencode
  - simplest
# Credential Injection Proxy (Optional). The standalone proxy config, CA cert
# and key live under ~/asgard/config/ and are not path-configurable.
# proxy:
#   enable: true
#   server:
#     addr: "127.0.0.1:8082"
#   debug:
#     enable: false
#     dump_headers: false
#     dump_body: false
#     max_body_bytes: 4096
#   rules:
#     - host: "api.openai.com"
#       path_prefix: "/v1"
#       header_key: "Authorization"
#       real_secret: "Bearer sk-real-key"
#       dummy_secret: "Bearer dummy-token"
#       env: "OPENAI_API_KEY"
`

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

	hasAgentFather := false
	for _, a := range agents {
		if a.Config.ID == agentFatherID {
			hasAgentFather = true
			break
		}
	}

	if !hasAgentFather {
		return errors.New("agent_father is required as the initial root agent, but was not found in the loaded agent definitions")
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

	if s.diagnostics != nil {
		s.validateModelContextWindows(agents)
		s.validateWorkflows(agents)
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

// validateWorkflows validates workflow definitions and checks model pairing coverage,
// recording any errors or warnings in diagnostics.
func (s *Server) validateWorkflows(agents []*agentspec.Agent) {
	s.diagnostics.ResetSource("workflow_validation")
	wfAgents := agentspec.AgentsToWorkflowAgentInfos(agents)

	for _, a := range agents {
		if a == nil || a.Config.Type != "workflow" || a.WorkflowPath == "" {
			continue
		}
		defn, err := workflowspec.LoadDefinition(a.WorkflowPath)
		if err != nil {
			log.Error().Err(err).Str("agent", a.Config.ID).Msg("Failed to load workflow definition during validation")
			s.diagnostics.AddError("workflow_validation", fmt.Sprintf("Workflow %q definition error: %v", a.Config.ID, err))
			continue
		}

		if err := defn.ValidateScheduleRunner(a.NodeID); err != nil {
			s.diagnostics.AddError("workflow_validation", fmt.Sprintf("Workflow %q: %v", a.Config.ID, err))
		}
		if err := defn.ValidateWithAgents(wfAgents); err != nil {
			log.Error().Err(err).Str("agent", a.Config.ID).Msg("Workflow validation failed")
			s.diagnostics.AddError("workflow_validation", fmt.Sprintf("Workflow %q: %v", a.Config.ID, err))
		}
		for _, warning := range defn.ModelPairingWarnings() {
			s.diagnostics.AddWarning("workflow_validation", fmt.Sprintf("Workflow %q: %s", a.Config.ID, warning))
		}
	}
}

// validateModelContextWindows checks configured models against the context window registry,
// recording warnings in diagnostics for any uncataloged models without failing reload.
func (s *Server) validateModelContextWindows(agents []*agentspec.Agent) {
	s.diagnostics.ResetSource("model_validation")
	seenModels := make(map[string]bool)
	for _, a := range agents {
		if a == nil {
			continue
		}
		for _, target := range a.Config.CLI {
			if target.Model == "" || !s.conf.IsProviderEnabled(target.CLI) {
				continue
			}
			// Deduplicate warnings by CLI and model pair to avoid spamming duplicate warnings across agents.
			modelKey := target.CLI + ":" + target.Model
			if seenModels[modelKey] {
				continue
			}
			seenModels[modelKey] = true

			// CLI-aware lookup: handles variant suffixes (e.g. agy
			// "gemini-3.7-flash-low", simplest "deepseek/deepseek-v4-pro/high")
			// and simplest user-config models.
			if !agentwrapper.IsKnownModel(target.CLI, target.Model) {
				log.Error().
					Str("agent", a.Config.ID).
					Str("cli", target.CLI).
					Str("model", target.Model).
					Msg("Configured model is not in known context window registry, falling back to 1M default context window")
				s.diagnostics.AddWarning(
					"model_validation",
					fmt.Sprintf("Agent %q uses uncataloged model %q; falling back to 1M default context window", a.Config.ID, target.Model),
				)
			}
		}
	}
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
	snap.Version = version.Get()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(snap)
}

// handleSystemLogs handles GET /api/system/logs.
func (s *Server) handleSystemLogs(w http.ResponseWriter, r *http.Request) {
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

// handleReload handles POST /api/manage/reload.
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if err := checkManageOrigin(r); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if err := s.reload(); err != nil {
		log.Error().Err(err).Msg("Failed to reload agents")
		if s.diagnostics != nil {
			s.diagnostics.ResetSource("agent_load")
			s.diagnostics.AddError("agent_load", err.Error())
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if s.proxyManager != nil {
		reloadedCfg, err := s.proxyManager.ReloadFromFile()
		if err != nil {
			log.Warn().Err(err).Msg("failed to cascade reload proxy config")
			if s.diagnostics != nil {
				s.diagnostics.AddError("proxy", err.Error())
			}
		} else if reloadedCfg != nil && s.conf != nil {
			if s.diagnostics != nil {
				s.diagnostics.ResetSource("proxy")
			}
			s.conf.SetProxy(reloadedCfg)
			log.Info().
				Bool("enable", reloadedCfg.Enable).
				Int("rules_count", len(reloadedCfg.Rules)).
				Strs("env_vars", reloadedCfg.EnvNames()).
				Msg("cascaded proxy config reload to server config")
		}
	} else {
		log.Debug().Msg("proxy manager not initialized at startup, skipping proxy reload cascade")
	}

	if s.diagnostics != nil {
		s.diagnostics.ResetSource("agent_load")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success", "message": "agents reloaded"})
}

// ConfigRawResponse represents the raw configuration file response.
type ConfigRawResponse struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Exists  bool   `json:"exists"`
}

// SaveConfigRawRequest represents the request body for saving configuration.
type SaveConfigRawRequest struct {
	Content string `json:"content"`
}

// handleGetConfigRaw handles GET /api/manage/config.
func (s *Server) handleGetConfigRaw(w http.ResponseWriter, r *http.Request) {
	if err := checkManageOrigin(r); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	cfgPath := s.configPath
	if cfgPath == "" {
		cfgPath = paths.ConfigFile()
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(ConfigRawResponse{
				Path:    cfgPath,
				Content: defaultConfigTemplate,
				Exists:  false,
			})
			return
		}
		log.Error().Err(err).Str("path", cfgPath).Msg("failed to read raw config")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(ConfigRawResponse{
		Path:    cfgPath,
		Content: string(data),
		Exists:  true,
	})
}

func writeStatusOK(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success", "message": msg})
}

// handleSaveConfigRaw handles PUT /api/manage/config.
func (s *Server) handleSaveConfigRaw(w http.ResponseWriter, r *http.Request) {
	if err := checkManageOrigin(r); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Limit request body size to 1MB
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req SaveConfigRawRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body"})
		return
	}

	// Validate configuration
	if _, err := config.ParseAndValidate([]byte(req.Content)); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("invalid configuration: %v", err)})
		return
	}

	cfgPath := s.configPath
	if cfgPath == "" {
		cfgPath = paths.ConfigFile()
	}

	dir := filepath.Dir(cfgPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Error().Err(err).Str("dir", dir).Msg("failed to create config directory")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// 1. Try atomic rename via temp file
	tmpFile, err := os.CreateTemp(dir, "config-*.tmp")
	if err == nil {
		tmpPath := tmpFile.Name()
		_, writeErr := tmpFile.Write([]byte(req.Content))
		syncErr := tmpFile.Sync()
		closeErr := tmpFile.Close()

		if writeErr == nil && syncErr == nil && closeErr == nil {
			renameErr := osRename(tmpPath, cfgPath)
			if renameErr == nil {
				// Atomic write succeeded
				writeStatusOK(w, "config saved")
				return
			}

			// If rename fails due to Docker bind-mount (EBUSY or EXDEV), fallback to direct truncate write
			if errors.Is(renameErr, syscall.EBUSY) || errors.Is(renameErr, syscall.EXDEV) {
				_ = os.Remove(tmpPath)
				if writeErr := writeConfigDirect(cfgPath, req.Content, 0644); writeErr != nil {
					log.Error().Err(writeErr).Str("path", cfgPath).Msg("failed to write config via direct truncate fallback")
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": writeErr.Error()})
					return
				}
				writeStatusOK(w, "config saved")
				return
			}

			_ = os.Remove(tmpPath)
			log.Error().Err(renameErr).Str("path", cfgPath).Msg("failed to atomic rename config file")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": renameErr.Error()})
			return
		}

		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}

	// If creating temp file failed, fallback to direct truncate
	if writeErr := writeConfigDirect(cfgPath, req.Content, 0644); writeErr != nil {
		log.Error().Err(writeErr).Str("path", cfgPath).Msg("failed to write config directly")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": writeErr.Error()})
		return
	}

	writeStatusOK(w, "config saved")
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

// handleRestart handles POST /api/manage/restart.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if err := checkManageOrigin(r); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "success", "message": "server restart initiated"})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	if s.restartTrigger != nil {
		time.AfterFunc(300*time.Millisecond, s.restartTrigger)
	}
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
// Without runners configured (development) the backend host is queried
// directly, as before.
func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	if s.runnerClient == nil {
		res, err := agentwrapper.GetQuota(r.Context())
		if err != nil {
			log.Error().Err(err).Msg("Failed to fetch quota info")
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
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
	return runnerfs.QueryQuota(ctx, client)
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
		ProxyEnvs:          s.conf.ProxyEnvNames(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// ManageProxyResponse represents the response body for GET /api/manage/proxy.
type ManageProxyResponse struct {
	Enabled     bool         `json:"enabled"`
	Addr        string       `json:"addr,omitempty"`
	CACert      string       `json:"ca_cert,omitempty"`
	ProxyConfig string       `json:"proxy_config,omitempty"`
	Exists      bool         `json:"exists"`
	RawContent  string       `json:"raw_content,omitempty"`
	Rules       []proxy.Rule `json:"rules,omitempty"`
}

// SaveManageProxyRequest represents the request body for PUT /api/manage/proxy.
type SaveManageProxyRequest struct {
	Content string `json:"content"`
}

// handleGetManageProxy handles GET /api/manage/proxy.
func (s *Server) handleGetManageProxy(w http.ResponseWriter, r *http.Request) {
	if err := checkManageOrigin(r); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if s.proxyManager == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(ManageProxyResponse{
			Enabled: false,
			Exists:  false,
		})
		return
	}

	resp := ManageProxyResponse{
		Enabled:     true,
		Addr:        s.proxyManager.Addr(),
		CACert:      s.proxyManager.CACertPath(),
		ProxyConfig: s.proxyManager.ConfigPath(),
		Rules:       s.proxyManager.GetMaskedRules(),
	}

	if resp.ProxyConfig != "" {
		data, err := os.ReadFile(resp.ProxyConfig)
		if err == nil {
			resp.Exists = true
			resp.RawContent = string(data)
		} else {
			resp.Exists = false
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// handleSaveManageProxy handles PUT /api/manage/proxy.
func (s *Server) handleSaveManageProxy(w http.ResponseWriter, r *http.Request) {
	if err := checkManageOrigin(r); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if s.proxyManager == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "proxy service is not enabled"})
		return
	}

	if s.conf == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "no configuration loaded"})
		return
	}

	targetPath := s.conf.ResolvedProxyConfigPath()
	if targetPath == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "cannot resolve proxy_config path"})
		return
	}

	// Limit request body size to 1MB
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var req SaveManageProxyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body"})
		return
	}

	// Validate YAML syntax and proxy configuration
	newCfg, err := proxy.ParseConfig([]byte(req.Content))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("invalid proxy configuration: %v", err)})
		return
	}

	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Error().Err(err).Str("dir", dir).Msg("failed to create proxy config directory")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Atomic write via temp file with fallback
	tmpFile, err := os.CreateTemp(dir, "proxy-*.tmp")
	if err == nil {
		tmpPath := tmpFile.Name()
		_, writeErr := tmpFile.Write([]byte(req.Content))
		syncErr := tmpFile.Sync()
		closeErr := tmpFile.Close()

		if writeErr == nil && syncErr == nil && closeErr == nil {
			renameErr := osRename(tmpPath, targetPath)
			if renameErr == nil {
				s.finishProxySave(w, newCfg)
				return
			}

			if errors.Is(renameErr, syscall.EBUSY) || errors.Is(renameErr, syscall.EXDEV) {
				_ = os.Remove(tmpPath)
				if writeErr := writeConfigDirect(targetPath, req.Content, 0600); writeErr != nil {
					log.Error().Err(writeErr).Str("path", targetPath).Msg("failed to write proxy config via direct fallback")
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": writeErr.Error()})
					return
				}
				s.finishProxySave(w, newCfg)
				return
			}

			_ = os.Remove(tmpPath)
			log.Error().Err(renameErr).Str("path", targetPath).Msg("failed to atomic rename proxy config file")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": renameErr.Error()})
			return
		}

		_ = os.Remove(tmpPath)
	}

	if writeErr := writeConfigDirect(targetPath, req.Content, 0600); writeErr != nil {
		log.Error().Err(writeErr).Str("path", targetPath).Msg("failed to write proxy config directly")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": writeErr.Error()})
		return
	}

	s.finishProxySave(w, newCfg)
}

// finishProxySave hot-reloads the proxy rules after a successful config save
// and writes the success response.
func (s *Server) finishProxySave(w http.ResponseWriter, newCfg *proxy.Config) {
	if err := s.proxyManager.Reload(newCfg); err != nil {
		log.Warn().Err(err).Msg("failed to reload proxy rules after saving")
	}
	s.conf.SetProxy(newCfg)
	writeStatusOK(w, "proxy config saved and reloaded")
}

// handleReloadManageProxy handles POST /api/manage/proxy/reload.
func (s *Server) handleReloadManageProxy(w http.ResponseWriter, r *http.Request) {
	if err := checkManageOrigin(r); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	if s.proxyManager == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "proxy service is not enabled"})
		return
	}

	if s.conf == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "no configuration loaded"})
		return
	}

	newCfg, err := s.proxyManager.ReloadFromFile()
	if err != nil {
		log.Error().Err(err).Msg("failed to reload proxy rules from file")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("failed to reload proxy rules: %v", err)})
		return
	}
	s.conf.SetProxy(newCfg)

	writeStatusOK(w, "proxy config reloaded")
}

// cronAgents extends the global agents with the agents owned by the allowed
// runners, so their scheduled workflows run too.
func (s *Server) cronAgents(global []*agentspec.Agent) []*agentspec.Agent {
	src, ok := s.agentSource.(agentsForRunner)
	if !ok || s.conf == nil {
		return global
	}
	all := append([]*agentspec.Agent(nil), global...)
	for _, node := range s.conf.AllowedRunnerNodes() {
		list, err := src.AgentsFor(node)
		if err != nil {
			log.Warn().Err(err).Str("runner", node).Msg("runner agents unavailable for cron scheduling")
			continue
		}
		for _, a := range list {
			if a.NodeID != "" {
				all = append(all, a)
			}
		}
	}
	return all
}

func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(v)
}
