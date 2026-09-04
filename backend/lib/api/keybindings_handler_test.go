package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/backend/lib/config"
	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/dbmodels"
)

func setupTestServerWithConfig(t *testing.T, cfgFilePath string) *Server {
	t.Helper()

	mockClients := map[string]types.CLIClient{
		"agy": &mockClient{models: []string{"test-model"}},
	}
	agentwrapper.SetClients(mockClients)
	t.Cleanup(func() {
		agentwrapper.SetClients(nil)
	})

	tmpDir := filepath.Dir(cfgFilePath)
	fatherDir := filepath.Join(tmpDir, "agents", "agent_father")
	require.NoError(t, os.MkdirAll(fatherDir, 0755))

	fatherYaml := `
id: "agent_father"
name: "Agent Father"
description: "Root agent"
run_dirs: ["/tmp"]
cli:
  - cli: "agy"
    model: "test-model"
`
	require.NoError(t, os.WriteFile(filepath.Join(fatherDir, "config.yaml"), []byte(fatherYaml), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "teams.yaml"), []byte("teams:\n  - my-team\n"), 0644))

	conf := &config.Config{
		AgentDir: tmpDir,
		Port:     8080,
	}

	testDB := db.NewDBForTest(t)
	require.NoError(t, dbmodels.AutoMigrate(testDB))
	srv, err := New(conf, testDB, WithConfigPath(cfgFilePath))
	require.NoError(t, err)
	return srv
}

func TestKeybindings_GetEmptyFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cfgFilePath := filepath.Join(tmpDir, "config.yaml")
	srv := setupTestServerWithConfig(t, cfgFilePath)

	req := httptest.NewRequest(http.MethodGet, "/api/keybindings", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp KeybindingsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.False(t, resp.Exists)
	assert.NotNil(t, resp.Overrides)
	assert.Empty(t, resp.Overrides)
}

func TestKeybindings_GetCorruptedYaml(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cfgFilePath := filepath.Join(tmpDir, "config.yaml")
	keysFilePath := filepath.Join(tmpDir, "keys.yaml")
	require.NoError(t, os.WriteFile(keysFilePath, []byte("invalid_yaml: [ unclosed"), 0644))

	srv := setupTestServerWithConfig(t, cfgFilePath)

	req := httptest.NewRequest(http.MethodGet, "/api/keybindings", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "failed to parse keys.yaml")
}

func TestKeybindings_SaveCorruptedYamlRejects(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cfgFilePath := filepath.Join(tmpDir, "config.yaml")
	keysFilePath := filepath.Join(tmpDir, "keys.yaml")
	require.NoError(t, os.WriteFile(keysFilePath, []byte("corrupted: [ unclosed"), 0644))

	srv := setupTestServerWithConfig(t, cfgFilePath)

	payload := SaveKeybindingsRequest{
		Overrides: KeybindingsOverrides{
			"linux": {
				"command_palette": "Ctrl+P",
			},
		},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, "/api/manage/keybindings", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "existing keys.yaml is corrupted")

	// Ensure corrupted content was untouched
	data, readErr := os.ReadFile(keysFilePath)
	require.NoError(t, readErr)
	assert.Equal(t, "corrupted: [ unclosed", string(data))
}

func TestKeybindings_SaveSegmentMerging(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cfgFilePath := filepath.Join(tmpDir, "config.yaml")
	keysFilePath := filepath.Join(tmpDir, "keys.yaml")

	initialYaml := `mac:
  command_palette: Cmd+P
windows:
  toggle_terminal: Ctrl+Backquote
`
	require.NoError(t, os.WriteFile(keysFilePath, []byte(initialYaml), 0644))

	srv := setupTestServerWithConfig(t, cfgFilePath)

	// Save linux overrides only
	payload := SaveKeybindingsRequest{
		Overrides: KeybindingsOverrides{
			"linux": {
				"toggle_sidebar": "Ctrl+B",
			},
		},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, "/api/manage/keybindings", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// Verify merged content on disk
	data, err := os.ReadFile(keysFilePath)
	require.NoError(t, err)

	var saved KeybindingsOverrides
	require.NoError(t, yaml.Unmarshal(data, &saved))

	assert.Contains(t, saved, "mac")
	assert.Equal(t, "Cmd+P", saved["mac"]["command_palette"])
	assert.Contains(t, saved, "windows")
	assert.Equal(t, "Ctrl+Backquote", saved["windows"]["toggle_terminal"])
	assert.Contains(t, saved, "linux")
	assert.Equal(t, "Ctrl+B", saved["linux"]["toggle_sidebar"])
}

func TestKeybindings_SaveEmptyArrayUnassigned(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cfgFilePath := filepath.Join(tmpDir, "config.yaml")

	srv := setupTestServerWithConfig(t, cfgFilePath)

	// Payload with [] unassigned binding and []string multi-binding
	payload := SaveKeybindingsRequest{
		Overrides: KeybindingsOverrides{
			"linux": {
				"command_palette": []any{},
				"toggle_terminal": []any{"Ctrl+Backquote", "F1"},
			},
		},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, "/api/manage/keybindings", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// GET back and verify
	getReq := httptest.NewRequest(http.MethodGet, "/api/keybindings", nil)
	getW := httptest.NewRecorder()
	srv.ServeHTTP(getW, getReq)

	assert.Equal(t, http.StatusOK, getW.Code)
	var resp KeybindingsResponse
	require.NoError(t, json.Unmarshal(getW.Body.Bytes(), &resp))
	assert.True(t, resp.Exists)
	assert.NotNil(t, resp.Overrides["linux"])

	// Check unassigned []
	cmdPaletteVal, ok := resp.Overrides["linux"]["command_palette"].([]any)
	require.True(t, ok, "expected []any for command_palette")
	assert.Empty(t, cmdPaletteVal)

	// Check multi-binding
	toggleTermVal, ok := resp.Overrides["linux"]["toggle_terminal"].([]any)
	require.True(t, ok, "expected []any for toggle_terminal")
	assert.Len(t, toggleTermVal, 2)
	assert.Equal(t, "Ctrl+Backquote", toggleTermVal[0])
	assert.Equal(t, "F1", toggleTermVal[1])
}

func TestKeybindings_SaveTokenValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		payload        SaveKeybindingsRequest
		expectedStatus int
		expectError    string
	}{
		{
			name: "valid normalized aliases",
			payload: SaveKeybindingsRequest{
				Overrides: KeybindingsOverrides{
					"mac": {
						"toggle_sidebar":  "⌘+⌥+B",
						"toggle_terminal": "Ctrl+`",
					},
				},
			},
			expectedStatus: http.StatusOK,
		},
		{
			name: "invalid OS",
			payload: SaveKeybindingsRequest{
				Overrides: KeybindingsOverrides{
					"android": {
						"command_palette": "Ctrl+P",
					},
				},
			},
			expectedStatus: http.StatusBadRequest,
			expectError:    "invalid OS",
		},
		{
			name: "exceeds 64 chars",
			payload: SaveKeybindingsRequest{
				Overrides: KeybindingsOverrides{
					"linux": {
						"command_palette": strings.Repeat("Ctrl+", 15) + "P",
					},
				},
			},
			expectedStatus: http.StatusBadRequest,
			expectError:    "exceeds maximum length of 64 characters",
		},
		{
			name: "invalid token character",
			payload: SaveKeybindingsRequest{
				Overrides: KeybindingsOverrides{
					"linux": {
						"command_palette": "Ctrl+InvalidKeyName123",
					},
				},
			},
			expectedStatus: http.StatusBadRequest,
			expectError:    "unrecognized key token",
		},
		{
			name: "missing base key",
			payload: SaveKeybindingsRequest{
				Overrides: KeybindingsOverrides{
					"linux": {
						"command_palette": "Ctrl+Alt",
					},
				},
			},
			expectedStatus: http.StatusBadRequest,
			expectError:    "no base key found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			cfgFilePath := filepath.Join(tmpDir, "config.yaml")
			srv := setupTestServerWithConfig(t, cfgFilePath)

			body, err := json.Marshal(tt.payload)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPut, "/api/manage/keybindings", strings.NewReader(string(body)))
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code)
			if tt.expectError != "" {
				assert.Contains(t, w.Body.String(), tt.expectError)
			}
			if tt.expectedStatus == http.StatusOK && tt.name == "valid normalized aliases" {
				data, err := os.ReadFile(filepath.Join(tmpDir, "keys.yaml"))
				require.NoError(t, err)
				var saved KeybindingsOverrides
				require.NoError(t, yaml.Unmarshal(data, &saved))
				assert.Equal(t, "Cmd+Alt+B", saved["mac"]["toggle_sidebar"])
				assert.Equal(t, "Ctrl+Backquote", saved["mac"]["toggle_terminal"])
			}
		})
	}
}

func TestKeybindings_SaveInvalidOrigin(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cfgFilePath := filepath.Join(tmpDir, "config.yaml")
	srv := setupTestServerWithConfig(t, cfgFilePath)

	payload := SaveKeybindingsRequest{
		Overrides: KeybindingsOverrides{
			"linux": {
				"command_palette": "Ctrl+P",
			},
		},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, "/api/manage/keybindings", strings.NewReader(string(body)))
	req.Host = "192.168.1.100:8080"
	req.RemoteAddr = "192.168.1.50:12345"
	req.Header.Set("Origin", "http://attacker.com")

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "cross-origin manage request rejected")
}

func TestKeybindings_SaveDirectWriteFallback(t *testing.T) {
	tests := []struct {
		name           string
		renameErr      error
		expectedStatus int
		expectSaved    bool
	}{
		{
			name:           "fallback on EXDEV",
			renameErr:      syscall.EXDEV,
			expectedStatus: http.StatusOK,
			expectSaved:    true,
		},
		{
			name:           "fallback on EBUSY",
			renameErr:      syscall.EBUSY,
			expectedStatus: http.StatusOK,
			expectSaved:    true,
		},
		{
			name:           "error on unexpected rename failure",
			renameErr:      syscall.EPERM,
			expectedStatus: http.StatusInternalServerError,
			expectSaved:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			cfgFilePath := filepath.Join(tmpDir, "config.yaml")
			keysFilePath := filepath.Join(tmpDir, "keys.yaml")
			srv := setupTestServerWithConfig(t, cfgFilePath)

			t.Cleanup(func() { osRename = os.Rename })
			osRename = func(_, _ string) error {
				return tc.renameErr
			}

			payload := SaveKeybindingsRequest{
				Overrides: KeybindingsOverrides{
					"linux": {
						"command_palette": "Ctrl+P",
					},
				},
			}
			body, err := json.Marshal(payload)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPut, "/api/manage/keybindings", strings.NewReader(string(body)))
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)

			assert.Equal(t, tc.expectedStatus, w.Code)
			if tc.expectSaved {
				saved, err := os.ReadFile(keysFilePath)
				require.NoError(t, err)
				assert.Contains(t, string(saved), "Ctrl+P")
			}

			// Ensure temp files cleaned up
			tmpMatches, err := filepath.Glob(filepath.Join(tmpDir, "keys-*.tmp"))
			require.NoError(t, err)
			assert.Empty(t, tmpMatches)
		})
	}
}
