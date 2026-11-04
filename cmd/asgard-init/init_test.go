package main

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type syncWriter struct {
	onWrite func([]byte)
	writeFn func([]byte) (int, error)
}

func (s *syncWriter) Write(p []byte) (int, error) {
	if s.onWrite != nil {
		s.onWrite(p)
	}
	if s.writeFn != nil {
		return s.writeFn(p)
	}
	return len(p), nil
}

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
		"etcd-tree/aw/opencode.aw.yaml",
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

	// Verify permissions on sensitive files and credential directories
	for _, credSubDir := range []string{"credentials", "credentials/agy", "credentials/claude", "credentials/gcp", "credentials/ssh"} {
		dirInfo, err := os.Stat(filepath.Join(tempDir, "etcd-tree", credSubDir))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0700), dirInfo.Mode().Perm(), "dir %s should have 0700 perms", credSubDir)
	}

	authJSONInfo, err := os.Stat(filepath.Join(tempDir, "etcd-tree", "credentials", "auth.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), authJSONInfo.Mode().Perm())

	opencodeAWInfo, err := os.Stat(filepath.Join(tempDir, "etcd-tree", "aw", "opencode.aw.yaml"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), opencodeAWInfo.Mode().Perm())

	simplestKeyInfo, err := os.Stat(filepath.Join(tempDir, "etcd-tree", "simplest", "key.yaml"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), simplestKeyInfo.Mode().Perm())

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

	// Verify etcd-tree/credentials dedicated directories were created
	for _, sub := range []string{"agy", "claude", "gcp", "ssh"} {
		dirInfo, err := os.Stat(filepath.Join(tempDir, "etcd-tree", "credentials", sub))
		require.NoError(t, err, "directory must exist: %s", sub)
		assert.True(t, dirInfo.IsDir())
	}
	sshDirInfo, err := os.Stat(filepath.Join(tempDir, "etcd-tree", "credentials", "ssh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), sshDirInfo.Mode().Perm())

	// Verify Justfile contains binary sops encryption loop for extensionless credentials
	assert.Contains(t, justStr, "sops --input-type binary --output-type json -e \"$$f\" > \"$$f.sops.json\"")
	assert.NotContains(t, justStr, ".credentials.json")
	assert.Contains(t, justStr, "./etcd-tree/credentials/agy/antigravity-oauth-token")

	// Verify .gitignore ignores plaintext staged credentials (in-place encrypted JSON files are not ignored)
	gitignoreContent, err := os.ReadFile(filepath.Join(tempDir, ".gitignore"))
	require.NoError(t, err)
	assert.Contains(t, string(gitignoreContent), "etcd-tree/credentials/ssh/*")
	assert.Contains(t, string(gitignoreContent), "!etcd-tree/credentials/ssh/*.sops.json")
	assert.Contains(t, string(gitignoreContent), "!etcd-tree/credentials/ssh/.gitkeep")
	assert.Contains(t, string(gitignoreContent), "etcd-tree/credentials/agy/antigravity-oauth-token")
	assert.Contains(t, string(gitignoreContent), "etcd-tree/credentials/gcp/*")
	assert.Contains(t, string(gitignoreContent), "!etcd-tree/credentials/gcp/.gitkeep")
	assert.NotContains(t, string(gitignoreContent), ".credentials.json")

	// Verify .sops.yaml contains oauth_creds.json in path_regex and credentials rule precedes generic rule
	sopsContent, err := os.ReadFile(filepath.Join(tempDir, "etcd-tree", ".sops.yaml"))
	require.NoError(t, err)
	sopsStr := string(sopsContent)
	assert.Contains(t, sopsStr, "oauth_creds\\.json")
	assert.Contains(t, sopsStr, `\.credentials\.json`)
	assert.Contains(t, sopsStr, `aw/opencode\.aw\.yaml`)
	assert.Contains(t, sopsStr, `simplest/key\.yaml`)
	credRuleIdx := strings.Index(sopsStr, "credentials/.*")
	genericRuleIdx := strings.Index(sopsStr, ".*\\.(yaml|yml|json)$")
	require.True(t, credRuleIdx != -1 && genericRuleIdx != -1)
	assert.True(t, credRuleIdx < genericRuleIdx, "credentials whole-file encryption rule must precede generic yaml/json rule to avoid shadowing")
	assert.Contains(t, sopsStr, "refresh_token")
	assert.Contains(t, sopsStr, "id_token")

	// Verify commented providers when data.Providers is empty
	configContent, err = os.ReadFile(filepath.Join(tempDir, "etcd-tree", "asgard", "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(configContent), "# providers:")

	// Overwrite guard test without Force
	err = GenerateDeployment(tempDir, data)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "existing Asgard deployment detected")

	// Overwrite guard test with Force and custom providers
	data.Force = true
	data.Providers = []string{"agy", "opencode"}
	err = GenerateDeployment(tempDir, data)
	require.NoError(t, err)

	configContent, err = os.ReadFile(filepath.Join(tempDir, "etcd-tree", "asgard", "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(configContent), "providers:\n  - agy\n  - opencode")
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
	t.Setenv("ASGARD_INIT_SKIP_CFGCTL_CHECK", "1")

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

	// Verify default providers in config.yaml
	cfgData, err := os.ReadFile(filepath.Join(tempDir, "etcd-tree", "asgard", "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(cfgData), "providers:\n  - agy\n  - claude\n  - opencode\n  - simplest")

	// Test non-interactive with specific --providers flag
	tempDirCustom := t.TempDir()
	customArgs := []string{
		"--non-interactive",
		"--dir", tempDirCustom,
		"--providers", "opencode,simplest",
	}
	err = run(customArgs)
	require.NoError(t, err)
	customCfgData, err := os.ReadFile(filepath.Join(tempDirCustom, "etcd-tree", "asgard", "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(customCfgData), "providers:\n  - opencode\n  - simplest")
	assert.NotContains(t, string(customCfgData), "  - agy")
	assert.NotContains(t, string(customCfgData), "  - claude")

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
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials", "agy")
		require.NoError(t, os.MkdirAll(credDir, 0755))
		oauthFile := filepath.Join(credDir, "oauth_creds.json")
		require.NoError(t, os.WriteFile(oauthFile, []byte(""), 0600))

		path, ok := CheckCLICredentials(tempDir)
		assert.False(t, ok)
		assert.Empty(t, path)
	})

	t.Run("valid oauth_creds.json returns true and tightens permissions", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials", "agy")
		require.NoError(t, os.MkdirAll(credDir, 0755))
		oauthFile := filepath.Join(credDir, "oauth_creds.json")
		require.NoError(t, os.WriteFile(oauthFile, []byte(`{"token":"abc"}`), 0644))

		path, ok := CheckCLICredentials(tempDir)
		assert.True(t, ok)
		assert.Equal(t, oauthFile, path)

		fi, err := os.Stat(oauthFile)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), fi.Mode().Perm())
	})

	t.Run("valid antigravity-oauth-token returns true and tightens permissions", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials", "agy")
		require.NoError(t, os.MkdirAll(credDir, 0755))
		tokenFile := filepath.Join(credDir, "antigravity-oauth-token")
		require.NoError(t, os.WriteFile(tokenFile, []byte("tok123"), 0644))

		path, ok := CheckCLICredentials(tempDir)
		assert.True(t, ok)
		assert.Equal(t, tokenFile, path)

		fi, err := os.Stat(tokenFile)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), fi.Mode().Perm())
	})

	t.Run("root credentials file ignored (no root fallback)", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials")
		require.NoError(t, os.MkdirAll(credDir, 0755))
		oauthFile := filepath.Join(credDir, "oauth_creds.json")
		require.NoError(t, os.WriteFile(oauthFile, []byte(`{"token":"abc"}`), 0600))

		path, ok := CheckCLICredentials(tempDir)
		assert.False(t, ok)
		assert.Empty(t, path)
	})
}

func TestCheckClaudeCredentials(t *testing.T) {
	t.Parallel()

	t.Run("empty directory returns false", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		path, ok := CheckClaudeCredentials(tempDir)
		assert.False(t, ok)
		assert.Empty(t, path)
	})

	t.Run("zero-byte claude .credentials.json returns false", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		claudeDir := filepath.Join(tempDir, "etcd-tree", "credentials", "claude")
		require.NoError(t, os.MkdirAll(claudeDir, 0755))
		claudeFile := filepath.Join(claudeDir, ".credentials.json")
		require.NoError(t, os.WriteFile(claudeFile, []byte(""), 0600))

		path, ok := CheckClaudeCredentials(tempDir)
		assert.False(t, ok)
		assert.Empty(t, path)
	})

	t.Run("valid claude .credentials.json returns true and tightens permissions", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		claudeDir := filepath.Join(tempDir, "etcd-tree", "credentials", "claude")
		require.NoError(t, os.MkdirAll(claudeDir, 0755))
		claudeFile := filepath.Join(claudeDir, ".credentials.json")
		require.NoError(t, os.WriteFile(claudeFile, []byte(`{"claude_token":"secret"}`), 0644))

		path, ok := CheckClaudeCredentials(tempDir)
		assert.True(t, ok)
		assert.Equal(t, claudeFile, path)

		fi, err := os.Stat(claudeFile)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), fi.Mode().Perm())
	})

	t.Run("root .credentials.json ignored (no root fallback)", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials")
		require.NoError(t, os.MkdirAll(credDir, 0755))
		rootFile := filepath.Join(credDir, ".credentials.json")
		require.NoError(t, os.WriteFile(rootFile, []byte(`{"claude_root_token":"root_secret"}`), 0600))

		path, ok := CheckClaudeCredentials(tempDir)
		assert.False(t, ok)
		assert.Empty(t, path)
	})
}

func TestCheckGCPSACredentials(t *testing.T) {
	t.Parallel()

	t.Run("empty directory returns false", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		path, ok := CheckGCPSACredentials(tempDir)
		assert.False(t, ok)
		assert.Empty(t, path)
	})

	t.Run("zero-byte gcp-sa.json returns false", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials", "gcp")
		require.NoError(t, os.MkdirAll(credDir, 0755))
		saFile := filepath.Join(credDir, "gcp-sa.json")
		require.NoError(t, os.WriteFile(saFile, []byte(""), 0600))

		path, ok := CheckGCPSACredentials(tempDir)
		assert.False(t, ok)
		assert.Empty(t, path)
	})

	t.Run("invalid json returns false", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials", "gcp")
		require.NoError(t, os.MkdirAll(credDir, 0755))
		saFile := filepath.Join(credDir, "gcp-sa.json")
		require.NoError(t, os.WriteFile(saFile, []byte("{invalid-json"), 0600))

		path, ok := CheckGCPSACredentials(tempDir)
		assert.False(t, ok)
		assert.Empty(t, path)
	})

	t.Run("missing project_id returns false", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials", "gcp")
		require.NoError(t, os.MkdirAll(credDir, 0755))
		saFile := filepath.Join(credDir, "gcp-sa.json")
		require.NoError(t, os.WriteFile(saFile, []byte(`{"type":"service_account"}`), 0600))

		path, ok := CheckGCPSACredentials(tempDir)
		assert.False(t, ok)
		assert.Empty(t, path)
	})

	t.Run("valid gcp-sa.json returns true and tightens permissions to 0600", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials", "gcp")
		require.NoError(t, os.MkdirAll(credDir, 0755))
		saFile := filepath.Join(credDir, "gcp-sa.json")
		require.NoError(t, os.WriteFile(saFile, []byte(`{"type":"service_account","project_id":"test-proj"}`), 0644))

		path, ok := CheckGCPSACredentials(tempDir)
		assert.True(t, ok)
		assert.Equal(t, saFile, path)

		fi, err := os.Stat(saFile)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), fi.Mode().Perm())
	})

	t.Run("root gcp-sa.json ignored (no root fallback)", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials")
		require.NoError(t, os.MkdirAll(credDir, 0755))
		saFile := filepath.Join(credDir, "gcp-sa.json")
		require.NoError(t, os.WriteFile(saFile, []byte(`{"type":"service_account","project_id":"test-proj"}`), 0644))

		path, ok := CheckGCPSACredentials(tempDir)
		assert.False(t, ok)
		assert.Empty(t, path)
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
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials", "agy")
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
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials", "agy")
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
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials", "agy")
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
		assert.NotContains(t, out.String(), "(or antigravity-oauth-token)")
		assert.Contains(t, out.String(), "CLI credential verified successfully")
	})
}

func TestCheckSSHCredentials(t *testing.T) {
	t.Parallel()

	t.Run("empty directory returns false", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		keys, ok := CheckSSHCredentials(tempDir)
		assert.False(t, ok)
		assert.Nil(t, keys)
	})

	t.Run("zero-byte key returns false", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		sshDir := filepath.Join(tempDir, "etcd-tree", "credentials", "ssh")
		require.NoError(t, os.MkdirAll(sshDir, 0755))
		keyFile := filepath.Join(sshDir, "id_ed25519")
		require.NoError(t, os.WriteFile(keyFile, []byte(""), 0600))

		keys, ok := CheckSSHCredentials(tempDir)
		assert.False(t, ok)
		assert.Nil(t, keys)
	})

	t.Run("key is a directory returns false", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		keyDir := filepath.Join(tempDir, "etcd-tree", "credentials", "ssh", "id_ed25519")
		require.NoError(t, os.MkdirAll(keyDir, 0755))

		keys, ok := CheckSSHCredentials(tempDir)
		assert.False(t, ok)
		assert.Nil(t, keys)
	})

	t.Run("valid non-empty id_ed25519 returns true", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		sshDir := filepath.Join(tempDir, "etcd-tree", "credentials", "ssh")
		require.NoError(t, os.MkdirAll(sshDir, 0755))
		keyFile := filepath.Join(sshDir, "id_ed25519")
		require.NoError(t, os.WriteFile(keyFile, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\ntest\n-----END OPENSSH PRIVATE KEY-----"), 0600))

		keys, ok := CheckSSHCredentials(tempDir)
		assert.True(t, ok)
		assert.Equal(t, []string{"id_ed25519"}, keys)
	})

	t.Run("valid key with loose 0644 permissions is tightened to 0600", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		sshDir := filepath.Join(tempDir, "etcd-tree", "credentials", "ssh")
		require.NoError(t, os.MkdirAll(sshDir, 0755))
		keyFile := filepath.Join(sshDir, "id_ed25519")
		require.NoError(t, os.WriteFile(keyFile, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nmy-private-key"), 0644))

		keys, ok := CheckSSHCredentials(tempDir)
		assert.True(t, ok)
		assert.Equal(t, []string{"id_ed25519"}, keys)

		stat, err := os.Stat(keyFile)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), stat.Mode().Perm())
	})

	t.Run("key without BEGIN header returns false", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		sshDir := filepath.Join(tempDir, "etcd-tree", "credentials", "ssh")
		require.NoError(t, os.MkdirAll(sshDir, 0755))
		keyFile := filepath.Join(sshDir, "id_ed25519")
		require.NoError(t, os.WriteFile(keyFile, []byte("not-a-valid-pem-or-openssh-key"), 0600))

		keys, ok := CheckSSHCredentials(tempDir)
		assert.False(t, ok)
		assert.Nil(t, keys)
	})

	t.Run("multiple keys found, non-private files ignored, unsupported files warned", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		sshDir := filepath.Join(tempDir, "etcd-tree", "credentials", "ssh")
		require.NoError(t, os.MkdirAll(sshDir, 0755))

		// Valid private keys with BEGIN header (loose permissions)
		require.NoError(t, os.WriteFile(filepath.Join(sshDir, "id_ed25519"), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\ned25519"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("-----BEGIN RSA PRIVATE KEY-----\nrsa"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(sshDir, "key"), []byte("-----BEGIN PRIVATE KEY-----\nkey"), 0644))

		// Non-private files should be ignored without warning
		require.NoError(t, os.WriteFile(filepath.Join(sshDir, "id_ed25519.pub"), []byte("pub-data"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(sshDir, "id_ed25519.sops.json"), []byte("{}"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(sshDir, "config"), []byte("Host *"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(sshDir, "known_hosts"), []byte("hosts"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(sshDir, ".gitkeep"), []byte(""), 0644))

		// Unsupported file names (including authorized_keys and id_custom) should trigger warning and not be synced
		require.NoError(t, os.WriteFile(filepath.Join(sshDir, "authorized_keys"), []byte("ssh-ed25519 ..."), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(sshDir, "id_custom"), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\ncustom"), 0644))

		keys, ok := CheckSSHCredentials(tempDir)
		assert.True(t, ok)
		assert.Equal(t, []string{"id_ed25519", "id_rsa", "key"}, keys)

		// Check all private keys were chmodded to 0600
		for _, name := range keys {
			stat, err := os.Stat(filepath.Join(sshDir, name))
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0600), stat.Mode().Perm())
		}
	})
}

func TestPromptSSHCredentials(t *testing.T) {
	t.Parallel()

	t.Run("non-interactive skips gracefully when file missing", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		var out bytes.Buffer
		reader := bufio.NewReader(strings.NewReader(""))

		verified := PromptSSHCredentials(reader, &out, tempDir, false)
		assert.False(t, verified)
		assert.NotContains(t, out.String(), "SSH credentials verified")
	})

	t.Run("non-interactive detects existing id_ed25519 key", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		sshDir := filepath.Join(tempDir, "etcd-tree", "credentials", "ssh")
		require.NoError(t, os.MkdirAll(sshDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(sshDir, "id_ed25519"), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nmy-private-key"), 0600))

		var out bytes.Buffer
		reader := bufio.NewReader(strings.NewReader(""))

		verified := PromptSSHCredentials(reader, &out, tempDir, false)
		assert.True(t, verified)
		assert.Contains(t, out.String(), "SSH credentials verified")
		assert.Contains(t, out.String(), "id_ed25519")
	})

	t.Run("interactive prompt verify success on y with multiple keys", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		sshDir := filepath.Join(tempDir, "etcd-tree", "credentials", "ssh")
		require.NoError(t, os.MkdirAll(sshDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(sshDir, "id_ed25519"), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nmy-private-key"), 0600))
		require.NoError(t, os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("-----BEGIN RSA PRIVATE KEY-----\nmy-rsa-key"), 0600))

		var out bytes.Buffer
		reader := bufio.NewReader(strings.NewReader("y\n"))

		verified := PromptSSHCredentials(reader, &out, tempDir, true)
		assert.True(t, verified)
		assert.Contains(t, out.String(), "SSH credentials verified successfully")
		assert.Contains(t, out.String(), "id_ed25519, id_rsa")
	})

	t.Run("interactive prompt verify success on Y (uppercase)", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		sshDir := filepath.Join(tempDir, "etcd-tree", "credentials", "ssh")
		require.NoError(t, os.MkdirAll(sshDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(sshDir, "id_ed25519"), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nmy-private-key"), 0600))

		var out bytes.Buffer
		reader := bufio.NewReader(strings.NewReader("Y\n"))

		verified := PromptSSHCredentials(reader, &out, tempDir, true)
		assert.True(t, verified)
		assert.Contains(t, out.String(), "SSH credentials verified successfully")
		assert.Contains(t, out.String(), "id_ed25519")
	})

	t.Run("interactive prompt skip on s", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()

		var out bytes.Buffer
		reader := bufio.NewReader(strings.NewReader("s\n"))

		verified := PromptSSHCredentials(reader, &out, tempDir, true)
		assert.False(t, verified)
		assert.Contains(t, out.String(), "Skipped SSH credentials setup")
	})

	t.Run("interactive retry: missing then placed then y", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		sshDir := filepath.Join(tempDir, "etcd-tree", "credentials", "ssh")
		require.NoError(t, os.MkdirAll(sshDir, 0755))

		r, w := io.Pipe()
		t.Cleanup(func() { _ = r.Close(); _ = w.Close() })

		errSeen := make(chan struct{}, 1)
		var outBuf bytes.Buffer
		var outMu sync.Mutex

		outWriter := &syncWriter{
			onWrite: func(p []byte) {
				if strings.Contains(string(p), "No valid") {
					select {
					case errSeen <- struct{}{}:
					default:
					}
				}
			},
			writeFn: func(p []byte) (int, error) {
				outMu.Lock()
				defer outMu.Unlock()
				return outBuf.Write(p)
			},
		}

		go func() {
			// Write first 'y' -> file doesn't exist
			_, _ = w.Write([]byte("y\n"))
			// Wait until error message is output before creating the file
			select {
			case <-errSeen:
			case <-time.After(2 * time.Second):
			}
			// Place credential key file
			_ = os.WriteFile(filepath.Join(sshDir, "id_ed25519"), []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nssh-ed25519 test-key"), 0600)
			// Write second 'y' -> should now succeed
			_, _ = w.Write([]byte("y\n"))
		}()

		reader := bufio.NewReader(r)
		verified := PromptSSHCredentials(reader, outWriter, tempDir, true)
		assert.True(t, verified)

		outMu.Lock()
		outStr := outBuf.String()
		outMu.Unlock()

		assert.Contains(t, outStr, "No valid, non-empty SSH private key found")
		assert.Contains(t, outStr, "SSH credentials verified successfully")
		assert.Contains(t, outStr, "id_ed25519")
	})

	t.Run("interactive invalid input then skip", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()

		var out bytes.Buffer
		reader := bufio.NewReader(strings.NewReader("what?\ns\n"))

		verified := PromptSSHCredentials(reader, &out, tempDir, true)
		assert.False(t, verified)
		assert.Contains(t, out.String(), "Invalid input \"what?\"")
		assert.Contains(t, out.String(), "Skipped SSH credentials setup")
	})
}

func TestCheckOpencodeConfig(t *testing.T) {
	t.Parallel()

	t.Run("missing file returns false", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		path, ok := CheckOpencodeConfig(tempDir)
		assert.False(t, ok)
		assert.Empty(t, path)
	})

	t.Run("empty file returns false", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		awDir := filepath.Join(tempDir, "etcd-tree", "aw")
		require.NoError(t, os.MkdirAll(awDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(awDir, "opencode.aw.yaml"), []byte("   \n\n"), 0600))
		path, ok := CheckOpencodeConfig(tempDir)
		assert.False(t, ok)
		assert.Empty(t, path)
	})

	t.Run("commented-only file returns false", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		awDir := filepath.Join(tempDir, "etcd-tree", "aw")
		require.NoError(t, os.MkdirAll(awDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(awDir, "opencode.aw.yaml"), []byte("# comment\n# env:\n#   KEY: val\n"), 0600))
		path, ok := CheckOpencodeConfig(tempDir)
		assert.False(t, ok)
		assert.Empty(t, path)
	})

	t.Run("file with uncommented keys returns true and tightens permissions to 0600", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		awDir := filepath.Join(tempDir, "etcd-tree", "aw")
		require.NoError(t, os.MkdirAll(awDir, 0755))
		targetPath := filepath.Join(awDir, "opencode.aw.yaml")
		require.NoError(t, os.WriteFile(targetPath, []byte("env:\n  OPENCODE_ZAI_API_KEY: \"sk-123\"\n"), 0644))
		path, ok := CheckOpencodeConfig(tempDir)
		assert.True(t, ok)
		assert.Equal(t, targetPath, path)
		info, err := os.Stat(targetPath)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	})
}

func TestCheckSimplestConfig(t *testing.T) {
	t.Parallel()

	t.Run("missing file returns false", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		path, ok := CheckSimplestConfig(tempDir)
		assert.False(t, ok)
		assert.Empty(t, path)
	})

	t.Run("commented-only file returns false", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		simplestDir := filepath.Join(tempDir, "etcd-tree", "simplest")
		require.NoError(t, os.MkdirAll(simplestDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(simplestDir, "key.yaml"), []byte("# openai: sk-...\n"), 0600))
		path, ok := CheckSimplestConfig(tempDir)
		assert.False(t, ok)
		assert.Empty(t, path)
	})

	t.Run("file with uncommented keys returns true and tightens permissions to 0600", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		simplestDir := filepath.Join(tempDir, "etcd-tree", "simplest")
		require.NoError(t, os.MkdirAll(simplestDir, 0755))
		targetPath := filepath.Join(simplestDir, "key.yaml")
		require.NoError(t, os.WriteFile(targetPath, []byte("openai: \"sk-12345\"\n"), 0644))
		path, ok := CheckSimplestConfig(tempDir)
		assert.True(t, ok)
		assert.Equal(t, targetPath, path)
		info, err := os.Stat(targetPath)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	})
}

func TestPromptCLIActivation(t *testing.T) {
	t.Parallel()

	t.Run("yes activates", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		assert.True(t, PromptCLIActivation(strings.NewReader("y\n"), &out, "opencode"))
		assert.Contains(t, out.String(), "Activate opencode anyway? [y/N]:")
	})

	t.Run("uppercase YES activates", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		assert.True(t, PromptCLIActivation(strings.NewReader("YES\n"), &out, "claude"))
	})

	t.Run("no does not activate", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		assert.False(t, PromptCLIActivation(strings.NewReader("n\n"), &out, "agy"))
	})

	t.Run("empty enter does not activate", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		assert.False(t, PromptCLIActivation(strings.NewReader("\n"), &out, "simplest"))
	})

	t.Run("EOF does not activate", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		assert.False(t, PromptCLIActivation(strings.NewReader(""), &out, "opencode"))
	})
}

func TestPromptClaudeCredentials(t *testing.T) {
	t.Parallel()

	t.Run("non-interactive detects existing", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		claudeDir := filepath.Join(tempDir, "etcd-tree", "credentials", "claude")
		require.NoError(t, os.MkdirAll(claudeDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), []byte(`{"token":"c"}`), 0600))

		var out bytes.Buffer
		assert.True(t, PromptClaudeCredentials(strings.NewReader(""), &out, tempDir, false))
		assert.Contains(t, out.String(), "Claude credential verified:")
	})

	t.Run("interactive prompt skip on s", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		var out bytes.Buffer
		assert.False(t, PromptClaudeCredentials(strings.NewReader("s\n"), &out, tempDir, true))
		assert.Contains(t, out.String(), "Skipped Claude credential setup")
	})

	t.Run("interactive prompt verify success on y", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		claudeDir := filepath.Join(tempDir, "etcd-tree", "credentials", "claude")
		require.NoError(t, os.MkdirAll(claudeDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), []byte(`{"token":"c"}`), 0600))

		var out bytes.Buffer
		assert.True(t, PromptClaudeCredentials(strings.NewReader("y\n"), &out, tempDir, true))
		assert.Contains(t, out.String(), "Claude credential verified successfully:")
		assert.NotContains(t, out.String(), "(or etcd-tree/credentials/.credentials.json)")
	})

	t.Run("interactive prompt verify failure on y then skip", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()

		var out bytes.Buffer
		assert.False(t, PromptClaudeCredentials(strings.NewReader("y\ns\n"), &out, tempDir, true))
		assert.Contains(t, out.String(), "No valid, non-empty Claude credential file found at")
		assert.NotContains(t, out.String(), "(or etcd-tree/credentials/.credentials.json)")
	})
}

func TestPromptGCPSACredentials(t *testing.T) {
	t.Parallel()

	t.Run("non-interactive skips gracefully when file missing", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		var out bytes.Buffer
		in := strings.NewReader("")

		verified := PromptGCPSACredentials(in, &out, tempDir, false)
		assert.False(t, verified)
		assert.NotContains(t, out.String(), "GCP Service Account key verified")
	})

	t.Run("non-interactive detects existing key", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials", "gcp")
		require.NoError(t, os.MkdirAll(credDir, 0755))
		saFile := filepath.Join(credDir, "gcp-sa.json")
		require.NoError(t, os.WriteFile(saFile, []byte(`{"type":"service_account","project_id":"test-proj"}`), 0600))

		var out bytes.Buffer
		in := strings.NewReader("")

		verified := PromptGCPSACredentials(in, &out, tempDir, false)
		assert.True(t, verified)
		assert.Contains(t, out.String(), "GCP Service Account key verified:")
	})

	t.Run("interactive prompt verify success on y", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials", "gcp")
		require.NoError(t, os.MkdirAll(credDir, 0755))
		saFile := filepath.Join(credDir, "gcp-sa.json")
		require.NoError(t, os.WriteFile(saFile, []byte(`{"type":"service_account","project_id":"test-proj"}`), 0600))

		var out bytes.Buffer
		in := strings.NewReader("y\n")

		verified := PromptGCPSACredentials(in, &out, tempDir, true)
		assert.True(t, verified)
		assert.Contains(t, out.String(), "GCP Service Account key verified successfully:")
	})

	t.Run("interactive prompt skip on s", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()

		var out bytes.Buffer
		in := strings.NewReader("s\n")

		verified := PromptGCPSACredentials(in, &out, tempDir, true)
		assert.False(t, verified)
		assert.Contains(t, out.String(), "Skipped GCP Service Account key setup")
	})

	t.Run("interactive retry: missing then placed then y", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials", "gcp")
		require.NoError(t, os.MkdirAll(credDir, 0755))

		r, w := io.Pipe()
		t.Cleanup(func() { _ = r.Close(); _ = w.Close() })

		go func() {
			// Write first 'y' -> file doesn't exist
			_, _ = w.Write([]byte("y\n"))
			time.Sleep(50 * time.Millisecond)
			// Place valid credential file
			_ = os.WriteFile(filepath.Join(credDir, "gcp-sa.json"), []byte(`{"type":"service_account","project_id":"retry-proj"}`), 0600)
			// Write second 'y' -> should now succeed
			_, _ = w.Write([]byte("y\n"))
		}()

		var out bytes.Buffer
		verified := PromptGCPSACredentials(r, &out, tempDir, true)
		assert.True(t, verified)
		assert.Contains(t, out.String(), "No valid GCP Service Account JSON key file found")
		assert.Contains(t, out.String(), "GCP Service Account key verified successfully:")
	})
}

func TestPromptOpencodeConfig(t *testing.T) {
	t.Parallel()

	t.Run("non-interactive detects existing", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		awDir := filepath.Join(tempDir, "etcd-tree", "aw")
		require.NoError(t, os.MkdirAll(awDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(awDir, "opencode.aw.yaml"), []byte("env:\n  API_KEY: \"sk-1\"\n"), 0600))

		var out bytes.Buffer
		assert.True(t, PromptOpencodeConfig(strings.NewReader(""), &out, tempDir, false))
		assert.Contains(t, out.String(), "OpenCode configuration verified:")
	})

	t.Run("interactive prompt skip on s", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		var out bytes.Buffer
		assert.False(t, PromptOpencodeConfig(strings.NewReader("s\n"), &out, tempDir, true))
		assert.Contains(t, out.String(), "Skipped OpenCode configuration setup")
	})

	t.Run("interactive prompt verify success on y", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		awDir := filepath.Join(tempDir, "etcd-tree", "aw")
		require.NoError(t, os.MkdirAll(awDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(awDir, "opencode.aw.yaml"), []byte("env:\n  API_KEY: \"sk-1\"\n"), 0600))

		var out bytes.Buffer
		assert.True(t, PromptOpencodeConfig(strings.NewReader("y\n"), &out, tempDir, true))
		assert.Contains(t, out.String(), "OpenCode configuration verified successfully:")
	})
}

func TestPromptSimplestConfig(t *testing.T) {
	t.Parallel()

	t.Run("non-interactive detects existing", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		simplestDir := filepath.Join(tempDir, "etcd-tree", "simplest")
		require.NoError(t, os.MkdirAll(simplestDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(simplestDir, "key.yaml"), []byte("openai: \"sk-test\"\n"), 0600))

		var out bytes.Buffer
		assert.True(t, PromptSimplestConfig(strings.NewReader(""), &out, tempDir, false))
		assert.Contains(t, out.String(), "Simplest configuration verified:")
	})

	t.Run("interactive prompt skip on s", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		var out bytes.Buffer
		assert.False(t, PromptSimplestConfig(strings.NewReader("s\n"), &out, tempDir, true))
		assert.Contains(t, out.String(), "Skipped Simplest configuration setup")
	})

	t.Run("interactive prompt verify success on y", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		simplestDir := filepath.Join(tempDir, "etcd-tree", "simplest")
		require.NoError(t, os.MkdirAll(simplestDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(simplestDir, "key.yaml"), []byte("openai: \"sk-test\"\n"), 0600))

		var out bytes.Buffer
		assert.True(t, PromptSimplestConfig(strings.NewReader("y\n"), &out, tempDir, true))
		assert.Contains(t, out.String(), "Simplest configuration verified successfully:")
	})
}

func TestPromptProviderSetup(t *testing.T) {
	t.Parallel()

	t.Run("all verified with credentials", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()

		// Prepare all credential files
		credDir := filepath.Join(tempDir, "etcd-tree", "credentials")
		agyDir := filepath.Join(credDir, "agy")
		claudeDir := filepath.Join(credDir, "claude")
		awDir := filepath.Join(tempDir, "etcd-tree", "aw")
		simplestDir := filepath.Join(tempDir, "etcd-tree", "simplest")

		require.NoError(t, os.MkdirAll(agyDir, 0755))
		require.NoError(t, os.MkdirAll(claudeDir, 0755))
		require.NoError(t, os.MkdirAll(awDir, 0755))
		require.NoError(t, os.MkdirAll(simplestDir, 0755))

		require.NoError(t, os.WriteFile(filepath.Join(agyDir, "oauth_creds.json"), []byte(`{"tok":"1"}`), 0600))
		require.NoError(t, os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), []byte(`{"tok":"2"}`), 0600))
		require.NoError(t, os.WriteFile(filepath.Join(awDir, "opencode.aw.yaml"), []byte("env:\n  K: V\n"), 0600))
		require.NoError(t, os.WriteFile(filepath.Join(simplestDir, "key.yaml"), []byte("gemini: AIza...\n"), 0600))

		in := strings.NewReader("y\ny\ny\ny\n")
		var out bytes.Buffer

		providers := PromptProviderSetup(in, &out, tempDir, true)
		assert.Equal(t, []string{"agy", "claude", "opencode", "simplest"}, providers)
	})

	t.Run("skip credential but activate anyway vs decline activation", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()

		// Scenario:
		// 1. agy: skip ('s'), activate anyway? yes ('y') -> activated
		// 2. claude: skip ('s'), activate anyway? no ('n') -> not activated
		// 3. opencode: skip ('s'), activate anyway? yes ('y') -> activated
		// 4. simplest: skip ('s'), activate anyway? no ('n') -> not activated
		in := strings.NewReader("s\ny\ns\nn\ns\ny\ns\nn\n")
		var out bytes.Buffer

		providers := PromptProviderSetup(in, &out, tempDir, true)
		assert.Equal(t, []string{"agy", "opencode"}, providers)
	})

	t.Run("decline all providers warns and falls back to agy", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()

		// Decline all 4 providers:
		in := strings.NewReader("s\nn\ns\nn\ns\nn\ns\nn\n")
		var out bytes.Buffer

		providers := PromptProviderSetup(in, &out, tempDir, true)
		assert.Equal(t, []string{"agy"}, providers)
		assert.Contains(t, out.String(), "At least one provider must be enabled for Asgard to function.")
	})

	t.Run("decline all providers warns and falls back to simplest when simplest config exists", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()

		simplestDir := filepath.Join(tempDir, "etcd-tree", "simplest")
		require.NoError(t, os.MkdirAll(simplestDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(simplestDir, "key.yaml"), []byte("gemini: AIza...\n"), 0600))

		// agy: skip & decline
		// claude: skip & decline
		// opencode: skip & decline
		// simplest: skip & decline
		in := strings.NewReader("s\nn\ns\nn\ns\nn\ns\nn\n")
		var out bytes.Buffer

		providers := PromptProviderSetup(in, &out, tempDir, true)
		assert.Equal(t, []string{"simplest"}, providers)
		assert.Contains(t, out.String(), "At least one provider must be enabled for Asgard to function.")
	})
}

func TestProvidersFlagValidation(t *testing.T) {
	t.Parallel()

	t.Run("valid providers accepted", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		err := run([]string{"--dir", tempDir, "--non-interactive", "--providers", "agy,opencode,simplest"})
		require.NoError(t, err)

		cfgData, err := os.ReadFile(filepath.Join(tempDir, "etcd-tree", "asgard", "config.yaml"))
		require.NoError(t, err)
		assert.Contains(t, string(cfgData), "providers:\n  - agy\n  - opencode\n  - simplest")
	})

	t.Run("invalid provider rejected", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		err := run([]string{"--dir", tempDir, "--non-interactive", "--providers", "agy,unknown_provider"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `invalid provider "unknown_provider": supported providers are agy, claude, opencode, simplest`)
	})

	t.Run("empty providers flag rejected", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		err := run([]string{"--dir", tempDir, "--non-interactive", "--providers", " , "})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no valid providers specified in --providers flag")
	})
}

func TestGenerateDeployment_PreservesExistingConfigs(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	awDir := filepath.Join(tempDir, "etcd-tree", "aw")
	simplestDir := filepath.Join(tempDir, "etcd-tree", "simplest")
	require.NoError(t, os.MkdirAll(awDir, 0755))
	require.NoError(t, os.MkdirAll(simplestDir, 0755))

	opencodeAWPath := filepath.Join(awDir, "opencode.aw.yaml")
	simplestKeyPath := filepath.Join(simplestDir, "key.yaml")

	require.NoError(t, os.WriteFile(opencodeAWPath, []byte("# custom opencode config\nCUSTOM: 1\n"), 0600))
	require.NoError(t, os.WriteFile(simplestKeyPath, []byte("# custom simplest key\ngemini: AIza-custom\n"), 0600))

	cfg := &ConfigData{
		Host:        "localhost",
		BackendPort: 7000,
		Force:       true,
	}
	require.NoError(t, GenerateDeployment(tempDir, cfg))

	awData, err := os.ReadFile(opencodeAWPath)
	require.NoError(t, err)
	assert.Equal(t, "# custom opencode config\nCUSTOM: 1\n", string(awData))

	keyData, err := os.ReadFile(simplestKeyPath)
	require.NoError(t, err)
	assert.Equal(t, "# custom simplest key\ngemini: AIza-custom\n", string(keyData))
}

func TestMigrateLegacyCredentials(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	credDir := filepath.Join(tempDir, "etcd-tree", "credentials")
	require.NoError(t, os.MkdirAll(credDir, 0700))

	// Write legacy files
	legacyOAuth := filepath.Join(credDir, "oauth_creds.json")
	legacyToken := filepath.Join(credDir, "antigravity-oauth-token")
	legacyClaude := filepath.Join(credDir, ".credentials.json")
	legacyGCP := filepath.Join(credDir, "gcp-sa.json")

	require.NoError(t, os.WriteFile(legacyOAuth, []byte(`{"oauth":"data"}`), 0600))
	require.NoError(t, os.WriteFile(legacyToken, []byte("tok-data"), 0600))
	require.NoError(t, os.WriteFile(legacyClaude, []byte(`{"claude":"data"}`), 0600))
	require.NoError(t, os.WriteFile(legacyGCP, []byte(`{"gcp":"data"}`), 0600))

	MigrateLegacyCredentials(tempDir)

	// Old files should be removed
	assert.NoFileExists(t, legacyOAuth)
	assert.NoFileExists(t, legacyToken)
	assert.NoFileExists(t, legacyClaude)
	assert.NoFileExists(t, legacyGCP)

	// New files should exist with correct content and 0600 permissions
	newOAuth := filepath.Join(credDir, "agy", "oauth_creds.json")
	newToken := filepath.Join(credDir, "agy", "antigravity-oauth-token")
	newClaude := filepath.Join(credDir, "claude", ".credentials.json")
	newGCP := filepath.Join(credDir, "gcp", "gcp-sa.json")

	require.FileExists(t, newOAuth)
	require.FileExists(t, newToken)
	require.FileExists(t, newClaude)
	require.FileExists(t, newGCP)

	data, err := os.ReadFile(newOAuth)
	require.NoError(t, err)
	assert.Equal(t, `{"oauth":"data"}`, string(data))

	data, err = os.ReadFile(newToken)
	require.NoError(t, err)
	assert.Equal(t, "tok-data", string(data))

	data, err = os.ReadFile(newClaude)
	require.NoError(t, err)
	assert.Equal(t, `{"claude":"data"}`, string(data))

	data, err = os.ReadFile(newGCP)
	require.NoError(t, err)
	assert.Equal(t, `{"gcp":"data"}`, string(data))

	// Verify directory permissions
	for _, sub := range []string{"agy", "claude", "gcp"} {
		info, err := os.Stat(filepath.Join(credDir, sub))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
	}
}
