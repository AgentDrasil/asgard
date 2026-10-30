package kms

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/googleapis/gax-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/paths"
)

type fakeKMSClient struct {
	mu      sync.Mutex
	storage map[string]string // key: ciphertext string -> plaintext
	counter int
}

func newFakeKMSClient() *fakeKMSClient {
	return &fakeKMSClient{storage: make(map[string]string)}
}

func (f *fakeKMSClient) Encrypt(_ context.Context, req *kmspb.EncryptRequest, _ ...gax.CallOption) (*kmspb.EncryptResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counter++
	ct := fmt.Sprintf("encrypted-%d-%s", f.counter, string(req.Plaintext))
	f.storage[ct] = string(req.Plaintext)
	return &kmspb.EncryptResponse{
		Ciphertext: []byte(ct),
	}, nil
}

func (f *fakeKMSClient) Decrypt(_ context.Context, req *kmspb.DecryptRequest, _ ...gax.CallOption) (*kmspb.DecryptResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pt, ok := f.storage[string(req.Ciphertext)]
	if !ok {
		return nil, fmt.Errorf("ciphertext not found")
	}
	return &kmspb.DecryptResponse{
		Plaintext: []byte(pt),
	}, nil
}

func (f *fakeKMSClient) Close() error {
	return nil
}

func TestAESGCMCipher_TableDriven(t *testing.T) {
	t.Parallel()

	validKey := make([]byte, 32)
	for i := range validKey {
		validKey[i] = byte(i + 1)
	}

	tests := []struct {
		name      string
		plaintext string
	}{
		{"empty string", ""},
		{"simple text", "secret-api-key-12345"},
		{"special characters", "AIzaSy!@#$%^&*()_+{}[]:;\"'<>?,./~`"},
		{"multiline text", "line1\nline2\nline3\n"},
	}

	cipher, err := NewAESGCMCipher(validKey)
	require.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			encrypted, err := cipher.Encrypt(context.Background(), tt.plaintext)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(encrypted, PrefixGCM))
			assert.NotEqual(t, tt.plaintext, encrypted)

			decrypted, err := cipher.Decrypt(context.Background(), encrypted)
			require.NoError(t, err)
			assert.Equal(t, tt.plaintext, decrypted)
		})
	}
}

func TestAESGCMCipher_KeyLengthValidation(t *testing.T) {
	t.Parallel()

	_, errShort := NewAESGCMCipher([]byte("too-short"))
	require.Error(t, errShort)
	assert.Contains(t, errShort.Error(), "requires a 32-byte key")

	_, errLong := NewAESGCMCipher(make([]byte, 64))
	require.Error(t, errLong)
	assert.Contains(t, errLong.Error(), "requires a 32-byte key")
}

func TestAESGCMCipher_InvalidCiphertext(t *testing.T) {
	t.Parallel()

	validKey := sha256.Sum256([]byte("test-key"))
	cipher, err := NewAESGCMCipher(validKey[:])
	require.NoError(t, err)

	t.Run("wrong prefix", func(t *testing.T) {
		t.Parallel()
		_, err := cipher.Decrypt(context.Background(), "kms:v1:something")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected \"gcm:v1:\" prefix")
	})

	t.Run("invalid base64", func(t *testing.T) {
		t.Parallel()
		_, err := cipher.Decrypt(context.Background(), PrefixGCM+"!not-base64!")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to decode base64")
	})

	t.Run("too short raw bytes", func(t *testing.T) {
		t.Parallel()
		_, err := cipher.Decrypt(context.Background(), PrefixGCM+"AQID")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ciphertext too short")
	})
}

func TestGCPKMSCipher_TableDriven(t *testing.T) {
	t.Parallel()

	fakeClient := newFakeKMSClient()
	cipher := NewGCPKMSCipher(fakeClient, "projects/test/locations/global/keyRings/ring/cryptoKeys/key")

	tests := []struct {
		name      string
		plaintext string
	}{
		{"standard secret", "gemini-api-key-test-value"},
		{"empty secret", ""},
		{"unicode secret", "密钥-测试-12345"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			encrypted, err := cipher.Encrypt(context.Background(), tt.plaintext)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(encrypted, PrefixKMS))

			decrypted, err := cipher.Decrypt(context.Background(), encrypted)
			require.NoError(t, err)
			assert.Equal(t, tt.plaintext, decrypted)
		})
	}
}

func TestAutoCipher_RoutingAndFallback(t *testing.T) {
	t.Parallel()

	fakeClient := newFakeKMSClient()
	kmsCipher := NewGCPKMSCipher(fakeClient, "projects/test/locations/global/keyRings/ring/cryptoKeys/key")

	key := sha256.Sum256([]byte("fallback-test-key"))
	fbCipher, err := NewAESGCMCipher(key[:])
	require.NoError(t, err)

	t.Run("with KMS configured", func(t *testing.T) {
		t.Parallel()
		auto := &AutoCipher{
			kms:      kmsCipher,
			fallback: fbCipher,
		}

		// Encrypt should produce KMS ciphertext
		enc, err := auto.Encrypt(context.Background(), "my-kms-secret")
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(enc, PrefixKMS))

		// Decrypt KMS ciphertext
		dec, err := auto.Decrypt(context.Background(), enc)
		require.NoError(t, err)
		assert.Equal(t, "my-kms-secret", dec)

		// Decrypt GCM ciphertext also works
		gcmEnc, err := fbCipher.Encrypt(context.Background(), "my-gcm-secret")
		require.NoError(t, err)
		decGcm, err := auto.Decrypt(context.Background(), gcmEnc)
		require.NoError(t, err)
		assert.Equal(t, "my-gcm-secret", decGcm)
	})

	t.Run("without KMS configured (fallback only)", func(t *testing.T) {
		t.Parallel()
		auto := &AutoCipher{
			fallback: fbCipher,
		}

		// Encrypt should produce GCM ciphertext
		enc, err := auto.Encrypt(context.Background(), "my-fallback-secret")
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(enc, PrefixGCM))

		// Decrypt GCM ciphertext
		dec, err := auto.Decrypt(context.Background(), enc)
		require.NoError(t, err)
		assert.Equal(t, "my-fallback-secret", dec)

		// Decrypt KMS ciphertext without KMS returns error
		kmsEnc, err := kmsCipher.Encrypt(context.Background(), "kms-secret")
		require.NoError(t, err)
		_, errKMS := auto.Decrypt(context.Background(), kmsEnc)
		require.Error(t, errKMS)
		assert.Contains(t, errKMS.Error(), "GCP KMS is not configured")
	})

	t.Run("unknown prefix returns error", func(t *testing.T) {
		t.Parallel()
		auto := &AutoCipher{fallback: fbCipher}
		_, err := auto.Decrypt(context.Background(), "plain:value")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported ciphertext prefix")
	})
}

func TestAutoCipher_MasterKeyPersistenceAndFallback(t *testing.T) {
	t.Run("generates, persists, and reuses master key", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("ASGARD_ROOT", tmpDir)
		t.Setenv("ASGARD_ENCRYPTION_KEY", "")
		t.Setenv("ASGARD_KMS_KEY", "")
		t.Setenv("GCP_KMS_KEY", "")

		cipher1, err := NewAutoCipher(context.Background(), "", nil)
		require.NoError(t, err)

		keyPath := filepath.Join(paths.ConfigDir(), "master.key")
		fi, err := os.Stat(keyPath)
		require.NoError(t, err)
		assert.Equal(t, int64(32), fi.Size())
		assert.Equal(t, os.FileMode(0600), fi.Mode().Perm())

		ciphertext, err := cipher1.Encrypt(context.Background(), "persistent-secret-123")
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(ciphertext, PrefixGCM))

		// Create a second cipher instance simulating a restart
		cipher2, err := NewAutoCipher(context.Background(), "", nil)
		require.NoError(t, err)

		decrypted, err := cipher2.Decrypt(context.Background(), ciphertext)
		require.NoError(t, err)
		assert.Equal(t, "persistent-secret-123", decrypted)
	})

	t.Run("uses persistent master key for fallback even when KMS key is specified", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("ASGARD_ROOT", tmpDir)
		t.Setenv("ASGARD_ENCRYPTION_KEY", "")
		t.Setenv("ASGARD_KMS_KEY", "projects/test/locations/global/keyRings/ring/cryptoKeys/key")
		t.Setenv("GCP_KMS_KEY", "")

		cipher, err := NewAutoCipher(context.Background(), "", nil)
		require.NoError(t, err)

		keyPath := filepath.Join(paths.ConfigDir(), "master.key")
		fi, err := os.Stat(keyPath)
		require.NoError(t, err)
		assert.Equal(t, int64(32), fi.Size())

		auto, ok := cipher.(*AutoCipher)
		require.True(t, ok)
		require.NotNil(t, auto.fallback)

		enc, err := auto.fallback.Encrypt(context.Background(), "persistent-fallback-with-kms-configured")
		require.NoError(t, err)
		dec, err := auto.Decrypt(context.Background(), enc)
		require.NoError(t, err)
		assert.Equal(t, "persistent-fallback-with-kms-configured", dec)
	})

	t.Run("falls back to derived key when file cannot be written", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("ASGARD_ROOT", tmpDir)
		t.Setenv("ASGARD_ENCRYPTION_KEY", "")
		t.Setenv("ASGARD_KMS_KEY", "")
		t.Setenv("GCP_KMS_KEY", "")

		// Block directory creation by creating a regular file at ConfigDir path
		require.NoError(t, os.WriteFile(paths.ConfigDir(), []byte("not-a-directory"), 0600))

		cipher, err := NewAutoCipher(context.Background(), "", nil)
		require.NoError(t, err)

		ciphertext, err := cipher.Encrypt(context.Background(), "fallback-secret")
		require.NoError(t, err)

		decrypted, err := cipher.Decrypt(context.Background(), ciphertext)
		require.NoError(t, err)
		assert.Equal(t, "fallback-secret", decrypted)
	})
}
