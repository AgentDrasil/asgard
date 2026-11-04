package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createFakeGCPKeyJSON(t *testing.T, dir, filename string) string {
	t.Helper()
	content := map[string]string{
		"type":         "service_account",
		"project_id":   "gcp-test-project",
		"private_key":  "-----BEGIN RSA PRIVATE KEY-----\nMIIEfake...\n-----END RSA PRIVATE KEY-----\n",
		"client_email": "sa-test@gcp-test-project.iam.gserviceaccount.com",
	}
	data, err := json.Marshal(content)
	require.NoError(t, err)

	p := filepath.Join(dir, filename)
	require.NoError(t, os.WriteFile(p, data, 0600))
	return p
}

func TestGCPSAKey_FlagValidation(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	validKey := createFakeGCPKeyJSON(t, tempDir, "valid-sa.json")

	invalidJSON := filepath.Join(tempDir, "invalid.json")
	require.NoError(t, os.WriteFile(invalidJSON, []byte("{not-json"), 0600))

	tests := []struct {
		name        string
		keyPath     string
		expectErr   bool
		errContains string
	}{
		{
			name:      "valid key file accepted",
			keyPath:   validKey,
			expectErr: false,
		},
		{
			name:        "non-existent key file rejected",
			keyPath:     filepath.Join(tempDir, "nonexistent.json"),
			expectErr:   true,
			errContains: "cannot be read",
		},
		{
			name:        "invalid json rejected",
			keyPath:     invalidJSON,
			expectErr:   true,
			errContains: "is not valid JSON",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			outDir := filepath.Join(tempDir, tt.name)
			args := []string{"--dir", outDir, "--non-interactive", "--gcp-sa-key", tt.keyPath}
			err := run(args)

			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}

			require.NoError(t, err)
			// Verify key was copied to <dir>/gcp-sa.json with 0600 permissions
			localKey := filepath.Join(outDir, "gcp-sa.json")
			require.FileExists(t, localKey)
			info, err := os.Stat(localKey)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

			// Verify Justfile has GOOGLE_APPLICATION_CREDENTIALS export
			justfileData, err := os.ReadFile(filepath.Join(outDir, "Justfile"))
			require.NoError(t, err)
			assert.Contains(t, string(justfileData), `export GOOGLE_APPLICATION_CREDENTIALS := "./gcp-sa.json"`)
		})
	}
}

func TestGCPSAKey_InteractivePrompt(t *testing.T) {
	// Not t.Parallel() because tests mutate shared defaultStdinReader

	t.Run("interactive prompt enters y when key is pre-placed", func(t *testing.T) {
		tempDir := t.TempDir()
		deployDir := filepath.Join(tempDir, "deploy")
		credDir := deployDir
		require.NoError(t, os.MkdirAll(credDir, 0755))
		createFakeGCPKeyJSON(t, credDir, "gcp-sa.json")

		// Simulation inputs for prompts:
		// 1. targetDir -> deployDir
		// 2. host -> default
		// 3. port -> default
		// 4. deploy etcd in compose -> default (Y)
		// 5. etcd endpoints -> default
		// 6. etcd prefix -> default
		// 7. oidc issuer -> default
		// 8. oidc client id -> default
		// 9. oidc client secret -> default
		// 10. runner -> default
		// 11. workspace mount -> empty
		// 12. GCP KMS -> projects/p/locations/l/keyRings/r/cryptoKeys/k
		// 13. GCP SA key -> y (verify placed key)
		// 14. providers (agy: skip/decline, claude: s/n, opencode: s/n, simplest: s/n -> fallback agy)
		// 15. ssh -> s
		inputs := []string{
			deployDir,
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"projects/p/locations/l/keyRings/r/cryptoKeys/k",
			"y",
			"s", "n", "s", "n", "s", "n", "s", "n",
			"s",
		}
		origStdin := defaultStdinReader
		t.Cleanup(func() { defaultStdinReader = origStdin })
		defaultStdinReader = nil

		// Run wizard via run
		r := strings.NewReader(strings.Join(inputs, "\n") + "\n")
		defaultStdinReader = bufioNewReader(r)

		err := run([]string{"--dir", deployDir})
		require.NoError(t, err)

		localKey := filepath.Join(deployDir, "gcp-sa.json")
		require.FileExists(t, localKey)
		info, err := os.Stat(localKey)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	})

	t.Run("interactive prompt with --gcp-sa-key flag automatically copied and verified on y", func(t *testing.T) {
		tempDir := t.TempDir()
		deployDir := filepath.Join(tempDir, "deploy-flag")
		saKey := createFakeGCPKeyJSON(t, tempDir, "interactive-sa.json")

		inputs := []string{
			deployDir,
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"projects/p/locations/l/keyRings/r/cryptoKeys/k",
			"y", // press 'y' to verify key that was passed via CLI flag and copied
			"s", "n", "s", "n", "s", "n", "s", "n",
			"s",
		}
		origStdin := defaultStdinReader
		t.Cleanup(func() { defaultStdinReader = origStdin })
		defaultStdinReader = bufioNewReader(strings.NewReader(strings.Join(inputs, "\n") + "\n"))

		err := run([]string{"--dir", deployDir, "--gcp-sa-key", saKey})
		require.NoError(t, err)

		localKey := filepath.Join(deployDir, "gcp-sa.json")
		require.FileExists(t, localKey)
		info, err := os.Stat(localKey)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	})

	t.Run("interactive prompt leaves SA key empty when skipped with s", func(t *testing.T) {
		tempDir := t.TempDir()
		deployDir := filepath.Join(tempDir, "deploy-empty-sa")

		inputs := []string{
			deployDir,
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"projects/p/locations/l/keyRings/r/cryptoKeys/k",
			"s", // skip SA key!
			"s", "n", "s", "n", "s", "n", "s", "n",
			"s",
		}

		origStdin := defaultStdinReader
		t.Cleanup(func() { defaultStdinReader = origStdin })
		defaultStdinReader = bufioNewReader(strings.NewReader(strings.Join(inputs, "\n") + "\n"))

		err := run([]string{"--dir", deployDir})
		require.NoError(t, err)

		// <dir>/gcp-sa.json should not exist
		localKey := filepath.Join(deployDir, "gcp-sa.json")
		assert.NoFileExists(t, localKey)
	})
}

func TestPromptGCPSACredentials_Interactive(t *testing.T) {
	t.Parallel()

	t.Run("interactive y with missing file retried then success", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		credDir := tempDir
		require.NoError(t, os.MkdirAll(credDir, 0755))

		r, w := io.Pipe()
		t.Cleanup(func() { _ = r.Close(); _ = w.Close() })

		go func() {
			_, _ = w.Write([]byte("y\n"))
			time.Sleep(50 * time.Millisecond)
			createFakeGCPKeyJSON(t, credDir, "gcp-sa.json")
			_, _ = w.Write([]byte("y\n"))
		}()

		var out bytes.Buffer
		verified := PromptGCPSACredentials(r, &out, tempDir, true)
		assert.True(t, verified)
		assert.Contains(t, out.String(), "No valid GCP Service Account JSON key file found")
		assert.Contains(t, out.String(), "GCP Service Account key verified successfully:")
	})

	t.Run("interactive s skips", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()

		var out bytes.Buffer
		in := strings.NewReader("s\n")

		verified := PromptGCPSACredentials(in, &out, tempDir, true)
		assert.False(t, verified)
		assert.Contains(t, out.String(), "Skipped GCP Service Account key setup")
	})

	t.Run("non-interactive existing vs missing", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()

		var out bytes.Buffer
		assert.False(t, PromptGCPSACredentials(strings.NewReader(""), &out, tempDir, false))

		credDir := tempDir
		require.NoError(t, os.MkdirAll(credDir, 0755))
		createFakeGCPKeyJSON(t, credDir, "gcp-sa.json")

		out.Reset()
		assert.True(t, PromptGCPSACredentials(strings.NewReader(""), &out, tempDir, false))
		assert.Contains(t, out.String(), "GCP Service Account key verified:")
	})
}

func TestAddRunner_PreservesGCPSAKey(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	saKey := createFakeGCPKeyJSON(t, tempDir, "base-sa.json")

	// Generate initial deployment with a local SA key
	err := run([]string{"--dir", tempDir, "--non-interactive", "--gcp-sa-key", saKey})
	require.NoError(t, err)

	localKey := filepath.Join(tempDir, "gcp-sa.json")
	require.FileExists(t, localKey)

	// Add new runner
	err = run([]string{"--dir", tempDir, "--add-new-runner", "runner-worker-2"})
	require.NoError(t, err)

	// Verify <dir>/gcp-sa.json remains intact with 0600 permissions
	require.FileExists(t, localKey)
	info, err := os.Stat(localKey)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())

	origContent, err := os.ReadFile(saKey)
	require.NoError(t, err)
	copiedContent, err := os.ReadFile(localKey)
	require.NoError(t, err)
	assert.Equal(t, origContent, copiedContent)
}

func TestAppendRunnerService_PreservesGoogleAppCreds(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	composePath := filepath.Join(tempDir, "compose.yaml")

	composeContent := `services:
  backend:
    environment:
      - ETCD_ENDPOINTS=http://etcd:2379
      - ETCD_PREFIX=/asgard/configs
      - GOOGLE_APPLICATION_CREDENTIALS=/asgard/gcp-sa.json
  runner-1:
    environment:
      - ETCD_ENDPOINTS=http://etcd:2379
      - ETCD_PREFIX=/asgard/configs
      - GOOGLE_APPLICATION_CREDENTIALS=/asgard/gcp-sa.json
`
	require.NoError(t, os.WriteFile(composePath, []byte(composeContent), 0644))

	err := appendRunnerService(composePath, "runner-2", nil)
	require.NoError(t, err)

	updatedContent, err := os.ReadFile(composePath)
	require.NoError(t, err)

	updatedStr := string(updatedContent)
	assert.Contains(t, updatedStr, "runner-2:")
	// Must preserve GOOGLE_APPLICATION_CREDENTIALS in newly appended service
	assert.Equal(t, 3, strings.Count(updatedStr, "GOOGLE_APPLICATION_CREDENTIALS=/asgard/gcp-sa.json"))
}

func bufioNewReader(r *strings.Reader) *bufio.Reader {
	return bufio.NewReader(r)
}

func TestGCPSAKey_LocalMountNotInEtcdTree(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	saKey := createFakeGCPKeyJSON(t, tempDir, "base-sa.json")
	outDir := filepath.Join(tempDir, "out")
	require.NoError(t, run([]string{"--dir", outDir, "--non-interactive", "--gcp-sa-key", saKey}))

	// Key is a local file, never staged inside etcd-tree.
	require.FileExists(t, filepath.Join(outDir, "gcp-sa.json"))
	assert.NoFileExists(t, filepath.Join(outDir, "etcd-tree", "credentials", "gcp", "gcp-sa.json"))

	compose, err := os.ReadFile(filepath.Join(outDir, "compose.yaml"))
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(compose), "- ./gcp-sa.json:/secrets/gcp-sa.json:ro"))
	assert.Equal(t, 2, strings.Count(string(compose), "- GOOGLE_APPLICATION_CREDENTIALS=/secrets/gcp-sa.json"))

	ignore, err := os.ReadFile(filepath.Join(outDir, "etcd-tree", ".cfgctlignore"))
	require.NoError(t, err)
	assert.Contains(t, string(ignore), "credentials/gcp/")
	assert.Contains(t, string(ignore), "credentials/gcp-sa.json")

	// A newly added runner gets the same mount.
	require.NoError(t, run([]string{"--dir", outDir, "--add-new-runner", "runner-2"}))
	compose, err = os.ReadFile(filepath.Join(outDir, "compose.yaml"))
	require.NoError(t, err)
	assert.Equal(t, 3, strings.Count(string(compose), "- ./gcp-sa.json:/secrets/gcp-sa.json:ro"))
	assert.Equal(t, 3, strings.Count(string(compose), "- GOOGLE_APPLICATION_CREDENTIALS=/secrets/gcp-sa.json"))
}

func TestJustfileExportsGCPCredsWhenKMSConfiguredWithoutKey(t *testing.T) {
	t.Parallel()

	outDir := t.TempDir()
	require.NoError(t, run([]string{"--dir", outDir, "--non-interactive", "--gcp-kms", "projects/p/locations/l/keyRings/r/cryptoKeys/k"}))

	justfile, err := os.ReadFile(filepath.Join(outDir, "Justfile"))
	require.NoError(t, err)
	assert.Contains(t, string(justfile), `export GOOGLE_APPLICATION_CREDENTIALS := "./gcp-sa.json"`)
	// No key yet: compose must not bind-mount a missing file (docker would create a directory).
	compose, err := os.ReadFile(filepath.Join(outDir, "compose.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(compose), "/secrets/gcp-sa.json")
}
