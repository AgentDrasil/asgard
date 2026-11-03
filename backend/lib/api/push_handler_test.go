package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/config"
)

func createFakeServiceAccountJSON(t *testing.T, dir, filename, projectID string) string {
	t.Helper()
	content := map[string]string{
		"type":         "service_account",
		"project_id":   projectID,
		"client_email": "test@" + projectID + ".iam.gserviceaccount.com",
		"private_key":  "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0fake...\n-----END RSA PRIVATE KEY-----\n",
	}
	data, err := json.Marshal(content)
	require.NoError(t, err)

	filePath := filepath.Join(dir, filename)
	require.NoError(t, os.WriteFile(filePath, data, 0600))
	return filePath
}

func TestPushHandler_IgnoresEnvironmentVariables(t *testing.T) {
	tempDir := t.TempDir()
	envPath1 := createFakeServiceAccountJSON(t, tempDir, "env-fcm-sa.json", "env-fcm-proj")
	envPath2 := createFakeServiceAccountJSON(t, tempDir, "env-google-sa.json", "env-google-proj")

	t.Setenv("FCM_SERVICE_ACCOUNT_FILE", envPath1)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", envPath2)

	// Case 1: cfg is nil. Even though env vars are set, it MUST NOT load anything from env vars.
	path, data, projectID, err := findServiceAccountFile(nil)
	require.Error(t, err)
	assert.Empty(t, path)
	assert.Nil(t, data)
	assert.Empty(t, projectID)

	// Case 2: cfg has empty ServiceAccountFile.
	emptyCfg := &config.FirebaseWebpushWebConfig{
		ServiceAccountFile: "",
	}
	path, data, projectID, err = findServiceAccountFile(emptyCfg)
	require.Error(t, err)
	assert.Empty(t, path)
	assert.Nil(t, data)
	assert.Empty(t, projectID)

	// Case 3: cfg has explicit ServiceAccountFile pointing to a different file.
	cfgPath := createFakeServiceAccountJSON(t, tempDir, "explicit-sa.json", "explicit-proj")
	explicitCfg := &config.FirebaseWebpushWebConfig{
		ServiceAccountFile: cfgPath,
	}
	path, data, projectID, err = findServiceAccountFile(explicitCfg)
	require.NoError(t, err)
	assert.Equal(t, cfgPath, path)
	assert.NotEmpty(t, data)
	assert.Equal(t, "explicit-proj", projectID)
}

func TestPushHandler_FindServiceAccountFile_TableDriven(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	validSA := createFakeServiceAccountJSON(t, tempDir, "valid-sa.json", "my-test-proj")

	invalidJSON := filepath.Join(tempDir, "invalid.json")
	require.NoError(t, os.WriteFile(invalidJSON, []byte("{not-valid-json"), 0600))

	missingProjectID := filepath.Join(tempDir, "missing-proj.json")
	require.NoError(t, os.WriteFile(missingProjectID, []byte(`{"client_email":"test@example.com"}`), 0600))

	tests := []struct {
		name          string
		cfg           *config.FirebaseWebpushWebConfig
		expectErr     bool
		errContains   string
		expectProject string
	}{
		{
			name:        "nil config",
			cfg:         nil,
			expectErr:   true,
			errContains: "service_account_file not configured",
		},
		{
			name: "empty service_account_file",
			cfg: &config.FirebaseWebpushWebConfig{
				ServiceAccountFile: "   ",
			},
			expectErr:   true,
			errContains: "service_account_file not configured",
		},
		{
			name: "non-existent file",
			cfg: &config.FirebaseWebpushWebConfig{
				ServiceAccountFile: filepath.Join(tempDir, "does-not-exist.json"),
			},
			expectErr:   true,
			errContains: "no such file",
		},
		{
			name: "invalid json content",
			cfg: &config.FirebaseWebpushWebConfig{
				ServiceAccountFile: invalidJSON,
			},
			expectErr:   true,
			errContains: "invalid character",
		},
		{
			name: "missing project_id in json",
			cfg: &config.FirebaseWebpushWebConfig{
				ServiceAccountFile: missingProjectID,
			},
			expectErr:   true,
			errContains: "missing project_id",
		},
		{
			name: "valid service account file",
			cfg: &config.FirebaseWebpushWebConfig{
				ServiceAccountFile: validSA,
			},
			expectErr:     false,
			expectProject: "my-test-proj",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path, data, projectID, err := findServiceAccountFile(tt.cfg)
			if tt.expectErr {
				require.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.cfg.ServiceAccountFile, path)
			assert.NotEmpty(t, data)
			assert.Equal(t, tt.expectProject, projectID)
		})
	}
}

func TestCheckPushNotificationSetup(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	validSA := createFakeServiceAccountJSON(t, tempDir, "check-sa.json", "check-proj")

	// Verify CheckPushNotificationSetup doesn't panic in any configuration
	assert.NotPanics(t, func() {
		CheckPushNotificationSetup(nil)
	})

	assert.NotPanics(t, func() {
		CheckPushNotificationSetup(&config.FirebaseWebpushWebConfig{ServiceAccountFile: ""})
	})

	assert.NotPanics(t, func() {
		CheckPushNotificationSetup(&config.FirebaseWebpushWebConfig{ServiceAccountFile: filepath.Join(tempDir, "missing.json")})
	})

	assert.NotPanics(t, func() {
		CheckPushNotificationSetup(&config.FirebaseWebpushWebConfig{ServiceAccountFile: validSA})
	})
}

func TestGetFCMClient(t *testing.T) {
	// Not t.Parallel() because it mutates cachedFcmClient
	resetFCMClientForTest()
	t.Cleanup(resetFCMClientForTest)

	tempDir := t.TempDir()

	// 1. Nil config falls back to direct HTTP client with empty projectID
	client, projectID := getFCMClient(nil)
	require.NotNil(t, client)
	assert.Empty(t, projectID)

	// 2. Empty ServiceAccountFile falls back to direct HTTP client
	client, projectID = getFCMClient(&config.FirebaseWebpushWebConfig{ServiceAccountFile: ""})
	require.NotNil(t, client)
	assert.Empty(t, projectID)

	// 3. Invalid file falls back to direct HTTP client
	client, projectID = getFCMClient(&config.FirebaseWebpushWebConfig{ServiceAccountFile: filepath.Join(tempDir, "missing.json")})
	require.NotNil(t, client)
	assert.Empty(t, projectID)

	// 4. Valid SA JSON with malformed private key falls back gracefully
	saPath := createFakeServiceAccountJSON(t, tempDir, "fcm-client-sa.json", "fcm-proj")
	client, _ = getFCMClient(&config.FirebaseWebpushWebConfig{ServiceAccountFile: saPath})
	require.NotNil(t, client)
}

func TestRegisterPushToken_Handler(t *testing.T) {
	t.Parallel()

	s := &Server{}

	t.Run("valid token registration", func(t *testing.T) {
		t.Cleanup(func() {
			pushTokensMu.Lock()
			delete(pushTokens, "fcm-test-token-12345")
			pushTokensMu.Unlock()
		})
		reqBody := `{"token":"fcm-test-token-12345"}`
		req := httptest.NewRequest(http.MethodPost, "/api/push/register", bytes.NewBufferString(reqBody))
		w := httptest.NewRecorder()

		s.handleRegisterPushToken(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp map[string]string
		require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
		assert.Equal(t, "ok", resp["status"])

		pushTokensMu.Lock()
		_, exists := pushTokens["fcm-test-token-12345"]
		pushTokensMu.Unlock()
		assert.True(t, exists)
	})

	t.Run("empty token returns bad request", func(t *testing.T) {
		t.Parallel()
		reqBody := `{"token":""}`
		req := httptest.NewRequest(http.MethodPost, "/api/push/register", bytes.NewBufferString(reqBody))
		w := httptest.NewRecorder()

		s.handleRegisterPushToken(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("invalid json returns bad request", func(t *testing.T) {
		t.Parallel()
		reqBody := `invalid json`
		req := httptest.NewRequest(http.MethodPost, "/api/push/register", bytes.NewBufferString(reqBody))
		w := httptest.NewRecorder()

		s.handleRegisterPushToken(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}
