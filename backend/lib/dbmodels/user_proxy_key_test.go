package dbmodels

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/AgentDrasil/asgard/backend/lib/db"
	"github.com/AgentDrasil/asgard/backend/lib/kms"
)

func newTestUserProxyKeyRepo(t *testing.T) (*UserProxyKeyRepository, *gorm.DB) {
	t.Helper()
	database := db.NewDBForTest(t)
	require.NoError(t, AutoMigrate(database))

	key := sha256.Sum256([]byte("test-aes-key-32-bytes-long-now!"))
	c, err := kms.NewAESGCMCipher(key[:])
	require.NoError(t, err)

	repo := NewUserProxyKeyRepository(database, c)
	return repo, database
}

func TestUserProxyKeyRepository_CRUD(t *testing.T) {
	t.Parallel()

	repo, db := newTestUserProxyKeyRepo(t)
	ctx := context.Background()

	// 1. Save keys for user-1
	err := repo.SaveKey(ctx, "user-1", "gemini_key", "secret-value-1")
	require.NoError(t, err)

	err = repo.SaveKey(ctx, "user-1", "OPENAI_API_KEY", "secret-value-2")
	require.NoError(t, err)

	// Verify plaintext is not stored in DB
	var rawRecord UserProxyKey
	err = db.First(&rawRecord, "user_subject = ? AND key_name = ?", "user-1", "gemini_key").Error
	require.NoError(t, err)
	assert.NotEqual(t, "secret-value-1", rawRecord.EncryptedValue)
	assert.Contains(t, rawRecord.EncryptedValue, kms.PrefixGCM)

	// 2. List metadata
	metas, err := repo.ListKeyMetadata(ctx, "user-1")
	require.NoError(t, err)
	require.Len(t, metas, 2)
	assert.Equal(t, "OPENAI_API_KEY", metas[0].KeyName)
	assert.Equal(t, "gemini_key", metas[1].KeyName)

	// 3. GetAllDecryptedKeys
	decrypted, err := repo.GetAllDecryptedKeys(ctx, "user-1")
	require.NoError(t, err)
	assert.Equal(t, "secret-value-1", decrypted["gemini_key"])
	assert.Equal(t, "secret-value-2", decrypted["OPENAI_API_KEY"])

	// 4. Update key
	err = repo.SaveKey(ctx, "user-1", "gemini_key", "updated-secret-value-1")
	require.NoError(t, err)

	decryptedUpdated, err := repo.GetAllDecryptedKeys(ctx, "user-1")
	require.NoError(t, err)
	assert.Equal(t, "updated-secret-value-1", decryptedUpdated["gemini_key"])

	// 5. Delete key
	err = repo.DeleteKey(ctx, "user-1", "OPENAI_API_KEY")
	require.NoError(t, err)

	metasAfterDelete, err := repo.ListKeyMetadata(ctx, "user-1")
	require.NoError(t, err)
	require.Len(t, metasAfterDelete, 1)
	assert.Equal(t, "gemini_key", metasAfterDelete[0].KeyName)
}

func TestUserProxyKeyRepository_UserIsolation(t *testing.T) {
	t.Parallel()

	repo, _ := newTestUserProxyKeyRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.SaveKey(ctx, "user-A", "KEY_A", "val-A"))
	require.NoError(t, repo.SaveKey(ctx, "user-B", "KEY_B", "val-B"))

	keysA, err := repo.GetAllDecryptedKeys(ctx, "user-A")
	require.NoError(t, err)
	assert.Contains(t, keysA, "KEY_A")
	assert.NotContains(t, keysA, "KEY_B")

	keysB, err := repo.GetAllDecryptedKeys(ctx, "user-B")
	require.NoError(t, err)
	assert.Contains(t, keysB, "KEY_B")
	assert.NotContains(t, keysB, "KEY_A")
}

func TestSession_UserSubjectRecorded(t *testing.T) {
	t.Parallel()

	database := db.NewDBForTest(t)
	require.NoError(t, AutoMigrate(database))

	repo := NewSessionRepository(database)
	chatID := "test-chat-subject-1"

	err := repo.UpdateAgentSession(chatID, "test-agent", "", "", nil, "user-sub-1234")
	require.NoError(t, err)

	meta, err := repo.GetSessionMeta(chatID)
	require.NoError(t, err)
	require.NotNil(t, meta)
	assert.Equal(t, "user-sub-1234", meta.UserSubject)
}

func TestValidateKeyName_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		keyName   string
		wantErr   bool
		errSubstr string
	}{
		{"valid uppercase with underscore", "GEMINI_API_KEY", false, ""},
		{"valid lowercase", "my_custom_key", false, ""},
		{"valid leading underscore", "_SECRET_1", false, ""},
		{"valid max length 128", "A" + strings.Repeat("B", 127), false, ""},
		{"empty string", "", true, "key_name is required"},
		{"exceeds max length 128", "A" + strings.Repeat("B", 128), true, "must not exceed 128 characters"},
		{"contains hyphen", "gemini-key", true, "must contain only letters, numbers, and underscores"},
		{"contains period", "my.key", true, "must contain only letters, numbers, and underscores"},
		{"starts with digit", "1_KEY", true, "begin with a letter or underscore"},
		{"denylisted PATH uppercase", "PATH", true, "is reserved"},
		{"denylisted path lowercase", "path", true, "is reserved"},
		{"denylisted HTTP_PROXY", "HTTP_PROXY", true, "is reserved"},
		{"denylisted http_proxy", "http_proxy", true, "is reserved"},
		{"denylisted LD_PRELOAD", "LD_PRELOAD", true, "is reserved"},
		{"denylisted SSL_CERT_FILE", "SSL_CERT_FILE", true, "is reserved"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateKeyName(tt.keyName)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.errSubstr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestUserProxyKeyRepository_ResilientDecryption(t *testing.T) {
	t.Parallel()

	repo, database := newTestUserProxyKeyRepo(t)
	ctx := context.Background()

	// Save one valid key
	require.NoError(t, repo.SaveKey(ctx, "user-resilient", "VALID_KEY", "secret-value-valid"))

	// Manually insert a key with corrupted/invalid ciphertext
	corrupted := UserProxyKey{
		UserSubject:    "user-resilient",
		KeyName:        "CORRUPTED_KEY",
		EncryptedValue: kms.PrefixGCM + "not-valid-base64-or-ciphertext",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	require.NoError(t, database.Create(&corrupted).Error)

	// GetAllDecryptedKeys should succeed, log warning, skip corrupted key, and return valid key
	keys, err := repo.GetAllDecryptedKeys(ctx, "user-resilient")
	require.NoError(t, err)
	assert.Equal(t, "secret-value-valid", keys["VALID_KEY"])
	assert.NotContains(t, keys, "CORRUPTED_KEY")
}
