package bwrap

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/AgentDrasil/asgard/fakebash"
)

// Sandbox environment policy.
//
// Both the agent sandbox and the command-exec sandbox start from an empty
// environment (bwrap --clearenv); nothing of the runner's own environment
// (ETCD_*, ASGARD_RUNNER_*, API keys, KMS settings, ...) reaches them. What a
// sandbox does see is exactly:
//
//   - HOME, PATH and TZ (appendBaseSandboxArgs),
//   - the allowlisted host variables of inheritedEnvKeys (locale, terminal,
//     user name, XDG base directories, fakebash tunables, Asgard's config path
//     overrides, the operator's upstream proxy / CA settings, and the
//     operator's SetEnvPassthrough list),
//   - the variables the runner code sets on purpose: per-run ASGARD_* identity
//     and opts.Env (ProxySandboxConfig.ExtraEnv), per-CLI ExtraEnv, proxy
//     variables and the dummy secrets of the proxy (appendProxyArgs),
//     SSH_AUTH_SOCK and GIT_SSH_COMMAND, and the TYPESAFE/GEMINI compass keys
//     (to be moved out of the sandbox).
//
// Precedence: bwrap applies the last --setenv of a key. ExtraEnv entries are
// emitted right after the inherited variables and before every builder-set
// value, and entries for protected keys (HOME, PATH, ASGARD_SANDBOXED, the
// proxy/CA variables) are dropped, so a caller cannot override what the
// builders enforce.

// inheritedEnvKeys are the host variables copied into a sandbox as they are.
// None of them carries a credential.
var inheritedEnvKeys = map[string]bool{
	// Identity and terminal handling that every tool expects.
	"USER":     true,
	"LOGNAME":  true,
	"TERM":     true,
	"LANG":     true,
	"LANGUAGE": true,
	// POSIX and glibc locale categories (an explicit list, not a prefix).
	"LC_ALL": true, "LC_CTYPE": true, "LC_COLLATE": true, "LC_MESSAGES": true,
	"LC_MONETARY": true, "LC_NUMERIC": true, "LC_TIME": true,
	"LC_ADDRESS": true, "LC_IDENTIFICATION": true, "LC_MEASUREMENT": true,
	"LC_NAME": true, "LC_PAPER": true, "LC_TELEPHONE": true,
	// XDG base directories: aw and the CLIs resolve their config under them.
	"XDG_CONFIG_HOME": true,
	"XDG_DATA_HOME":   true,
	"XDG_CACHE_HOME":  true,
	"XDG_STATE_HOME":  true,
	// fakebash / fakebashd tunables (plain numbers, flags and a directory).
	"ASGARD_BASH_WATCHDOG_TIMEOUT": true,
	"ASGARD_BASH_COMPACT":          true,
	"ASGARD_BASH_COMPACT_RAW":      true,
	"ASGARD_JEV_TIMEOUT_MS":        true,
	"ASGARD_GEMINI_TIMEOUT_MS":     true,
	"FAKEBASH_OUTPUT_DIR":          true,
	// Asgard's own path overrides for the binaries that run in the sandbox
	// (aw, opencode wrapper, simplest). Paths and a model name, no secrets.
	"AW_CONFIG":                               true,
	"OPENCODE_AW_CONFIG":                      true,
	"SIMPLEST_KEY_PATH":                       true,
	"SIMPLEST_CONFIG_PATH":                    true,
	"SIMPLEST_MODELS_PATH":                    true,
	"GEMINI_MODEL_FOR_COMMAND_RESULT_COMPASS": true,
}

var (
	passthroughMu   sync.RWMutex
	passthroughKeys map[string]bool
)

// SetEnvPassthrough sets the operator-chosen host variables (toolchain
// settings such as GOPROXY, RUSTUP_HOME, JAVA_HOME, PIP_INDEX_URL) that are
// copied into the sandboxes in addition to the built-in allowlist. It must be
// fed from runner-side operator configuration only, never from a request.
// Runner-only and secret variables (fakebash.IsRunnerOnlyEnvKey) and keys the
// builders enforce (isProtectedEnvKey) are refused whatever the operator lists.
func SetEnvPassthrough(keys []string) {
	m := map[string]bool{}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" || strings.ContainsAny(k, "=\x00") || fakebash.IsRunnerOnlyEnvKey(k) || isProtectedEnvKey(k) {
			continue
		}
		m[k] = true
	}
	passthroughMu.Lock()
	passthroughKeys = m
	passthroughMu.Unlock()
}

func passthroughEnvKey(k string) bool {
	passthroughMu.RLock()
	defer passthroughMu.RUnlock()
	return passthroughKeys[k]
}

// upstreamProxyEnvKeys are the operator's upstream proxy and CA variables
// (HTTP_PROXY, NO_PROXY, SSL_CERT_FILE, ...). They are inherited so CLIs behind
// a corporate proxy or custom CA keep working. Proxy URLs may embed
// credentials (user:pass@host); that is operator configuration which was
// always inherited. When the masking proxy is enabled, appendProxyArgs sets
// these keys later and wins.
var upstreamProxyEnvKeys = append(slices.Clone(fakebash.ProtectedProxyEnvKeys), "SSL_CERT_DIR")

// isProtectedEnvKey reports whether builders enforce the value of k, so it may
// not be set through ExtraEnv.
func isProtectedEnvKey(k string) bool {
	return k == "HOME" || k == "PATH" || k == SandboxedEnv || slices.Contains(upstreamProxyEnvKeys, k)
}

// runEnvKeys are the per-run variables the runner sets on a sandbox itself.
// FilterSandboxEnv accepts them in addition to the inherited ones, for callers
// that hand over a ready-made environment (workflow command nodes).
var runEnvKeys = map[string]bool{
	"ASGARD_CHAT_ID":     true,
	"ASGARD_NODE_ID":     true,
	"ASGARD_RUN_TOKEN":   true,
	"ASGARD_HEADLESS":    true,
	"ASGARD_AGENT_ID":    true,
	"ASGARD_AGENT_NAME":  true,
	"ASGARD_MODEL":       true,
	"ASGARD_STATUS_URL":  true,
	"ASGARD_TMP_DIR":     true,
	"ASGARD_SESSION_DIR": true,
	"ASGARD_SANDBOXED":   true,
	"ASGARD_INJECT_SOCK": true,
}

func inheritedEnvKey(k string) bool {
	return inheritedEnvKeys[k] || slices.Contains(upstreamProxyEnvKeys, k) || passthroughEnvKey(k)
}

// appendUpstreamProxyEnvArgs copies only the operator's upstream proxy / CA
// variables into the sandbox.
func appendUpstreamProxyEnvArgs(args []string) []string {
	for _, k := range upstreamProxyEnvKeys {
		if v, ok := os.LookupEnv(k); ok {
			args = append(args, "--setenv", k, v)
		}
	}
	return args
}

// FilterSandboxEnv returns the KEY=VALUE entries of env whose key is on the
// sandbox allowlist (inherited host variables plus the per-run ASGARD_*
// variables). PATH and HOME are not included: the sandbox derives them itself.
func FilterSandboxEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		if inheritedEnvKey(k) || runEnvKeys[k] {
			out = append(out, kv)
		}
	}
	return out
}

// appendInheritedEnvArgs copies the allowlisted variables of the runner's
// environment into the sandbox with --setenv.
func appendInheritedEnvArgs(args []string) []string {
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if ok && k != "" && inheritedEnvKey(k) {
			args = append(args, "--setenv", k, v)
		}
	}
	return args
}

// minimalParentEnv is the environment of the bwrap process itself. bwrap only
// needs to find nothing beyond this; the sandbox gets its environment through
// --clearenv and --setenv.
func minimalParentEnv() []string {
	env := []string{}
	if p := os.Getenv("PATH"); p != "" {
		env = append(env, "PATH="+p)
	}
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		env = append(env, "HOME="+h)
	}
	return env
}

// appendExtraEnvArgs emits --setenv for the trusted KEY=VALUE entries of env
// (run identity, opts.Env). They are explicit choices of the caller and are not
// allowlisted (use FilterSandboxEnv for an environment of unknown content),
// but protected keys are skipped; see the precedence note at the top.
func appendExtraEnvArgs(args []string, env []string) []string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" && !isProtectedEnvKey(k) {
			args = append(args, "--setenv", k, v)
		}
	}
	return args
}

// newBwrapCmd builds the bwrap command for args. The --setenv entries (they
// carry ASGARD_RUN_TOKEN, proxy URLs and dummy secrets) are not put on the
// command line, where any host user could read them from
// /proc/<pid>/cmdline; they are passed through bwrap's `--args FD` as
// NUL-separated arguments read from an anonymous memfd, which is the first
// extra file (fd 3) of the process. Callers that add their own ExtraFiles
// append them, so those start at fd 4. args must start with --clearenv so the
// --args file is read after the environment was cleared.
func newBwrapCmd(ctx context.Context, args []string) (*exec.Cmd, error) {
	var rest []string
	var secret []byte
	end := slices.Index(args, "--")
	if end < 0 {
		end = len(args)
	}
	for i := 0; i < len(args); i++ {
		if i < end && args[i] == "--setenv" && i+2 < end {
			for _, a := range args[i : i+3] {
				secret = append(secret, a...)
				secret = append(secret, 0)
			}
			i += 2
			continue
		}
		rest = append(rest, args[i])
	}
	f, err := newArgsFile(secret)
	if err != nil {
		return nil, err
	}
	argv := make([]string, 0, len(rest)+2)
	if len(rest) > 0 && rest[0] == "--clearenv" {
		argv = append(argv, rest[0])
		rest = rest[1:]
	}
	argv = append(argv, "--args", "3")
	argv = append(argv, rest...)
	cmd := exec.CommandContext(ctx, "bwrap", argv...)
	cmd.Env = minimalParentEnv()
	cmd.ExtraFiles = []*os.File{f}
	return cmd, nil
}

// StartCmd starts a command built by this package and then closes the parent's
// copy of its --args file: bwrap inherits its own descriptor at fork, and
// exec.Cmd never closes ExtraFiles, so without this each command would hold a
// memfd until garbage collection. It also closes the file when Start fails.
// Use it, or CloseArgs, for every command built by this package.
func StartCmd(cmd *exec.Cmd) error {
	err := cmd.Start()
	CloseArgs(cmd)
	return err
}

// CloseArgs closes the parent's copy of the --args file of a command built by
// this package. It is idempotent and safe to defer on builder error paths; the
// command must not be started afterwards (a start after it fails with a closed
// file). Commands not built by this package are left alone.
func CloseArgs(cmd *exec.Cmd) {
	if cmd == nil || len(cmd.ExtraFiles) == 0 || cmd.ExtraFiles[0] == nil {
		return
	}
	if !isBwrapArgsCmd(cmd) {
		return
	}
	_ = cmd.ExtraFiles[0].Close()
}

// isBwrapArgsCmd reports whether cmd has the exact layout newBwrapCmd
// produces: argv[0] "bwrap" followed by "--clearenv", "--args", "3". Matching
// the fixed prefix (not any "--args 3" pair) keeps user-supplied arguments of
// other commands, such as git, from closing their long-lived ExtraFiles[0].
func isBwrapArgsCmd(cmd *exec.Cmd) bool {
	return len(cmd.Args) >= 4 && cmd.Args[0] == "bwrap" &&
		cmd.Args[1] == "--clearenv" && cmd.Args[2] == "--args" && cmd.Args[3] == "3"
}

// newArgsFile returns an anonymous in-memory file holding data, positioned at
// its start. Close it once the command started (see StartCmd); bwrap gets its
// own copy.
func newArgsFile(data []byte) (*os.File, error) {
	fd, err := unix.MemfdCreate("bwrap-args", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("creating bwrap args file: %w", err)
	}
	f := os.NewFile(uintptr(fd), "bwrap-args")
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("writing bwrap args file: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("rewinding bwrap args file: %w", err)
	}
	return f, nil
}

// bwrapArgsOf returns the full bwrap argument list of cmd with the --args file
// expanded in place; for tests and diagnostics (it contains secrets).
func bwrapArgsOf(cmd *exec.Cmd) []string {
	i := slices.Index(cmd.Args, "--args")
	if i < 0 || len(cmd.ExtraFiles) == 0 {
		return slices.Clone(cmd.Args)
	}
	data, err := io.ReadAll(io.NewSectionReader(cmd.ExtraFiles[0], 0, 1<<30))
	if err != nil {
		return slices.Clone(cmd.Args)
	}
	out := slices.Clone(cmd.Args[:i])
	if len(data) > 0 {
		out = append(out, strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")...)
	}
	return append(out, cmd.Args[i+2:]...)
}

// ExpandedArgs returns the complete bwrap argument list of a command built by
// this package, with the --args file expanded. It contains secret values; use
// it for tests and diagnostics only, never for logging.
func ExpandedArgs(cmd *exec.Cmd) []string { return bwrapArgsOf(cmd) }
