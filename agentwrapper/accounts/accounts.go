package accounts

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// DefaultAccountsDir is the default root directory for accounts on the runner.
	DefaultAccountsDir = "/accounts"
	// EnvAccountsDir is the environment variable that can override the root directory.
	EnvAccountsDir = "ASGARD_ACCOUNTS_DIR"
	// TokenFileName is the filename of the antigravity oauth token for agy accounts.
	TokenFileName = "antigravity-oauth-token"
	// DirMode is the file mode used for account directories.
	DirMode = 0700
	// FileMode is the file mode used for account credential files.
	FileMode = 0600
)

var aliasRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Root returns the root directory for accounts, honoring ASGARD_ACCOUNTS_DIR if set.
func Root() string {
	if dir := os.Getenv(EnvAccountsDir); dir != "" {
		return dir
	}
	return DefaultAccountsDir
}

// ValidateCLI checks if the provided CLI name is supported ("agy" or "claude").
func ValidateCLI(cli string) error {
	if cli != "agy" && cli != "claude" {
		return fmt.Errorf("unsupported cli %q: must be agy or claude", cli)
	}
	return nil
}

// ValidateAlias checks if the alias matches ^[a-z0-9][a-z0-9_-]{0,31}$.
func ValidateAlias(alias string) error {
	if !aliasRegex.MatchString(alias) {
		return fmt.Errorf("invalid alias %q: must match ^[a-z0-9][a-z0-9_-]{0,31}$", alias)
	}
	return nil
}

// Dir returns the account directory for the given CLI and alias.
// Rejects unsupported CLIs, invalid aliases, and path traversal attempts.
func Dir(cli, alias string) (string, error) {
	if err := ValidateCLI(cli); err != nil {
		return "", err
	}
	if err := ValidateAlias(alias); err != nil {
		return "", err
	}

	root := Root()
	dir := filepath.Join(root, cli, alias)

	rel, err := filepath.Rel(root, dir)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return "", fmt.Errorf("path traversal rejected: %s", dir)
	}
	return dir, nil
}

// TokenPath returns the path to the antigravity-oauth-token file for an agy account alias.
func TokenPath(alias string) (string, error) {
	dir, err := Dir("agy", alias)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, TokenFileName), nil
}

// List returns a sorted list of account aliases for the given CLI.
// If the directory does not exist, an empty list is returned without error.
func List(cli string) ([]string, error) {
	if err := ValidateCLI(cli); err != nil {
		return nil, err
	}

	cliDir := filepath.Join(Root(), cli)
	entries, err := os.ReadDir(cliDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("reading accounts dir: %w", err)
	}

	var aliases []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if err := ValidateAlias(name); err == nil {
			aliases = append(aliases, name)
		}
	}
	sort.Strings(aliases)
	if aliases == nil {
		aliases = []string{}
	}
	return aliases, nil
}

// Remove removes the account directory for the given CLI and alias.
// Returns an error wrapping os.ErrNotExist if the account directory does not exist.
func Remove(cli, alias string) error {
	dir, err := Dir(cli, alias)
	if err != nil {
		return err
	}

	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("account %q does not exist: %w", alias, os.ErrNotExist)
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("account %q path is not a directory", alias)
	}

	return os.RemoveAll(dir)
}

// Select chooses the account alias for a CLI. It returns the alias named by
// ASGARD_<CLI>_ACCOUNT if set (which must be a valid, existing account),
// otherwise the first alias of List(cli). ok is false when no account exists.
func Select(cli string) (alias string, ok bool, err error) {
	if err := ValidateCLI(cli); err != nil {
		return "", false, err
	}
	envName := "ASGARD_" + strings.ToUpper(cli) + "_ACCOUNT"
	if v := os.Getenv(envName); v != "" {
		dir, err := Dir(cli, v)
		if err != nil {
			return "", false, fmt.Errorf("%s: %w", envName, err)
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return "", false, fmt.Errorf("%s: account %q does not exist", envName, v)
		}
		return v, true, nil
	}
	aliases, err := List(cli)
	if err != nil {
		return "", false, err
	}
	if len(aliases) == 0 {
		return "", false, nil
	}
	return aliases[0], true, nil
}
