package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/proxy"
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

const agentFatherID = "agent_father"

var osRename = os.Rename

const defaultConfigTemplate = `# Asgard Configuration Template
debug: false
port: 8080
internal_port: 8081
host: "127.0.0.1"
db: "sqlite"
dsn: "asgard.db"
agent_dir: "./agents"
gemini_api_key: "<your-gemini-api-key>"
gemini_model_for_chat_title: "gemini-2.5-flash"
chat_lang: "English (US)"
doc_lang: "English (US)"
comment_lang: "English (US)"
providers:
  - agy
  - opencode
  - simplest
# Credential Injection Proxy (Optional)
# proxy_config: "proxy.yaml" # Path to standalone proxy configuration file
# proxy:
#   enable: false
#   server:
#     addr: "127.0.0.1:8082"
#     ca_cert: "~/.asgard/ca/ca.crt"
#     ca_key: "~/.asgard/ca/ca.key"
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
	loader := agentspec.NewLoader(s.conf.AgentDir)
	agents, err := loader.LoadAll()
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
		return fmt.Errorf("agent_father is required as the initial root agent, but was not found in the agents directory (%s). You can clone the default agents via: git clone https://github.com/AgentDrasil/asgard-agents.git %s", s.conf.AgentDir, s.conf.AgentDir)
	}

	s.mu.Lock()
	s.agents = agents
	if s.workflowEngine != nil {
		s.workflowEngine.SetAgents(agents)
	}
	s.mux = s.buildMuxLocked()
	s.mu.Unlock()

	if s.diagnostics != nil {
		s.validateModelContextWindows(agents)
	}

	if s.cronManager != nil {
		s.cronManager.Reload(agents)
	}

	return nil
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

			if _, known := agentwrapper.LookupContextWindow(target.Model); !known {
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

	if s.proxyManager != nil && s.conf != nil && s.conf.ProxyConfig != "" {
		if _, err := s.proxyManager.ReloadFromFile(); err != nil {
			log.Warn().Err(err).Msg("failed to cascade reload proxy config")
		}
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
		cfgPath = "config.yaml"
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
		cfgPath = "config.yaml"
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
				if writeErr := writeConfigDirect(cfgPath, req.Content); writeErr != nil {
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
	if writeErr := writeConfigDirect(cfgPath, req.Content); writeErr != nil {
		log.Error().Err(writeErr).Str("path", cfgPath).Msg("failed to write config directly")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": writeErr.Error()})
		return
	}

	writeStatusOK(w, "config saved")
}

func writeConfigDirect(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
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

// handleQuota handles GET /api/quota.
func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	res, err := agentwrapper.GetQuota(r.Context())
	if err != nil {
		log.Error().Err(err).Msg("Failed to fetch quota info")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(res)
}

// ConfigResponse represents the public configuration sent to web clients.
type ConfigResponse struct {
	FirebaseWebpushWeb *config.FirebaseWebpushWebConfig `json:"firebase_webpush_web,omitempty"`
	DefaultUILang      string                           `json:"default_ui_lang,omitempty"`
}

// handleConfig handles GET /api/config.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	resp := ConfigResponse{
		FirebaseWebpushWeb: s.conf.FirebaseWebpushWeb,
		DefaultUILang:      s.conf.GetUILang(),
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

	if s.conf == nil || s.conf.ProxyConfig == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "no standalone proxy_config configured in config.yaml"})
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
				// Hot reload proxy rules
				if err := s.proxyManager.Reload(newCfg); err != nil {
					log.Warn().Err(err).Msg("failed to reload proxy rules after saving")
				}
				writeStatusOK(w, "proxy config saved and reloaded")
				return
			}

			if errors.Is(renameErr, syscall.EBUSY) || errors.Is(renameErr, syscall.EXDEV) {
				_ = os.Remove(tmpPath)
				if writeErr := writeConfigDirect(targetPath, req.Content); writeErr != nil {
					log.Error().Err(writeErr).Str("path", targetPath).Msg("failed to write proxy config via direct fallback")
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": writeErr.Error()})
					return
				}
				if err := s.proxyManager.Reload(newCfg); err != nil {
					log.Warn().Err(err).Msg("failed to reload proxy rules after saving")
				}
				writeStatusOK(w, "proxy config saved and reloaded")
				return
			}

			_ = os.Remove(tmpPath)
			log.Error().Err(renameErr).Str("path", targetPath).Msg("failed to atomic rename proxy config file")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": renameErr.Error()})
			return
		}

		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}

	if writeErr := writeConfigDirect(targetPath, req.Content); writeErr != nil {
		log.Error().Err(writeErr).Str("path", targetPath).Msg("failed to write proxy config directly")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": writeErr.Error()})
		return
	}

	if err := s.proxyManager.Reload(newCfg); err != nil {
		log.Warn().Err(err).Msg("failed to reload proxy rules after saving")
	}

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

	if s.conf == nil || s.conf.ProxyConfig == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "no standalone proxy_config configured in config.yaml"})
		return
	}

	if _, err := s.proxyManager.ReloadFromFile(); err != nil {
		log.Error().Err(err).Msg("failed to reload proxy rules from file")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("failed to reload proxy rules: %v", err)})
		return
	}

	writeStatusOK(w, "proxy config reloaded")
}
