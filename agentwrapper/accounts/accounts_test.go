package accounts

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoot(t *testing.T) {
	// Cannot call t.Parallel() when modifying environment variables.
	origEnv := os.Getenv(EnvAccountsDir)
	t.Cleanup(func() {
		_ = os.Setenv(EnvAccountsDir, origEnv)
	})

	_ = os.Unsetenv(EnvAccountsDir)
	assert.Equal(t, DefaultAccountsDir, Root())

	_ = os.Setenv(EnvAccountsDir, "/custom/accounts")
	assert.Equal(t, "/custom/accounts", Root())
}

func TestValidateCLI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cli     string
		wantErr bool
	}{
		{"valid agy", "agy", false},
		{"valid claude", "claude", false},
		{"empty cli", "", true},
		{"unsupported opencode", "opencode", true},
		{"unsupported simplest", "simplest", true},
		{"path traversal in cli", "../agy", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCLI(tt.cli)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateAlias(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		alias   string
		wantErr bool
	}{
		{"single char lower", "a", false},
		{"single digit", "0", false},
		{"standard alias", "my-account-1", false},
		{"with underscores and hyphens", "dev_team-prod_01", false},
		{"max length 32 chars", "a1234567890123456789012345678901", false},
		{"empty string", "", true},
		{"starts with hyphen", "-invalid", true},
		{"starts with underscore", "_invalid", true},
		{"uppercase letter", "myAccount", true},
		{"uppercase start", "Account", true},
		{"contains dot", "my.account", true},
		{"contains slash", "my/account", true},
		{"path traversal ..", "..", true},
		{"path traversal ../foo", "../foo", true},
		{"contains space", "my account", true},
		{"exceeds max length (33 chars)", "a12345678901234567890123456789012", true},
		{"special character @", "user@domain", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAlias(tt.alias)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDir(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	tests := []struct {
		name        string
		envRoot     string
		cli         string
		alias       string
		expectedDir string
		wantErr     bool
	}{
		{
			name:        "valid agy dir",
			envRoot:     tempDir,
			cli:         "agy",
			alias:       "account1",
			expectedDir: filepath.Join(tempDir, "agy", "account1"),
			wantErr:     false,
		},
		{
			name:        "valid claude dir",
			envRoot:     tempDir,
			cli:         "claude",
			alias:       "team-a",
			expectedDir: filepath.Join(tempDir, "claude", "team-a"),
			wantErr:     false,
		},
		{
			name:    "invalid cli",
			envRoot: tempDir,
			cli:     "unknown",
			alias:   "acc",
			wantErr: true,
		},
		{
			name:    "invalid alias",
			envRoot: tempDir,
			cli:     "agy",
			alias:   "../traversal",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Subtests that set env cannot run in parallel
			orig := os.Getenv(EnvAccountsDir)
			_ = os.Setenv(EnvAccountsDir, tt.envRoot)
			defer func() {
				_ = os.Setenv(EnvAccountsDir, orig)
			}()

			dir, err := Dir(tt.cli, tt.alias)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.expectedDir, dir)
		})
	}
}

func TestTokenPath(t *testing.T) {
	tempDir := t.TempDir()
	orig := os.Getenv(EnvAccountsDir)
	_ = os.Setenv(EnvAccountsDir, tempDir)
	t.Cleanup(func() {
		_ = os.Setenv(EnvAccountsDir, orig)
	})

	t.Run("valid alias", func(t *testing.T) {
		path, err := TokenPath("default")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(tempDir, "agy", "default", TokenFileName), path)
	})

	t.Run("invalid alias", func(t *testing.T) {
		_, err := TokenPath("../bad")
		require.Error(t, err)
	})
}

func TestList(t *testing.T) {
	tempDir := t.TempDir()
	orig := os.Getenv(EnvAccountsDir)
	_ = os.Setenv(EnvAccountsDir, tempDir)
	t.Cleanup(func() {
		_ = os.Setenv(EnvAccountsDir, orig)
	})

	t.Run("unsupported cli returns error", func(t *testing.T) {
		_, err := List("invalid")
		require.Error(t, err)
	})

	t.Run("missing directory returns empty list without error", func(t *testing.T) {
		aliases, err := List("agy")
		require.NoError(t, err)
		assert.Equal(t, []string{}, aliases)
	})

	t.Run("returns sorted aliases excluding files and invalid dirs", func(t *testing.T) {
		agyDir := filepath.Join(tempDir, "agy")
		require.NoError(t, os.MkdirAll(filepath.Join(agyDir, "charlie"), 0700))
		require.NoError(t, os.MkdirAll(filepath.Join(agyDir, "alpha"), 0700))
		require.NoError(t, os.MkdirAll(filepath.Join(agyDir, "bravo"), 0700))
		require.NoError(t, os.MkdirAll(filepath.Join(agyDir, ".hidden"), 0700))
		// Regular file, should not be included
		require.NoError(t, os.WriteFile(filepath.Join(agyDir, "file-not-dir"), []byte("data"), 0600))

		aliases, err := List("agy")
		require.NoError(t, err)
		assert.Equal(t, []string{"alpha", "bravo", "charlie"}, aliases)
	})
}

func TestRemove(t *testing.T) {
	tempDir := t.TempDir()
	orig := os.Getenv(EnvAccountsDir)
	_ = os.Setenv(EnvAccountsDir, tempDir)
	t.Cleanup(func() {
		_ = os.Setenv(EnvAccountsDir, orig)
	})

	t.Run("unsupported cli returns error", func(t *testing.T) {
		err := Remove("invalid", "test")
		require.Error(t, err)
	})

	t.Run("invalid alias returns error", func(t *testing.T) {
		err := Remove("agy", "../test")
		require.Error(t, err)
	})

	t.Run("non-existent account returns ErrNotExist", func(t *testing.T) {
		err := Remove("agy", "non-existent")
		require.Error(t, err)
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("existing account is deleted", func(t *testing.T) {
		accDir := filepath.Join(tempDir, "claude", "test-acc")
		require.NoError(t, os.MkdirAll(accDir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(accDir, "creds"), []byte("secret"), 0600))

		err := Remove("claude", "test-acc")
		require.NoError(t, err)

		_, statErr := os.Stat(accDir)
		assert.True(t, os.IsNotExist(statErr))
	})
}

func TestSelect(t *testing.T) {
	root := t.TempDir()
	t.Setenv(EnvAccountsDir, root)
	t.Setenv("ASGARD_AGY_ACCOUNT", "")

	_, ok, err := Select("agy")
	require.NoError(t, err)
	assert.False(t, ok)

	for _, a := range []string{"work", "alpha"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "agy", a), DirMode))
	}
	alias, ok, err := Select("agy")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "alpha", alias)

	t.Setenv("ASGARD_AGY_ACCOUNT", "work")
	alias, ok, err = Select("agy")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "work", alias)

	t.Setenv("ASGARD_AGY_ACCOUNT", "missing")
	_, _, err = Select("agy")
	assert.Error(t, err)

	t.Setenv("ASGARD_AGY_ACCOUNT", "../x")
	_, _, err = Select("agy")
	assert.Error(t, err)

	_, _, err = Select("bogus")
	assert.Error(t, err)
}
