package proxy

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureCA_Idempotent(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	certPath := filepath.Join(tempDir, "ca", "ca.crt")
	keyPath := filepath.Join(tempDir, "ca", "ca.key")

	// First run: files should be generated
	err := EnsureCA(certPath, keyPath)
	require.NoError(t, err)

	certInfo, err := os.Stat(certPath)
	require.NoError(t, err)
	keyInfo, err := os.Stat(keyPath)
	require.NoError(t, err)

	// Check permissions: key should be 0600, cert 0644
	assert.Equal(t, os.FileMode(0644), certInfo.Mode().Perm())
	assert.Equal(t, os.FileMode(0600), keyInfo.Mode().Perm())

	certBytes, err := os.ReadFile(certPath)
	require.NoError(t, err)
	keyBytes, err := os.ReadFile(keyPath)
	require.NoError(t, err)

	// Verify certificate attributes
	block, _ := pem.Decode(certBytes)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.True(t, cert.IsCA)
	assert.Equal(t, "Asgard Sandbox Root CA", cert.Subject.CommonName)
	assert.Contains(t, cert.Subject.Organization, "AgentDrasil")

	// Second run: verify idempotency, should not overwrite
	err = EnsureCA(certPath, keyPath)
	require.NoError(t, err)

	certBytes2, err := os.ReadFile(certPath)
	require.NoError(t, err)
	keyBytes2, err := os.ReadFile(keyPath)
	require.NoError(t, err)

	assert.Equal(t, certBytes, certBytes2, "CA cert should not change on second call")
	assert.Equal(t, keyBytes, keyBytes2, "CA key should not change on second call")
}

func TestMergeCACert(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	hostBundlePath := filepath.Join(tempDir, "host.crt")
	caCertPath := filepath.Join(tempDir, "ca.crt")
	outPath := filepath.Join(tempDir, "merged", "ca-certificates.crt")

	hostContent := "-----BEGIN CERTIFICATE-----\nHOST_BUNDLE_DATA\n-----END CERTIFICATE-----\n"
	caContent := "-----BEGIN CERTIFICATE-----\nASGARD_CA_DATA\n-----END CERTIFICATE-----\n"

	require.NoError(t, os.WriteFile(hostBundlePath, []byte(hostContent), 0644))
	require.NoError(t, os.WriteFile(caCertPath, []byte(caContent), 0644))

	err := MergeCACert(hostBundlePath, caCertPath, outPath)
	require.NoError(t, err)

	outBytes, err := os.ReadFile(outPath)
	require.NoError(t, err)

	outStr := string(outBytes)
	assert.Contains(t, outStr, "HOST_BUNDLE_DATA")
	assert.Contains(t, outStr, "ASGARD_CA_DATA")

	// Test fallback if hostBundlePath does not exist
	outPath2 := filepath.Join(tempDir, "merged2", "ca-certificates.crt")
	err = MergeCACert(filepath.Join(tempDir, "non-existent.crt"), caCertPath, outPath2)
	require.NoError(t, err)

	outBytes2, err := os.ReadFile(outPath2)
	require.NoError(t, err)
	assert.Contains(t, string(outBytes2), "ASGARD_CA_DATA")

	// Test concurrent writes to the same outPath (parallel subtasks sharing chat ID)
	concurrentOutPath := filepath.Join(tempDir, "concurrent", "ca-certificates.crt")
	done := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func() {
			done <- MergeCACert(hostBundlePath, caCertPath, concurrentOutPath)
		}()
	}
	for i := 0; i < 10; i++ {
		require.NoError(t, <-done)
	}
	concurrentBytes, err := os.ReadFile(concurrentOutPath)
	require.NoError(t, err)
	assert.Contains(t, string(concurrentBytes), "HOST_BUNDLE_DATA")
	assert.Contains(t, string(concurrentBytes), "ASGARD_CA_DATA")
}
