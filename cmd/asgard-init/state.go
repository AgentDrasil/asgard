package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const stateFileName = ".asgard-init-state.json"

// InitState stores the in-progress wizard responses to support resuming interrupted runs.
type InitState struct {
	TargetDir        string          `json:"target_dir,omitempty"`
	Host             string          `json:"host,omitempty"`
	BackendPort      int             `json:"backend_port,omitempty"`
	EtcdEndpoints    string          `json:"etcd_endpoints,omitempty"`
	EtcdPrefix       string          `json:"etcd_prefix,omitempty"`
	AuthIssuer       string          `json:"auth_issuer,omitempty"`
	AuthClientID     string          `json:"auth_client_id,omitempty"`
	AuthClientSecret string          `json:"auth_client_secret,omitempty"`
	InitialRunner    string          `json:"initial_runner,omitempty"`
	WorkspaceMounts  []string        `json:"workspace_mounts,omitempty"`
	GCPKMS           string          `json:"gcp_kms,omitempty"`
	GCPSAKey         string          `json:"gcp_sa_key,omitempty"`
	AgeRecipient     string          `json:"age_recipient,omitempty"`
	Providers        []string        `json:"providers,omitempty"`
	CompletedSteps   map[string]bool `json:"completed_steps,omitempty"`
}

func newInitState() *InitState {
	return &InitState{
		CompletedSteps: make(map[string]bool),
	}
}

func (s *InitState) MarkCompleted(step string) {
	if s.CompletedSteps == nil {
		s.CompletedSteps = make(map[string]bool)
	}
	s.CompletedSteps[step] = true
}

func (s *InitState) IsCompleted(step string) bool {
	if s.CompletedSteps == nil {
		return false
	}
	return s.CompletedSteps[step]
}

// Save writes the state to targetDir/.asgard-init-state.json (or cwd if targetDir is empty).
func (s *InitState) Save(targetDir string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal init state: %w", err)
	}

	var statePath string
	if targetDir != "" {
		statePath = filepath.Join(targetDir, stateFileName)
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
		statePath = filepath.Join(cwd, stateFileName)
	}

	if err := os.MkdirAll(filepath.Dir(statePath), 0755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	if err := os.WriteFile(statePath, data, 0600); err != nil {
		return fmt.Errorf("write state file %s: %w", statePath, err)
	}
	return nil
}

// loadState attempts to read an existing state file from targetDir or cwd.
func loadState(targetDir string) (string, *InitState, error) {
	candidates := make([]string, 0, 2)
	if targetDir != "" {
		candidates = append(candidates, filepath.Join(targetDir, stateFileName))
	}
	cwd, err := os.Getwd()
	if err == nil {
		cwdPath := filepath.Join(cwd, stateFileName)
		if len(candidates) == 0 || candidates[0] != cwdPath {
			candidates = append(candidates, cwdPath)
		}
	}

	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err == nil && len(data) > 0 {
			var state InitState
			if jsonErr := json.Unmarshal(data, &state); jsonErr == nil {
				if state.CompletedSteps == nil {
					state.CompletedSteps = make(map[string]bool)
				}
				return p, &state, nil
			}
		}
	}
	return "", nil, os.ErrNotExist
}

// removeState removes state files from targetDir and cwd.
func removeState(targetDir string) {
	if targetDir != "" {
		_ = os.Remove(filepath.Join(targetDir, stateFileName))
	}
	cwd, err := os.Getwd()
	if err == nil {
		_ = os.Remove(filepath.Join(cwd, stateFileName))
	}
}

func resolvePath(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func validateJSONFile(path string) error {
	resolved := resolvePath(path)
	data, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Errorf("file %q cannot be read: %w", path, err)
	}
	var sa struct {
		Type      string `json:"type"`
		ProjectID string `json:"project_id"`
	}
	if err := json.Unmarshal(data, &sa); err != nil {
		return fmt.Errorf("file %q is not valid JSON: %w", path, err)
	}
	if sa.ProjectID == "" {
		return fmt.Errorf("file %q is not a valid service account JSON: missing project_id", path)
	}
	return nil
}
