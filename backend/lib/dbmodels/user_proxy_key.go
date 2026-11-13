package dbmodels

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/AgentDrasil/asgard/backend/lib/kms"
	"github.com/AgentDrasil/asgard/fakebash"
)

var (
	keyNameRegex = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

	protectedEnvKeys = func() map[string]struct{} {
		systemKeys := []string{
			"PATH",
			"LD_PRELOAD",
			"LD_LIBRARY_PATH",
			"IFS",
			"SHELL",
			"HOME",
			"USER",
			"LOGNAME",
		}
		m := make(map[string]struct{}, len(systemKeys)+len(fakebash.ProtectedProxyEnvKeys))
		for _, k := range systemKeys {
			m[strings.ToUpper(k)] = struct{}{}
		}
		for _, k := range fakebash.ProtectedProxyEnvKeys {
			m[strings.ToUpper(k)] = struct{}{}
		}
		return m
	}()
)

// ValidateKeyName validates that keyName matches ^[A-Za-z_][A-Za-z0-9_]*$, is at most 128 characters,
// and does not collide with reserved proxy or system environment variables.
func ValidateKeyName(keyName string) error {
	if keyName == "" {
		return errors.New("key_name is required")
	}
	if len(keyName) > 128 {
		return errors.New("key_name must not exceed 128 characters")
	}
	if !keyNameRegex.MatchString(keyName) {
		return errors.New("key_name must contain only letters, numbers, and underscores, and begin with a letter or underscore")
	}
	if _, protected := protectedEnvKeys[strings.ToUpper(keyName)]; protected {
		return fmt.Errorf("key_name %q is reserved and cannot be configured", keyName)
	}
	return nil
}

// UserProxyKeyRule tells the proxy where a user's key is swapped in: requests
// to Host (optionally under PathPrefix) carry the key in HeaderKey. An empty
// Host means the key relies on an admin proxy rule with the same env name.
type UserProxyKeyRule struct {
	Host       string `json:"host"`
	PathPrefix string `json:"path_prefix"`
	HeaderKey  string `json:"header_key"`
}

var (
	hostRegex      = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$`)
	headerKeyRegex = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

// Normalize trims the rule and applies defaults (Authorization header).
func (r UserProxyKeyRule) Normalize() UserProxyKeyRule {
	r.Host = strings.ToLower(strings.TrimSpace(r.Host))
	r.PathPrefix = strings.TrimSpace(r.PathPrefix)
	r.HeaderKey = strings.TrimSpace(r.HeaderKey)
	if r.Host != "" && r.HeaderKey == "" {
		r.HeaderKey = "Authorization"
	}
	return r
}

// Validate checks a normalized rule.
func (r UserProxyKeyRule) Validate() error {
	if r.Host == "" {
		if r.PathPrefix != "" || r.HeaderKey != "" {
			return errors.New("host is required when path_prefix or header_key is set")
		}
		return nil
	}
	if len(r.Host) > 255 || !hostRegex.MatchString(r.Host) {
		return errors.New("host must be a bare hostname such as api.example.com")
	}
	if r.PathPrefix != "" && !strings.HasPrefix(r.PathPrefix, "/") {
		return errors.New("path_prefix must start with /")
	}
	if len(r.PathPrefix) > 512 {
		return errors.New("path_prefix is too long")
	}
	if len(r.HeaderKey) > 128 || !headerKeyRegex.MatchString(r.HeaderKey) {
		return errors.New("header_key is not a valid HTTP header name")
	}
	return nil
}

// UserProxyKey stores an encrypted proxy/API key belonging to a user.
type UserProxyKey struct {
	UserSubject    string    `gorm:"primaryKey;column:user_subject;type:varchar(255);index" json:"user_subject"`
	KeyName        string    `gorm:"primaryKey;column:key_name;type:varchar(255);index" json:"key_name"`
	EncryptedValue string    `gorm:"column:encrypted_value;type:text;not null" json:"-"`
	Host           string    `gorm:"column:host;type:varchar(255);not null;default:''" json:"host"`
	PathPrefix     string    `gorm:"column:path_prefix;type:varchar(512);not null;default:''" json:"path_prefix"`
	HeaderKey      string    `gorm:"column:header_key;type:varchar(128);not null;default:''" json:"header_key"`
	CreatedAt      time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (UserProxyKey) TableName() string {
	return "user_proxy_keys"
}

// UserProxyKeyMeta holds metadata for a user proxy key without revealing the encrypted value.
type UserProxyKeyMeta struct {
	KeyName    string    `json:"key_name"`
	Host       string    `json:"host"`
	PathPrefix string    `json:"path_prefix"`
	HeaderKey  string    `json:"header_key"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// UserProxyKeyEntry is a decrypted key together with its swap rule.
type UserProxyKeyEntry struct {
	UserProxyKeyRule
	KeyName string
	Value   string
}

// UserProxyKeyRepository handles encrypted storage and retrieval of personal proxy keys.
type UserProxyKeyRepository struct {
	db     *gorm.DB
	cipher kms.Cipher
}

// NewUserProxyKeyRepository creates a repository for user proxy keys.
func NewUserProxyKeyRepository(db *gorm.DB, cipher kms.Cipher) *UserProxyKeyRepository {
	return &UserProxyKeyRepository{
		db:     db,
		cipher: cipher,
	}
}

// ListKeyMetadata returns metadata of all configured proxy keys for a user.
func (r *UserProxyKeyRepository) ListKeyMetadata(ctx context.Context, userSubject string) ([]UserProxyKeyMeta, error) {
	if r.db == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	var keys []UserProxyKey
	if err := r.db.WithContext(ctx).
		Select("key_name, host, path_prefix, header_key, updated_at").
		Where("user_subject = ?", userSubject).
		Order("key_name ASC").
		Find(&keys).Error; err != nil {
		return nil, fmt.Errorf("failed to query user proxy keys: %w", err)
	}

	res := make([]UserProxyKeyMeta, 0, len(keys))
	for _, k := range keys {
		res = append(res, UserProxyKeyMeta{
			KeyName:    k.KeyName,
			Host:       k.Host,
			PathPrefix: k.PathPrefix,
			HeaderKey:  k.HeaderKey,
			UpdatedAt:  k.UpdatedAt,
		})
	}
	return res, nil
}

// GetAllDecryptedKeys retrieves and decrypts all proxy keys for a user.
func (r *UserProxyKeyRepository) GetAllDecryptedKeys(ctx context.Context, userSubject string) (map[string]string, error) {
	entries, err := r.GetAllDecryptedEntries(ctx, userSubject)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(entries))
	for _, e := range entries {
		result[e.KeyName] = e.Value
	}
	return result, nil
}

// GetAllDecryptedEntries retrieves and decrypts all proxy keys for a user with
// their swap rules. Keys that fail to decrypt are skipped.
func (r *UserProxyKeyRepository) GetAllDecryptedEntries(ctx context.Context, userSubject string) ([]UserProxyKeyEntry, error) {
	if r.db == nil {
		return nil, fmt.Errorf("database unavailable")
	}
	var records []UserProxyKey
	if err := r.db.WithContext(ctx).
		Where("user_subject = ?", userSubject).
		Order("key_name ASC").
		Find(&records).Error; err != nil {
		return nil, fmt.Errorf("failed to query user proxy keys: %w", err)
	}

	result := make([]UserProxyKeyEntry, 0, len(records))
	for _, rec := range records {
		if r.cipher == nil {
			return nil, fmt.Errorf("cipher unavailable to decrypt user proxy key")
		}
		plain, err := r.cipher.Decrypt(ctx, rec.EncryptedValue)
		if err != nil {
			log.Warn().Err(err).Str("user", userSubject).Str("key_name", rec.KeyName).Msg("failed to decrypt user proxy key, skipping")
			continue
		}
		result = append(result, UserProxyKeyEntry{
			UserProxyKeyRule: UserProxyKeyRule{Host: rec.Host, PathPrefix: rec.PathPrefix, HeaderKey: rec.HeaderKey},
			KeyName:          rec.KeyName,
			Value:            plain,
		})
	}
	return result, nil
}

// SaveKey encrypts and saves or updates a user proxy key.
func (r *UserProxyKeyRepository) SaveKey(ctx context.Context, userSubject string, keyName string, value string) error {
	return r.SaveKeyWithRule(ctx, userSubject, keyName, value, UserProxyKeyRule{})
}

// SaveKeyWithRule is SaveKey plus where the proxy should swap the key in.
func (r *UserProxyKeyRepository) SaveKeyWithRule(ctx context.Context, userSubject string, keyName string, value string, rule UserProxyKeyRule) error {
	if err := ValidateKeyName(keyName); err != nil {
		return err
	}
	rule = rule.Normalize()
	if err := rule.Validate(); err != nil {
		return err
	}
	if r.db == nil {
		return fmt.Errorf("database unavailable")
	}
	if r.cipher == nil {
		return fmt.Errorf("cipher unavailable to encrypt user proxy key")
	}
	encrypted, err := r.cipher.Encrypt(ctx, value)
	if err != nil {
		return fmt.Errorf("failed to encrypt user proxy key %q: %w", keyName, err)
	}

	now := time.Now().UTC()
	record := UserProxyKey{
		UserSubject:    userSubject,
		KeyName:        keyName,
		EncryptedValue: encrypted,
		Host:           rule.Host,
		PathPrefix:     rule.PathPrefix,
		HeaderKey:      rule.HeaderKey,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "user_subject"},
			{Name: "key_name"},
		},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"encrypted_value": encrypted,
			"host":            rule.Host,
			"path_prefix":     rule.PathPrefix,
			"header_key":      rule.HeaderKey,
			"updated_at":      now,
		}),
	}).Create(&record).Error
}

// DeleteKey removes a user proxy key.
func (r *UserProxyKeyRepository) DeleteKey(ctx context.Context, userSubject string, keyName string) error {
	if r.db == nil {
		return fmt.Errorf("database unavailable")
	}
	return r.db.WithContext(ctx).
		Where("user_subject = ? AND key_name = ?", userSubject, keyName).
		Delete(&UserProxyKey{}).Error
}
