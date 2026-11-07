package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AgentDrasil/asgard/agentwrapper/accounts"
)

// CLIExecutor is a function type for running interactive CLI commands.
type CLIExecutor func(ctx context.Context, name string, env []string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error

var defaultCLIExecutor CLIExecutor = func(ctx context.Context, name string, env []string, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// execCLI is the executor used by aw account commands. Can be overridden in tests.
var execCLI = defaultCLIExecutor

var (
	agyLoginForce      bool
	claudeLoginForce   bool
	agyLoginProfile    string
	claudeLoginProfile string
	agyProfileClear    bool
	claudeProfileClear bool
)

const skipConfigAnnotation = "skipConfig"

func markSkipConfig(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string)
	}
	cmd.Annotations[skipConfigAnnotation] = "true"
	return cmd
}

func isAccountCmd(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	return cmd.Annotations[skipConfigAnnotation] == "true"
}

func envWithKey(key, value string) []string {
	prefix := key + "="
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, prefix) {
			env = append(env, e)
		}
	}
	return append(env, prefix+value)
}

// bindLoginProfile records the quota profile of a freshly logged-in account:
// the one requested with --profile, else the one the account had before the
// login replaced it, so re-logging in does not silently drop the binding.
func bindLoginProfile(cli, alias, requested, previous string) error {
	name := requested
	if name == "" {
		name = previous
	}
	if name == "" {
		return nil
	}
	return accounts.WriteProfile(cli, alias, name)
}

// newProfileCmd builds `aw <cli> profile <alias> [name]`, which shows, sets
// or (with --clear) removes the quota profile an account is bound to.
func newProfileCmd(cli string, clear *bool) *cobra.Command {
	cmd := markSkipConfig(&cobra.Command{
		Use:   "profile <alias> [name]",
		Short: "Show or set the quota profile of a " + cli + " account",
		Long: "Without a name, prints the quota profile the account is bound to (empty when unbound).\n" +
			"With a name, binds the account to that profile of the aw config's quota_profiles.\n" +
			"With --clear, removes the binding so the account uses the default profile.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			alias := args[0]
			if *clear {
				if len(args) != 1 {
					return errors.New("--clear takes no profile name")
				}
				dir, err := accounts.Dir(cli, alias)
				if err != nil {
					return err
				}
				if err := os.Remove(filepath.Join(dir, accounts.ProfileFileName)); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("removing profile binding: %w", err)
				}
				return nil
			}
			if len(args) == 2 {
				return accounts.WriteProfile(cli, alias, args[1])
			}
			name, err := accounts.ReadProfile(cli, alias)
			if err != nil {
				return err
			}
			cmd.Println(name)
			return nil
		},
	})
	cmd.Flags().BoolVar(clear, "clear", false, "Remove the account's profile binding")
	return cmd
}

var (
	agyProfileCmd    = newProfileCmd("agy", &agyProfileClear)
	claudeProfileCmd = newProfileCmd("claude", &claudeProfileClear)
)

// agyLoginCmd handles `aw agy login <alias>`
var agyLoginCmd = markSkipConfig(&cobra.Command{
	Use:   "login <alias>",
	Short: "Log in and store account credentials for agy",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		alias := args[0]
		if err := accounts.ValidateAlias(alias); err != nil {
			return err
		}

		if agyLoginProfile != "" {
			if err := accounts.ValidateProfile(agyLoginProfile); err != nil {
				return err
			}
		}

		accountDir, err := accounts.Dir("agy", alias)
		if err != nil {
			return err
		}
		tokenPath, err := accounts.TokenPath(alias)
		if err != nil {
			return err
		}

		if _, err := os.Stat(accountDir); err == nil {
			if !agyLoginForce {
				return fmt.Errorf("account %q already exists (use --force to overwrite)", alias)
			}
		}

		tempHome, err := os.MkdirTemp("", "aw-agy-home-*")
		if err != nil {
			return fmt.Errorf("creating temp home: %w", err)
		}
		if err := os.Chmod(tempHome, 0700); err != nil {
			_ = os.RemoveAll(tempHome)
			return fmt.Errorf("setting temp home permissions: %w", err)
		}
		defer func() {
			_ = os.RemoveAll(tempHome)
		}()

		env := envWithKey("HOME", tempHome)
		runErr := execCLI(cmd.Context(), "agy", env, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())

		if runErr != nil {
			return fmt.Errorf("running agy: %w", runErr)
		}
		tokenSrc := filepath.Join(tempHome, ".gemini", "antigravity-cli", "antigravity-oauth-token")
		if _, statErr := os.Stat(tokenSrc); statErr != nil {
			return errors.New("login did not produce a token")
		}

		if err := os.MkdirAll(accountDir, 0700); err != nil {
			return fmt.Errorf("creating account dir: %w", err)
		}
		if err := os.Chmod(accountDir, 0700); err != nil {
			return fmt.Errorf("setting account dir permissions: %w", err)
		}

		// Atomically copy token: write to temp file in destination dir, chmod 0600, rename
		tmpFile, err := os.CreateTemp(accountDir, ".antigravity-oauth-token-*")
		if err != nil {
			return fmt.Errorf("creating temp token file: %w", err)
		}
		tmpPath := tmpFile.Name()
		defer func() { _ = os.Remove(tmpPath) }()

		if err := tmpFile.Chmod(0600); err != nil {
			_ = tmpFile.Close()
			return fmt.Errorf("setting temp token file permissions: %w", err)
		}

		srcFile, err := os.Open(tokenSrc)
		if err != nil {
			_ = tmpFile.Close()
			return fmt.Errorf("opening token src: %w", err)
		}
		defer func() {
			_ = srcFile.Close()
		}()

		if _, err := io.Copy(tmpFile, srcFile); err != nil {
			_ = tmpFile.Close()
			return fmt.Errorf("copying token: %w", err)
		}
		if err := tmpFile.Sync(); err != nil {
			_ = tmpFile.Close()
			return fmt.Errorf("syncing token file: %w", err)
		}
		if err := tmpFile.Close(); err != nil {
			return fmt.Errorf("closing temp token file: %w", err)
		}

		if err := os.Rename(tmpPath, tokenPath); err != nil {
			return fmt.Errorf("moving token file to destination: %w", err)
		}
		if err := os.Chmod(tokenPath, 0600); err != nil {
			return fmt.Errorf("setting token file permissions: %w", err)
		}

		// The account dir survives a re-login, so an existing binding stays.
		if err := bindLoginProfile("agy", alias, agyLoginProfile, ""); err != nil {
			return err
		}

		cmd.Println(alias)
		return nil
	},
})

// agyListCmd handles `aw agy list`
var agyListCmd = markSkipConfig(&cobra.Command{
	Use:   "list",
	Short: "List configured agy accounts",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		aliases, err := accounts.List("agy")
		if err != nil {
			return err
		}
		for _, a := range aliases {
			cmd.Println(a)
		}
		return nil
	},
})

// agyRemoveCmd handles `aw agy remove <alias>`
var agyRemoveCmd = markSkipConfig(&cobra.Command{
	Use:   "remove <alias>",
	Short: "Remove a configured agy account",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		alias := args[0]
		return accounts.Remove("agy", alias)
	},
})

// claudeLoginCmd handles `aw claude login <alias>`
var claudeLoginCmd = markSkipConfig(&cobra.Command{
	Use:   "login <alias>",
	Short: "Log in and store account credentials for claude",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		alias := args[0]
		if err := accounts.ValidateAlias(alias); err != nil {
			return err
		}

		if claudeLoginProfile != "" {
			if err := accounts.ValidateProfile(claudeLoginProfile); err != nil {
				return err
			}
		}

		accountDir, err := accounts.Dir("claude", alias)
		if err != nil {
			return err
		}

		if _, err := os.Stat(accountDir); err == nil && !claudeLoginForce {
			return fmt.Errorf("account %q already exists (use --force to overwrite)", alias)
		}

		// Log in against a fresh staging dir so a stale .credentials.json can
		// never mask a failed login; swap it in only after a verified success.
		parent := filepath.Dir(accountDir)
		if err := os.MkdirAll(parent, 0700); err != nil {
			return fmt.Errorf("creating accounts dir: %w", err)
		}
		staging, err := os.MkdirTemp(parent, ".login-*")
		if err != nil {
			return fmt.Errorf("creating staging dir: %w", err)
		}
		defer func() { _ = os.RemoveAll(staging) }()
		if err := os.Chmod(staging, 0700); err != nil {
			return fmt.Errorf("setting staging dir permissions: %w", err)
		}

		env := envWithKey("CLAUDE_CONFIG_DIR", staging)
		if runErr := execCLI(cmd.Context(), "claude", env, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()); runErr != nil {
			return fmt.Errorf("running claude: %w", runErr)
		}
		if _, statErr := os.Stat(filepath.Join(staging, ".credentials.json")); statErr != nil {
			return errors.New("login did not produce .credentials.json")
		}
		// Replacing the dir would drop the binding; carry it over.
		previousProfile, _ := accounts.ReadProfile("claude", alias)
		if err := os.RemoveAll(accountDir); err != nil {
			return fmt.Errorf("replacing account dir: %w", err)
		}
		if err := os.Rename(staging, accountDir); err != nil {
			return fmt.Errorf("moving account dir into place: %w", err)
		}
		if err := bindLoginProfile("claude", alias, claudeLoginProfile, previousProfile); err != nil {
			return err
		}

		cmd.Println(alias)
		return nil
	},
})

// claudeListCmd handles `aw claude list`
var claudeListCmd = markSkipConfig(&cobra.Command{
	Use:   "list",
	Short: "List configured claude accounts",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		aliases, err := accounts.List("claude")
		if err != nil {
			return err
		}
		for _, a := range aliases {
			cmd.Println(a)
		}
		return nil
	},
})

// claudeRemoveCmd handles `aw claude remove <alias>`
var claudeRemoveCmd = markSkipConfig(&cobra.Command{
	Use:   "remove <alias>",
	Short: "Remove a configured claude account",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		alias := args[0]
		return accounts.Remove("claude", alias)
	},
})

func init() {
	agyLoginCmd.Flags().BoolVarP(&agyLoginForce, "force", "f", false, "Overwrite existing account alias")
	claudeLoginCmd.Flags().BoolVarP(&claudeLoginForce, "force", "f", false, "Overwrite existing account alias")
	agyLoginCmd.Flags().StringVar(&agyLoginProfile, "profile", "", "Quota profile to bind the account to")
	claudeLoginCmd.Flags().StringVar(&claudeLoginProfile, "profile", "", "Quota profile to bind the account to")

	agyCmd.AddCommand(agyLoginCmd)
	agyCmd.AddCommand(agyListCmd)
	agyCmd.AddCommand(agyRemoveCmd)
	agyCmd.AddCommand(agyProfileCmd)

	claudeCmd.AddCommand(claudeLoginCmd)
	claudeCmd.AddCommand(claudeListCmd)
	claudeCmd.AddCommand(claudeRemoveCmd)
	claudeCmd.AddCommand(claudeProfileCmd)
}
