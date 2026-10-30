package kms

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	kms "cloud.google.com/go/kms/apiv1"
	"cloud.google.com/go/kms/apiv1/kmspb"
	"github.com/googleapis/gax-go/v2"
	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/pkg/paths"
)

const (
	PrefixGCM = "gcm:v1:"
	PrefixKMS = "kms:v1:"

	defaultSalt = "asgard-default-local-fallback-encryption-salt"
)

// Cipher provides symmetric or envelope encryption and decryption for secrets.
type Cipher interface {
	Encrypt(ctx context.Context, plaintext string) (string, error)
	Decrypt(ctx context.Context, ciphertext string) (string, error)
}

// AESGCMCipher implements Cipher using AES-256-GCM.
type AESGCMCipher struct {
	key []byte
}

// NewAESGCMCipher creates an AES-256-GCM cipher with a 32-byte key.
func NewAESGCMCipher(key []byte) (*AESGCMCipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("AES-256-GCM requires a 32-byte key, got %d bytes", len(key))
	}
	keyCopy := make([]byte, 32)
	copy(keyCopy, key)
	return &AESGCMCipher{key: keyCopy}, nil
}

// Encrypt encrypts plaintext using AES-256-GCM and returns a string with PrefixGCM.
func (c *AESGCMCipher) Encrypt(_ context.Context, plaintext string) (string, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", fmt.Errorf("failed to create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate random nonce: %w", err)
	}

	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return PrefixGCM + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt decrypts ciphertext with PrefixGCM using AES-256-GCM.
func (c *AESGCMCipher) Decrypt(_ context.Context, ciphertext string) (string, error) {
	if !strings.HasPrefix(ciphertext, PrefixGCM) {
		return "", fmt.Errorf("invalid ciphertext: expected %q prefix", PrefixGCM)
	}
	rawB64 := strings.TrimPrefix(ciphertext, PrefixGCM)
	raw, err := base64.StdEncoding.DecodeString(rawB64)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64 ciphertext: %w", err)
	}

	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", fmt.Errorf("failed to create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short: missing nonce")
	}

	nonce, encrypted := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plainBytes, err := gcm.Open(nil, nonce, encrypted, nil)
	if err != nil {
		return "", fmt.Errorf("AES-GCM decryption failed: %w", err)
	}
	return string(plainBytes), nil
}

// KMSClient abstracts the GCP KMS client methods used by GCPKMSCipher.
type KMSClient interface {
	Encrypt(ctx context.Context, req *kmspb.EncryptRequest, opts ...gax.CallOption) (*kmspb.EncryptResponse, error)
	Decrypt(ctx context.Context, req *kmspb.DecryptRequest, opts ...gax.CallOption) (*kmspb.DecryptResponse, error)
	Close() error
}

// GCPKMSCipher implements Cipher using Google Cloud KMS.
type GCPKMSCipher struct {
	client  KMSClient
	keyName string
}

// NewGCPKMSCipher creates a new GCP KMS cipher for the given key resource name.
func NewGCPKMSCipher(client KMSClient, keyName string) *GCPKMSCipher {
	return &GCPKMSCipher{
		client:  client,
		keyName: keyName,
	}
}

// Encrypt encrypts plaintext using GCP KMS and returns a string with PrefixKMS.
func (c *GCPKMSCipher) Encrypt(ctx context.Context, plaintext string) (string, error) {
	if c.client == nil || c.keyName == "" {
		return "", errors.New("GCP KMS cipher is unconfigured")
	}
	resp, err := c.client.Encrypt(ctx, &kmspb.EncryptRequest{
		Name:      c.keyName,
		Plaintext: []byte(plaintext),
	})
	if err != nil {
		return "", fmt.Errorf("GCP KMS Encrypt failed: %w", err)
	}
	return PrefixKMS + base64.StdEncoding.EncodeToString(resp.Ciphertext), nil
}

// Decrypt decrypts ciphertext with PrefixKMS using GCP KMS.
func (c *GCPKMSCipher) Decrypt(ctx context.Context, ciphertext string) (string, error) {
	if !strings.HasPrefix(ciphertext, PrefixKMS) {
		return "", fmt.Errorf("invalid ciphertext: expected %q prefix", PrefixKMS)
	}
	if c.client == nil || c.keyName == "" {
		return "", errors.New("GCP KMS cipher is unconfigured")
	}
	rawB64 := strings.TrimPrefix(ciphertext, PrefixKMS)
	raw, err := base64.StdEncoding.DecodeString(rawB64)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64 ciphertext: %w", err)
	}
	resp, err := c.client.Decrypt(ctx, &kmspb.DecryptRequest{
		Name:       c.keyName,
		Ciphertext: raw,
	})
	if err != nil {
		return "", fmt.Errorf("GCP KMS Decrypt failed: %w", err)
	}
	return string(resp.Plaintext), nil
}

// AutoCipher dynamically routes Decrypt based on prefix and encrypts with KMS if available, otherwise AES-GCM.
type AutoCipher struct {
	kms      *GCPKMSCipher
	fallback *AESGCMCipher
}

func loadOrGenerateMasterKey() []byte {
	keyPath := filepath.Join(paths.ConfigDir(), "master.key")
	if data, err := os.ReadFile(keyPath); err == nil {
		if len(data) == 32 {
			return data
		}
		log.Warn().Str("path", keyPath).Int("bytes", len(data)).Msg("existing master key has invalid length (must be 32 bytes)")
	}

	newKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, newKey); err != nil {
		log.Warn().Err(err).Msg("failed to generate random master key; fallback insecure key is in use")
		hash := sha256.Sum256([]byte(defaultSalt))
		return hash[:]
	}

	if err := os.MkdirAll(paths.ConfigDir(), 0700); err != nil {
		log.Warn().Err(err).Str("path", keyPath).Msg("failed to create config directory for master key file; fallback insecure key is in use")
		hash := sha256.Sum256([]byte(defaultSalt))
		return hash[:]
	}

	if err := os.WriteFile(keyPath, newKey, 0600); err != nil {
		log.Warn().Err(err).Str("path", keyPath).Msg("failed to write master key file; fallback insecure key is in use")
		hash := sha256.Sum256([]byte(defaultSalt))
		return hash[:]
	}

	return newKey
}

// NewAutoCipher creates a Cipher with GCP KMS if configured, falling back to AES-256-GCM.
func NewAutoCipher(ctx context.Context, kmsKeyName string, fallbackKey []byte) (Cipher, error) {
	if kmsKeyName == "" {
		kmsKeyName = os.Getenv("ASGARD_KMS_KEY")
		if kmsKeyName == "" {
			kmsKeyName = os.Getenv("GCP_KMS_KEY")
		}
	}

	var kmsCipher *GCPKMSCipher
	if kmsKeyName != "" {
		client, kErr := kms.NewKeyManagementClient(ctx)
		if kErr != nil {
			log.Warn().Err(kErr).Str("key", kmsKeyName).Msg("failed to initialize GCP KMS client, using AES-GCM fallback")
		} else {
			kmsCipher = NewGCPKMSCipher(client, kmsKeyName)
		}
	}

	if kmsCipher == nil {
		log.Warn().Str("master_key_file", filepath.Join(paths.ConfigDir(), "master.key")).Msg("GCP KMS is not configured; user personal proxy secrets will be encrypted using local fallback master key")
	}

	key := fallbackKey
	if len(key) != 32 {
		envKey := os.Getenv("ASGARD_ENCRYPTION_KEY")
		if envKey != "" {
			hash := sha256.Sum256([]byte(envKey))
			key = hash[:]
		} else {
			key = loadOrGenerateMasterKey()
		}
	}

	fbCipher, err := NewAESGCMCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize AES-GCM fallback cipher: %w", err)
	}

	return &AutoCipher{
		kms:      kmsCipher,
		fallback: fbCipher,
	}, nil
}

// Encrypt encrypts plaintext using GCP KMS if available, otherwise AES-GCM fallback.
func (a *AutoCipher) Encrypt(ctx context.Context, plaintext string) (string, error) {
	if a.kms != nil {
		return a.kms.Encrypt(ctx, plaintext)
	}
	if a.fallback != nil {
		return a.fallback.Encrypt(ctx, plaintext)
	}
	return "", errors.New("no cipher configured")
}

// Decrypt inspects the prefix and decrypts with the matching cipher.
func (a *AutoCipher) Decrypt(ctx context.Context, ciphertext string) (string, error) {
	switch {
	case strings.HasPrefix(ciphertext, PrefixKMS):
		if a.kms == nil {
			return "", errors.New("cannot decrypt KMS ciphertext: GCP KMS is not configured")
		}
		return a.kms.Decrypt(ctx, ciphertext)
	case strings.HasPrefix(ciphertext, PrefixGCM):
		if a.fallback == nil {
			return "", errors.New("cannot decrypt AES-GCM ciphertext: fallback cipher is not configured")
		}
		return a.fallback.Decrypt(ctx, ciphertext)
	default:
		return "", fmt.Errorf("unknown or unsupported ciphertext prefix")
	}
}
