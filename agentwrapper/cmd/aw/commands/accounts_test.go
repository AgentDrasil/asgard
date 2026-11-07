package commands

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/accounts"
)

func TestCobraDispatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		args        []string
		expectedCmd *cobra.Command
	}{
		{"agy with flags dispatches to agyCmd", []string{"agy", "-p", "test"}, agyCmd},
		{"agy login dispatches to agyLoginCmd", []string{"agy", "login", "alias1"}, agyLoginCmd},
		{"agy list dispatches to agyListCmd", []string{"agy", "list"}, agyListCmd},
		{"agy remove dispatches to agyRemoveCmd", []string{"agy", "remove", "alias1"}, agyRemoveCmd},
		{"claude with flags dispatches to claudeCmd", []string{"claude", "-p", "test"}, claudeCmd},
		{"claude login dispatches to claudeLoginCmd", []string{"claude", "login", "alias1"}, claudeLoginCmd},
		{"claude list dispatches to claudeListCmd", []string{"claude", "list"}, claudeListCmd},
		{"claude remove dispatches to claudeRemoveCmd", []string{"claude", "remove", "alias1"}, claudeRemoveCmd},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd, _, err := rootCmd.Find(tt.args)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedCmd, cmd)
		})
	}
}

func TestAccountCommandsSkipConfig(t *testing.T) {
	t.Parallel()

	accountCmds := []*cobra.Command{
		agyLoginCmd, agyListCmd, agyRemoveCmd,
		claudeLoginCmd, claudeListCmd, claudeRemoveCmd,
	}

	for _, cmd := range accountCmds {
		t.Run(cmd.CommandPath(), func(t *testing.T) {
			t.Parallel()
			assert.True(t, isAccountCmd(cmd), "%s should be recognized as account command", cmd.Name())

			// Calling rootCmd's PersistentPreRunE with an account command must not fail
			// even when configuration is not initialized or invalid.
			err := rootCmd.PersistentPreRunE(cmd, []string{})
			assert.NoError(t, err)
		})
	}

	nonAccountCmds := []*cobra.Command{
		rootCmd, agyCmd, claudeCmd, opencodeCmd, simplestCmd,
	}
	for _, cmd := range nonAccountCmds {
		t.Run("non-account "+cmd.Name(), func(t *testing.T) {
			t.Parallel()
			assert.False(t, isAccountCmd(cmd), "%s should not be recognized as account command", cmd.Name())
		})
	}
}

func TestAgyLogin(t *testing.T) {
	tempAccounts := t.TempDir()
	origAccounts := os.Getenv(accounts.EnvAccountsDir)
	_ = os.Setenv(accounts.EnvAccountsDir, tempAccounts)
	t.Cleanup(func() {
		_ = os.Setenv(accounts.EnvAccountsDir, origAccounts)
	})

	origExec := execCLI
	t.Cleanup(func() {
		execCLI = origExec
	})

	t.Run("successful login produces token and prints alias", func(t *testing.T) {
		var observedTempHome string
		execCLI = func(ctx context.Context, name string, env []string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
			assert.Equal(t, "agy", name)
			for _, e := range env {
				if strings.HasPrefix(e, "HOME=") {
					observedTempHome = strings.TrimPrefix(e, "HOME=")
					break
				}
			}
			require.NotEmpty(t, observedTempHome)

			// Verify temp home exists and is 0700
			info, err := os.Stat(observedTempHome)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0700), info.Mode().Perm())

			// Write fake token inside temp home
			tokenDir := filepath.Join(observedTempHome, ".gemini", "antigravity-cli")
			require.NoError(t, os.MkdirAll(tokenDir, 0700))
			return os.WriteFile(filepath.Join(tokenDir, "antigravity-oauth-token"), []byte("secret-agy-token"), 0600)
		}

		buf := new(bytes.Buffer)
		agyLoginCmd.SetOut(buf)
		defer agyLoginCmd.SetOut(nil)
		agyLoginForce = false

		err := agyLoginCmd.RunE(agyLoginCmd, []string{"my-alias"})
		require.NoError(t, err)

		assert.Equal(t, "my-alias\n", buf.String())

		// Verify target token exists with 0600 permissions
		tokenPath, err := accounts.TokenPath("my-alias")
		require.NoError(t, err)
		tokenInfo, err := os.Stat(tokenPath)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), tokenInfo.Mode().Perm())

		content, err := os.ReadFile(tokenPath)
		require.NoError(t, err)
		assert.Equal(t, "secret-agy-token", string(content))

		// Verify target account dir has 0700 permissions
		accountDir, err := accounts.Dir("agy", "my-alias")
		require.NoError(t, err)
		dirInfo, err := os.Stat(accountDir)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0700), dirInfo.Mode().Perm())

		// Verify temp home was removed
		_, statErr := os.Stat(observedTempHome)
		assert.True(t, os.IsNotExist(statErr))
	})

	t.Run("fails if alias already exists without force", func(t *testing.T) {
		buf := new(bytes.Buffer)
		agyLoginCmd.SetOut(buf)
		defer agyLoginCmd.SetOut(nil)
		agyLoginForce = false

		err := agyLoginCmd.RunE(agyLoginCmd, []string{"my-alias"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already exists")
	})

	t.Run("succeeds if alias already exists with force", func(t *testing.T) {
		execCLI = func(ctx context.Context, name string, env []string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
			var home string
			for _, e := range env {
				if strings.HasPrefix(e, "HOME=") {
					home = strings.TrimPrefix(e, "HOME=")
					break
				}
			}
			tokenDir := filepath.Join(home, ".gemini", "antigravity-cli")
			require.NoError(t, os.MkdirAll(tokenDir, 0700))
			return os.WriteFile(filepath.Join(tokenDir, "antigravity-oauth-token"), []byte("new-token"), 0600)
		}

		buf := new(bytes.Buffer)
		agyLoginCmd.SetOut(buf)
		defer agyLoginCmd.SetOut(nil)
		agyLoginForce = true
		defer func() { agyLoginForce = false }()

		err := agyLoginCmd.RunE(agyLoginCmd, []string{"my-alias"})
		require.NoError(t, err)

		tokenPath, err := accounts.TokenPath("my-alias")
		require.NoError(t, err)
		content, err := os.ReadFile(tokenPath)
		require.NoError(t, err)
		assert.Equal(t, "new-token", string(content))
	})

	t.Run("fails if token not produced and cleans up temp home", func(t *testing.T) {
		var observedTempHome string
		execCLI = func(ctx context.Context, name string, env []string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
			for _, e := range env {
				if strings.HasPrefix(e, "HOME=") {
					observedTempHome = strings.TrimPrefix(e, "HOME=")
					break
				}
			}
			// Do not write token
			return nil
		}

		agyLoginForce = false
		err := agyLoginCmd.RunE(agyLoginCmd, []string{"no-token-alias"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "login did not produce a token")

		// Verify temp home was removed
		_, statErr := os.Stat(observedTempHome)
		assert.True(t, os.IsNotExist(statErr))
	})
}

func TestClaudeLogin(t *testing.T) {
	tempAccounts := t.TempDir()
	origAccounts := os.Getenv(accounts.EnvAccountsDir)
	_ = os.Setenv(accounts.EnvAccountsDir, tempAccounts)
	t.Cleanup(func() {
		_ = os.Setenv(accounts.EnvAccountsDir, origAccounts)
	})

	origExec := execCLI
	t.Cleanup(func() {
		execCLI = origExec
	})

	t.Run("successful login produces .credentials.json and prints alias", func(t *testing.T) {
		execCLI = func(ctx context.Context, name string, env []string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
			assert.Equal(t, "claude", name)
			var configDir string
			for _, e := range env {
				if strings.HasPrefix(e, "CLAUDE_CONFIG_DIR=") {
					configDir = strings.TrimPrefix(e, "CLAUDE_CONFIG_DIR=")
					break
				}
			}
			require.NotEmpty(t, configDir)

			// Verify config dir exists with 0700
			info, err := os.Stat(configDir)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0700), info.Mode().Perm())

			// Write credentials file
			return os.WriteFile(filepath.Join(configDir, ".credentials.json"), []byte(`{"claude_token":"abc"}`), 0600)
		}

		buf := new(bytes.Buffer)
		claudeLoginCmd.SetOut(buf)
		defer claudeLoginCmd.SetOut(nil)
		claudeLoginForce = false

		err := claudeLoginCmd.RunE(claudeLoginCmd, []string{"claude-work"})
		require.NoError(t, err)
		assert.Equal(t, "claude-work\n", buf.String())

		// Verify credentials file exists
		accountDir, err := accounts.Dir("claude", "claude-work")
		require.NoError(t, err)
		credsPath := filepath.Join(accountDir, ".credentials.json")
		assert.FileExists(t, credsPath)

		dirInfo, err := os.Stat(accountDir)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0700), dirInfo.Mode().Perm())
	})

	t.Run("fails if alias exists without force", func(t *testing.T) {
		claudeLoginForce = false
		err := claudeLoginCmd.RunE(claudeLoginCmd, []string{"claude-work"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already exists")
	})

	t.Run("succeeds if alias exists with force", func(t *testing.T) {
		execCLI = func(ctx context.Context, name string, env []string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
			var configDir string
			for _, e := range env {
				if strings.HasPrefix(e, "CLAUDE_CONFIG_DIR=") {
					configDir = strings.TrimPrefix(e, "CLAUDE_CONFIG_DIR=")
					break
				}
			}
			return os.WriteFile(filepath.Join(configDir, ".credentials.json"), []byte(`{"claude_token":"new"}`), 0600)
		}

		buf := new(bytes.Buffer)
		claudeLoginCmd.SetOut(buf)
		defer claudeLoginCmd.SetOut(nil)

		claudeLoginForce = true
		defer func() { claudeLoginForce = false }()

		err := claudeLoginCmd.RunE(claudeLoginCmd, []string{"claude-work"})
		require.NoError(t, err)
		assert.Equal(t, "claude-work\n", buf.String())
	})

	t.Run("fails if .credentials.json missing and removes created dir", func(t *testing.T) {
		execCLI = func(ctx context.Context, name string, env []string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
			// Do not write .credentials.json
			return nil
		}

		claudeLoginForce = false
		accountDir, err := accounts.Dir("claude", "failed-login")
		require.NoError(t, err)

		err = claudeLoginCmd.RunE(claudeLoginCmd, []string{"failed-login"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "login did not produce .credentials.json")

		// Verify directory was removed
		_, statErr := os.Stat(accountDir)
		assert.True(t, os.IsNotExist(statErr))
	})

	t.Run("failed force relogin keeps the old credentials and leaves no staging dir", func(t *testing.T) {
		execCLI = func(ctx context.Context, name string, env []string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
			return nil // user aborted: nothing written
		}
		accountDir, err := accounts.Dir("claude", "claude-work")
		require.NoError(t, err)
		before, err := os.ReadFile(filepath.Join(accountDir, ".credentials.json"))
		require.NoError(t, err)

		claudeLoginForce = true
		defer func() { claudeLoginForce = false }()
		err = claudeLoginCmd.RunE(claudeLoginCmd, []string{"claude-work"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "login did not produce .credentials.json")

		after, err := os.ReadFile(filepath.Join(accountDir, ".credentials.json"))
		require.NoError(t, err)
		assert.Equal(t, before, after)
		entries, err := os.ReadDir(filepath.Dir(accountDir))
		require.NoError(t, err)
		for _, e := range entries {
			assert.False(t, strings.HasPrefix(e.Name(), ".login-"), "staging dir leaked: %s", e.Name())
		}
	})
}

func TestListAndRemoveCommands(t *testing.T) {
	tempAccounts := t.TempDir()
	origAccounts := os.Getenv(accounts.EnvAccountsDir)
	_ = os.Setenv(accounts.EnvAccountsDir, tempAccounts)
	t.Cleanup(func() {
		_ = os.Setenv(accounts.EnvAccountsDir, origAccounts)
	})

	// Setup agy accounts: alpha, beta
	agyDir := filepath.Join(tempAccounts, "agy")
	require.NoError(t, os.MkdirAll(filepath.Join(agyDir, "beta"), 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(agyDir, "alpha"), 0700))

	// Setup claude accounts: dev
	claudeDir := filepath.Join(tempAccounts, "claude")
	require.NoError(t, os.MkdirAll(filepath.Join(claudeDir, "dev"), 0700))

	t.Run("agy list prints sorted aliases", func(t *testing.T) {
		buf := new(bytes.Buffer)
		agyListCmd.SetOut(buf)
		defer agyListCmd.SetOut(nil)

		err := agyListCmd.RunE(agyListCmd, []string{})
		require.NoError(t, err)
		assert.Equal(t, "alpha\nbeta\n", buf.String())
	})

	t.Run("claude list prints aliases", func(t *testing.T) {
		buf := new(bytes.Buffer)
		claudeListCmd.SetOut(buf)
		defer claudeListCmd.SetOut(nil)

		err := claudeListCmd.RunE(claudeListCmd, []string{})
		require.NoError(t, err)
		assert.Equal(t, "dev\n", buf.String())
	})

	t.Run("agy remove deletes account", func(t *testing.T) {
		err := agyRemoveCmd.RunE(agyRemoveCmd, []string{"alpha"})
		require.NoError(t, err)

		buf := new(bytes.Buffer)
		agyListCmd.SetOut(buf)
		defer agyListCmd.SetOut(nil)
		err = agyListCmd.RunE(agyListCmd, []string{})
		require.NoError(t, err)
		assert.Equal(t, "beta\n", buf.String())
	})

	t.Run("claude remove deletes account", func(t *testing.T) {
		err := claudeRemoveCmd.RunE(claudeRemoveCmd, []string{"dev"})
		require.NoError(t, err)

		buf := new(bytes.Buffer)
		claudeListCmd.SetOut(buf)
		defer claudeListCmd.SetOut(nil)
		err = claudeListCmd.RunE(claudeListCmd, []string{})
		require.NoError(t, err)
		assert.Equal(t, "", buf.String())
	})

	t.Run("remove non-existent account returns error", func(t *testing.T) {
		err := agyRemoveCmd.RunE(agyRemoveCmd, []string{"alpha"})
		require.Error(t, err)
	})
}

func TestClaudeLoginProfileBinding(t *testing.T) {
	t.Setenv(accounts.EnvAccountsDir, t.TempDir())

	origExec := execCLI
	t.Cleanup(func() {
		execCLI = origExec
		claudeLoginForce = false
		claudeLoginProfile = ""
	})
	execCLI = func(ctx context.Context, name string, env []string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
		for _, e := range env {
			if dir, ok := strings.CutPrefix(e, "CLAUDE_CONFIG_DIR="); ok {
				return os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{}`), 0600)
			}
		}
		return nil
	}
	login := func(force bool, profile string) error {
		claudeLoginForce, claudeLoginProfile = force, profile
		claudeLoginCmd.SetOut(io.Discard)
		defer claudeLoginCmd.SetOut(nil)
		return claudeLoginCmd.RunE(claudeLoginCmd, []string{"work"})
	}

	require.NoError(t, login(false, "pro20"))
	got, err := accounts.ReadProfile("claude", "work")
	require.NoError(t, err)
	assert.Equal(t, "pro20", got)

	// Re-login without --profile replaces the dir but keeps the binding.
	require.NoError(t, login(true, ""))
	got, err = accounts.ReadProfile("claude", "work")
	require.NoError(t, err)
	assert.Equal(t, "pro20", got)

	// An explicit --profile rebinds.
	require.NoError(t, login(true, "max200"))
	got, err = accounts.ReadProfile("claude", "work")
	require.NoError(t, err)
	assert.Equal(t, "max200", got)

	// An invalid profile is rejected before the interactive login runs.
	ran := false
	execCLI = func(context.Context, string, []string, io.Reader, io.Writer, io.Writer, ...string) error {
		ran = true
		return nil
	}
	require.Error(t, login(true, "Bad Name"))
	assert.False(t, ran)
}

func TestProfileCommand(t *testing.T) {
	t.Setenv(accounts.EnvAccountsDir, t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join(accounts.Root(), "agy", "a1"), 0700))
	t.Cleanup(func() { agyProfileClear = false })

	run := func(args ...string) (string, error) {
		buf := new(bytes.Buffer)
		agyProfileCmd.SetOut(buf)
		defer agyProfileCmd.SetOut(nil)
		err := agyProfileCmd.RunE(agyProfileCmd, args)
		return buf.String(), err
	}

	out, err := run("a1")
	require.NoError(t, err)
	assert.Equal(t, "\n", out, "unbound account prints an empty line")

	_, err = run("a1", "pro20")
	require.NoError(t, err)
	out, err = run("a1")
	require.NoError(t, err)
	assert.Equal(t, "pro20\n", out)

	_, err = run("missing", "pro20")
	require.Error(t, err)

	agyProfileClear = true
	_, err = run("a1")
	require.NoError(t, err)
	agyProfileClear = false
	out, err = run("a1")
	require.NoError(t, err)
	assert.Equal(t, "\n", out)
}
