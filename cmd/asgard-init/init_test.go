package main

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTLSGeneration(t *testing.T) {
	t.Parallel()

	caCert, caKey, caCertPEM, caKeyPEM, err := GenerateCA()
	require.NoError(t, err)
	require.NotNil(t, caCert)
	require.NotNil(t, caKey)
	assert.True(t, caCert.IsCA)
	assert.Equal(t, "Asgard Root CA", caCert.Subject.CommonName)

	tests := []struct {
		name       string
		nodeID     string
		dnsNames   []string
		ips        []net.IP
		isServer   bool
		expectedCN string
		wantErr    bool
	}{
		{
			name:       "valid server cert",
			dnsNames:   []string{"backend", "localhost"},
			ips:        []net.IP{net.ParseIP("127.0.0.1")},
			isServer:   true,
			expectedCN: "asgard-backend",
			wantErr:    false,
		},
		{
			name:       "valid client cert runner-1",
			nodeID:     "runner-1",
			isServer:   false,
			expectedCN: "runner-1",
			wantErr:    false,
		},
		{
			name:       "valid client cert runner-a",
			nodeID:     "runner-a",
			isServer:   false,
			expectedCN: "runner-a",
			wantErr:    false,
		},
		{
			name:     "empty client node id fails",
			nodeID:   "",
			isServer: false,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			caPool := x509.NewCertPool()
			require.True(t, caPool.AppendCertsFromPEM(caCertPEM))

			if tt.isServer {
				certPEM, keyPEM, err := GenerateServerCert(caCert, caKey, tt.dnsNames, tt.ips)
				if tt.wantErr {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				require.NotEmpty(t, certPEM)
				require.NotEmpty(t, keyPEM)

				block, _ := pem.Decode(certPEM)
				require.NotNil(t, block)
				parsed, err := x509.ParseCertificate(block.Bytes)
				require.NoError(t, err)
				assert.Equal(t, tt.expectedCN, parsed.Subject.CommonName)

				// Verify against CA
				_, err = parsed.Verify(x509.VerifyOptions{
					Roots:     caPool,
					DNSName:   "backend",
					KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
				})
				require.NoError(t, err)
			} else {
				certPEM, keyPEM, err := GenerateClientCert(caCert, caKey, tt.nodeID)
				if tt.wantErr {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				require.NotEmpty(t, certPEM)
				require.NotEmpty(t, keyPEM)

				block, _ := pem.Decode(certPEM)
				require.NotNil(t, block)
				parsed, err := x509.ParseCertificate(block.Bytes)
				require.NoError(t, err)
				assert.Equal(t, tt.expectedCN, parsed.Subject.CommonName)

				// Verify against CA
				_, err = parsed.Verify(x509.VerifyOptions{
					Roots:     caPool,
					KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
				})
				require.NoError(t, err)
			}
		})
	}

	// Test LoadCA
	loadedCert, loadedKey, err := LoadCA(caCertPEM, caKeyPEM)
	require.NoError(t, err)
	assert.Equal(t, caCert.Subject.CommonName, loadedCert.Subject.CommonName)
	assert.NotNil(t, loadedKey)

	// Test LoadCA error on invalid PEM
	_, _, err = LoadCA([]byte("invalid"), caKeyPEM)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode CA certificate PEM")
}

func TestRunnerNameValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid standard", "runner-1", false},
		{"valid alphanumeric", "workerA", false},
		{"valid with underscore", "runner_east_1", false},
		{"path traversal parent", "../runner", true},
		{"path traversal current", "./runner", true},
		{"slash in name", "foo/bar", true},
		{"empty string", "", true},
		{"starts with hyphen", "-runner", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateRunnerName(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestGenerateDeployment(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	data := &ConfigData{
		Host:             "https://asgard.example.com",
		BackendPort:      7000,
		RegistryPort:     9311,
		EtcdEndpoints:    "http://etcd:2379",
		EtcdPrefix:       "/asgard/configs",
		AuthIssuer:       "https://auth.example.com/oauth2",
		AuthClientID:     "my-client-id",
		AuthClientSecret: "my-secret-123",
		AuthTokenSource:  "access_token",
		InitialRunner:    "runner-prod-1",
		WorkspaceMounts:  []string{"/home/user/src:/data/src"},
		GCPKMS:           "projects/test-p/locations/us/keyRings/r/cryptoKeys/k",
	}

	err := GenerateDeployment(tempDir, data)
	require.NoError(t, err)

	// Verify TLS certs generated and placed
	expectedFiles := []string{
		"tls/ca.crt",
		"tls/ca.key",
		"tls/backend.crt",
		"tls/backend.key",
		"tls/runner-prod-1.crt",
		"tls/runner-prod-1.key",
		"backend/certs/backend.crt",
		"backend/certs/backend.key",
		"backend/certs/client-ca.crt",
		"runners/runner-prod-1/certs/tls.crt",
		"runners/runner-prod-1/certs/tls.key",
		"runners/runner-prod-1/certs/ca.crt",
		"compose.yaml",
		"Justfile",
		".gitignore",
		"etcd-tree/.sops.yaml",
		"etcd-tree/asgard/config.yaml",
		"etcd-tree/asgard/proxy.yaml",
		"etcd-tree/aw/config.yaml",
		"etcd-tree/simplest/key.yaml",
		"etcd-tree/simplest/models.yaml",
		"etcd-tree/simplest/providers.yaml",
		"etcd-tree/credentials/auth.json",
		"etcd-tree/agents/agent-father/config.yaml",
		"etcd-tree/agents/agent-father/AGENTS.md",
		"etcd-tree/agents/default-agent/config.yaml",
		"etcd-tree/agents/default-agent/AGENTS.md",
	}

	for _, rel := range expectedFiles {
		p := filepath.Join(tempDir, rel)
		info, err := os.Stat(p)
		require.NoError(t, err, "file must exist: %s", rel)
		assert.False(t, info.IsDir(), "path should be file: %s", rel)
	}

	// Verify permissions on sensitive files
	authJSONInfo, err := os.Stat(filepath.Join(tempDir, "etcd-tree", "credentials", "auth.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), authJSONInfo.Mode().Perm())

	cfgInfo, err := os.Stat(filepath.Join(tempDir, "etcd-tree", "asgard", "config.yaml"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), cfgInfo.Mode().Perm())

	// Verify runner tls cert CN is runner-prod-1
	runnerCertBytes, err := os.ReadFile(filepath.Join(tempDir, "runners", "runner-prod-1", "certs", "tls.crt"))
	require.NoError(t, err)
	block, _ := pem.Decode(runnerCertBytes)
	require.NotNil(t, block)
	runnerCert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "runner-prod-1", runnerCert.Subject.CommonName)

	// Verify config.yaml has required fields and commented optional fields
	configContent, err := os.ReadFile(filepath.Join(tempDir, "etcd-tree", "asgard", "config.yaml"))
	require.NoError(t, err)
	cfgStr := string(configContent)

	assert.Contains(t, cfgStr, "host: \"https://asgard.example.com\"")
	assert.Contains(t, cfgStr, "port: 7000")
	assert.Contains(t, cfgStr, "tls_cert_file: \"/certs/backend.crt\"")
	assert.Contains(t, cfgStr, "tls_key_file: \"/certs/backend.key\"")
	assert.Contains(t, cfgStr, "client_ca_file: \"/certs/client-ca.crt\"")
	assert.Contains(t, cfgStr, "registry_port: 9311")
	assert.Contains(t, cfgStr, "- \"runner-prod-1\"")
	assert.Contains(t, cfgStr, "issuer: \"https://auth.example.com/oauth2\"")
	assert.Contains(t, cfgStr, "client_id: \"my-client-id\"")
	assert.Contains(t, cfgStr, "client_secret: \"my-secret-123\"")

	// Optional fields must be commented out
	assert.Contains(t, cfgStr, "# debug: false")
	assert.Contains(t, cfgStr, "# db: sqlite")
	assert.Contains(t, cfgStr, "# chat_lang:")
	assert.Contains(t, cfgStr, "# gemini_api_key:")

	// Verify compose.yaml port alignment
	composeContent, err := os.ReadFile(filepath.Join(tempDir, "compose.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(composeContent), "\"7000:7000\"")

	// Verify Justfile commands
	justContent, err := os.ReadFile(filepath.Join(tempDir, "Justfile"))
	require.NoError(t, err)
	justStr := string(justContent)
	assert.Contains(t, justStr, "start:\n    docker compose up -d")
	assert.Contains(t, justStr, "cfgctl encrypt ./etcd-tree")
	assert.Contains(t, justStr, "cfgctl push ./etcd-tree")
	assert.Contains(t, justStr, "publish: encrypt push")

	// Verify .sops.yaml contains oauth_creds.json in path_regex and credentials rule precedes generic rule
	sopsContent, err := os.ReadFile(filepath.Join(tempDir, "etcd-tree", ".sops.yaml"))
	require.NoError(t, err)
	sopsStr := string(sopsContent)
	assert.Contains(t, sopsStr, "oauth_creds\\.json")
	credRuleIdx := strings.Index(sopsStr, "credentials/.*")
	genericRuleIdx := strings.Index(sopsStr, ".*\\.(yaml|yml|json)$")
	require.True(t, credRuleIdx != -1 && genericRuleIdx != -1)
	assert.True(t, credRuleIdx < genericRuleIdx, "credentials whole-file encryption rule must precede generic yaml/json rule to avoid shadowing")
	assert.Contains(t, sopsStr, "refresh_token")
	assert.Contains(t, sopsStr, "id_token")

	// Overwrite guard test without Force
	err = GenerateDeployment(tempDir, data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "existing Asgard deployment detected")

	// Overwrite guard test with Force
	data.Force = true
	err = GenerateDeployment(tempDir, data)
	require.NoError(t, err)
}

func TestAddRunner(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	initialData := &ConfigData{
		Host:          "http://localhost:7000",
		InitialRunner: "runner-1",
	}
	require.NoError(t, GenerateDeployment(tempDir, initialData))

	// Add second runner
	err := AddRunner(tempDir, "runner-2", []string{"/data/extra:/data/extra"})
	require.NoError(t, err)

	// Check runner-2 certs and folders
	runner2Cert := filepath.Join(tempDir, "runners", "runner-2", "certs", "tls.crt")
	certBytes, err := os.ReadFile(runner2Cert)
	require.NoError(t, err)
	block, _ := pem.Decode(certBytes)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "runner-2", cert.Subject.CommonName)

	// Verify allowed_nodes order: runner-1 should remain first!
	configContent, err := os.ReadFile(filepath.Join(tempDir, "etcd-tree", "asgard", "config.yaml"))
	require.NoError(t, err)
	cfgStr := string(configContent)
	idx1 := strings.Index(cfgStr, "runner-1")
	idx2 := strings.Index(cfgStr, "runner-2")
	require.True(t, idx1 != -1 && idx2 != -1)
	assert.True(t, idx1 < idx2, "initial runner must remain first in allowed_nodes for backfill pinning")

	// Verify compose.yaml has runner-2 service
	composeContent, err := os.ReadFile(filepath.Join(tempDir, "compose.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(composeContent), "runner-2:")
	assert.Contains(t, string(composeContent), "container_name: asgard-runner-2")
	assert.Contains(t, string(composeContent), "./runners/runner-2/certs:/certs:ro")
	assert.Contains(t, string(composeContent), "/data/extra:/data/extra")

	// Adding runner with existing name reuses keypair and is idempotent
	err = AddRunner(tempDir, "runner-2", nil)
	require.NoError(t, err)
	configContent2, err := os.ReadFile(filepath.Join(tempDir, "etcd-tree", "asgard", "config.yaml"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(configContent2), "\"runner-2\""))

	// Test prefix protection: adding runner-2 when runner-21 exists should not falsely skip
	err = AddRunner(tempDir, "runner-21", nil)
	require.NoError(t, err)
	configContent3, err := os.ReadFile(filepath.Join(tempDir, "etcd-tree", "asgard", "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(configContent3), "\"runner-21\"")
	assert.Contains(t, string(configContent3), "\"runner-2\"")
}

func TestRunCommandNonInteractive(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	args := []string{
		"--non-interactive",
		"--dir", tempDir,
		"--host", "https://ai.example.com",
		"--port", "7000",
		"--runner", "worker-a",
		"--auth-issuer", "https://auth.example.com/oauth2",
		"--auth-client-id", "test-client",
		"--auth-client-secret", "test-secret",
	}

	err := run(args)
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(tempDir, "compose.yaml"))
	require.NoError(t, err)
	assert.False(t, info.IsDir())

	// Now test --add-new-runner flag
	addArgs := []string{
		"--dir", tempDir,
		"--add-new-runner", "worker-b",
	}
	err = run(addArgs)
	require.NoError(t, err)

	workerBCert := filepath.Join(tempDir, "runners", "worker-b", "certs", "tls.crt")
	_, err = os.Stat(workerBCert)
	require.NoError(t, err)
}

func TestCheckCLICredentials(t *testing.T) {
	t.Parallel()

	t.Run("empty directory returns false", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		path, ok := CheckCLICredentials(tempDir)
		assert.False(t, ok)
		assert.Empty(t, path)
	})

	t.Run("zero-byte oauth_creds.json returns false", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials")
		require.NoError(t, os.MkdirAll(credDir, 0755))
		oauthFile := filepath.Join(credDir, "oauth_creds.json")
		require.NoError(t, os.WriteFile(oauthFile, []byte(""), 0600))

		path, ok := CheckCLICredentials(tempDir)
		assert.False(t, ok)
		assert.Empty(t, path)
	})

	t.Run("valid oauth_creds.json returns true", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials")
		require.NoError(t, os.MkdirAll(credDir, 0755))
		oauthFile := filepath.Join(credDir, "oauth_creds.json")
		require.NoError(t, os.WriteFile(oauthFile, []byte(`{"token":"abc"}`), 0600))

		path, ok := CheckCLICredentials(tempDir)
		assert.True(t, ok)
		assert.Equal(t, oauthFile, path)
	})

	t.Run("valid antigravity-oauth-token fallback returns true", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials")
		require.NoError(t, os.MkdirAll(credDir, 0755))
		tokenFile := filepath.Join(credDir, "antigravity-oauth-token")
		require.NoError(t, os.WriteFile(tokenFile, []byte("tok123"), 0600))

		path, ok := CheckCLICredentials(tempDir)
		assert.True(t, ok)
		assert.Equal(t, tokenFile, path)
	})
}

func TestPromptCLICredentials(t *testing.T) {
	t.Parallel()

	t.Run("non-interactive skips gracefully when file missing", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		var out bytes.Buffer
		in := strings.NewReader("")

		verified := PromptCLICredentials(in, &out, tempDir, false)
		assert.False(t, verified)
		assert.NotContains(t, out.String(), "CLI credential verified")
	})

	t.Run("non-interactive detects existing credential", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials")
		require.NoError(t, os.MkdirAll(credDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(credDir, "oauth_creds.json"), []byte(`{"token":"xyz"}`), 0600))

		var out bytes.Buffer
		in := strings.NewReader("")

		verified := PromptCLICredentials(in, &out, tempDir, false)
		assert.True(t, verified)
		assert.Contains(t, out.String(), "CLI credential verified")
	})

	t.Run("interactive prompt verify success on y", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials")
		require.NoError(t, os.MkdirAll(credDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(credDir, "oauth_creds.json"), []byte(`{"token":"valid"}`), 0600))

		var out bytes.Buffer
		in := strings.NewReader("y\n")

		verified := PromptCLICredentials(in, &out, tempDir, true)
		assert.True(t, verified)
		assert.Contains(t, out.String(), "CLI credential verified successfully")
	})

	t.Run("interactive prompt skip on s", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()

		var out bytes.Buffer
		in := strings.NewReader("s\n")

		verified := PromptCLICredentials(in, &out, tempDir, true)
		assert.False(t, verified)
		assert.Contains(t, out.String(), "Skipped CLI credential setup")
	})

	t.Run("interactive retry: missing then placed then y", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials")
		require.NoError(t, os.MkdirAll(credDir, 0755))

		r, w := io.Pipe()
		t.Cleanup(func() { _ = r.Close(); _ = w.Close() })

		go func() {
			// Write first 'y' -> file doesn't exist
			_, _ = w.Write([]byte("y\n"))
			time.Sleep(50 * time.Millisecond)
			// Place credential file
			_ = os.WriteFile(filepath.Join(credDir, "oauth_creds.json"), []byte(`{"oauth":1}`), 0600)
			// Write second 'y' -> should now succeed
			_, _ = w.Write([]byte("y\n"))
		}()

		var out bytes.Buffer
		verified := PromptCLICredentials(r, &out, tempDir, true)
		assert.True(t, verified)
		assert.Contains(t, out.String(), "No valid, non-empty credential file found")
		assert.Contains(t, out.String(), "CLI credential verified successfully")
	})
}
