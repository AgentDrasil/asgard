package bwrap

import (
	"github.com/AgentDrasil/asgard/pkg/agentspec"
)

// SandboxSpec holds every input of a sandbox builder. The exported
// CommandFor* constructors fill one in from their arguments, so the builders
// (agent.go, cmdexec.go) take a single value instead of a long positional
// parameter list. Which fields a builder reads is documented per field.
type SandboxSpec struct {
	// Cfg is the agent configuration; nil for a sandbox without an agent
	// (plain command execution, the query sandbox).
	Cfg *agentspec.AgentConfig

	// RunDir is the host directory the sandbox works in. Agent sandboxes
	// require it to exist; a command sandbox falls back to HOME when it is
	// empty or missing.
	RunDir string
	// Cwd is the command sandbox working directory (empty: RunDir). Only the
	// command-exec builder reads it.
	Cwd string
	// SockDir is the per-run fakebash socket directory mounted at /fakebash
	// (empty: nothing is mounted).
	SockDir string
	// ChatID selects the per-chat /tmp and /session directories.
	ChatID string
	// ConfigPath is the Asgard server config file that is masked with
	// /dev/null.
	ConfigPath string

	// AllowCrossSession binds the cross-session data root writable into a
	// command sandbox (never for a readonly agent). Agent sandboxes ignore it.
	AllowCrossSession bool
	// ReadonlyDirs are operator readonly mount roots in addition to
	// Cfg.MountDirs.ReadOnly (command sandboxes only; globs are expanded).
	ReadonlyDirs []string

	// Proxy carries the proxy / extra-env settings. The zero value means none.
	Proxy ProxySandboxConfig

	// Agent sandbox only (also the query sandbox, with AllCLIs):

	// AgentPath is the agent's config directory (AGENTS.md, skills/).
	AgentPath string
	// Target is the selected CLI and model.
	Target agentspec.CLITarget
	// Session is the CLI session to resume ("" for none).
	Session string
	// LangRules are the global language rules prepended to the contract.
	LangRules string

	// AllCLIs makes this the metadata-query sandbox, which mounts the auth of
	// every registered CLI and the aw config instead of one target's.
	AllCLIs bool
	// Pins pins accounts (cli -> alias) for the query sandbox; see
	// accountForSandbox.
	Pins map[string]string
}

// firstProxy turns the optional variadic proxy argument of the exported
// constructors into the Proxy field value: only the first entry is read, and
// none means the zero value.
func firstProxy(opts []ProxySandboxConfig) ProxySandboxConfig {
	if len(opts) > 0 {
		return opts[0]
	}
	return ProxySandboxConfig{}
}
