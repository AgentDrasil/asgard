package bwrap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"

	"github.com/AgentDrasil/asgard/agentwrapper/accounts"
)

// readAccountProfile reads an account's quota profile binding; a variable so
// tests can stub it.
var readAccountProfile = accounts.ReadProfile

// setProfileEnv hands the quota profile bound to the selected account to the
// query sandbox, whose aw evaluates the quota policy with it. The binding
// file lives in the account directory, which a query sandbox does not
// necessarily see (agy only gets its token file), so the host passes the name.
func setProfileEnv(envs map[string]string, cli string, sel accountSel, allCLIs bool) {
	if !allCLIs || !sel.ok {
		return
	}
	profile, err := readAccountProfile(cli, sel.alias)
	if err != nil {
		log.Warn().Err(err).Str("cli", cli).Str("account", sel.alias).Msg("ignoring unreadable quota profile binding")
		return
	}
	if profile != "" {
		envs[accounts.ProfileEnvName(cli)] = profile
	}
}

// selectAccount picks the account alias for a CLI (env override or first
// alias); a variable so tests can stub it.
var selectAccount = accounts.Select

// accountSel is the account chosen for one CLI in a sandbox.
type accountSel struct {
	alias string
	ok    bool
}

// accountForSandbox resolves the account for cli in a sandbox:
//   - pinned query sandbox (allCLIs with pins): exactly the pinned alias, or
//     none for a CLI that is not pinned; no selection logic runs;
//   - unpinned query sandbox: the simple selection (env override or first
//     alias), so a metadata query never triggers quota lookups itself;
//   - agent sandbox: chooseAccount (quota-aware per cli and model).
func accountForSandbox(cli, model string, allCLIs bool, pins map[string]string) (accountSel, error) {
	if allCLIs && len(pins) > 0 {
		alias, ok := pins[cli]
		return accountSel{alias: alias, ok: ok}, nil
	}
	choose := chooseAccount
	if allCLIs {
		choose = func(cli, _ string) (string, bool, error) { return selectAccount(cli) }
	}
	alias, ok, err := choose(cli, model)
	if err != nil {
		return accountSel{}, fmt.Errorf("selecting %s account: %w", cli, err)
	}
	return accountSel{alias: alias, ok: ok}, nil
}

// agyOAuthTokenRelPath is the agy token path relative to the home directory.
const agyOAuthTokenRelPath = ".gemini/antigravity-cli/antigravity-oauth-token"

// appendAgyAccountToken binds the given agy account's token file over the
// sandbox's agy token path. It changes nothing when no account is selected or
// a selected account's missing token file is an error.
func appendAgyAccountToken(args []string, home string, sel accountSel) ([]string, error) {
	if !sel.ok {
		return args, nil
	}
	alias := sel.alias
	tokenPath, err := accounts.TokenPath(alias)
	if err != nil {
		return nil, fmt.Errorf("agy account %q: %w", alias, err)
	}
	fi, statErr := os.Stat(tokenPath)
	if statErr == nil && fi.IsDir() {
		statErr = errors.New("is a directory")
	}
	if statErr != nil {
		// A selected account must never silently run as the host's own
		// credentials.
		return nil, fmt.Errorf("agy account %q token %s: %w", alias, tokenPath, statErr)
	}
	log.Debug().Str("cli", "agy").Str("account", alias).Msg("binding account token")
	return append(args, "--bind", tokenPath, filepath.Join(home, agyOAuthTokenRelPath)), nil
}

// appendClaudeAccountDir binds the given claude account dir read-write over
// the sandbox's ~/.claude. When session is set and another account holds it,
// the session files are first relayed into the account (agent path only: the
// query sandbox passes no session). It changes nothing when no account is
// selected; a selected account without credentials is an error.
func appendClaudeAccountDir(args []string, home string, sel accountSel, session string) ([]string, error) {
	if !sel.ok {
		return args, nil
	}
	alias := sel.alias
	dir, err := accounts.Dir("claude", alias)
	if err != nil {
		return nil, fmt.Errorf("claude account %q: %w", alias, err)
	}
	credPath := filepath.Join(dir, accounts.ClaudeCredentialsFile)
	fi, statErr := os.Stat(credPath)
	if statErr == nil && fi.IsDir() {
		statErr = errors.New("is a directory")
	}
	if statErr != nil {
		return nil, fmt.Errorf("claude account %q credentials %s: %w", alias, credPath, statErr)
	}
	if session != "" {
		if err := relayClaudeSession(alias, session); err != nil {
			return nil, err
		}
	}
	log.Debug().Str("cli", "claude").Str("account", alias).Msg("binding account dir")
	return append(args, "--bind", dir, filepath.Join(home, ".claude")), nil
}

// relayClaudeSession makes sure the chosen account holds the newest copy of
// session, copying it from the account that does when it does not. An invalid
// session id is not an error: the relay is skipped and claude handles the id.
func relayClaudeSession(chosen, session string) error {
	if err := accounts.ValidateSessionID(session); err != nil {
		log.Debug().Err(err).Str("session", session).Msg("skipping claude session relay")
		return nil
	}
	holder, found, err := accounts.FindClaudeSession(session)
	if err != nil {
		return fmt.Errorf("locating claude session %s: %w", session, err)
	}
	if !found || holder == chosen {
		return nil
	}
	if err := accounts.CopyClaudeSession(holder, chosen, session); err != nil {
		return fmt.Errorf("relaying claude session %s from account %q to %q: %w", session, holder, chosen, err)
	}
	log.Info().Str("session", session).Str("from", holder).Str("to", chosen).Msg("relayed claude session between accounts")
	return nil
}

// resolveBindDests resolves symlinks in each bind destination (bwrap cannot
// mount onto a symlink) and returns the unique results in input order. A path
// that cannot be resolved (e.g. it does not exist) is kept as is.
func resolveBindDests(paths ...string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			p = resolved
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
