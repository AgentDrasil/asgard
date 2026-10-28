# `agentwrapper` - Agent Wrapper & CLI Abstraction Package

`agentwrapper` provides a unified Go interface (`CLIClient` and `SandboxSpec`) to integrate, manage, and execute external CLI AI coding agents (such as `agy`, `claude`, and `opencode`) within Asgard.

---

## 1. Overview & Architecture

### Is all logic contained in `agentwrapper`?

**No, not exclusively, but `agentwrapper` is the core provider of CLI agent abstractions.**

Here is the breakdown of responsibilities across Asgard:

1. **Core Package (`backend/agentwrapper/`)**:
   - Defines standard interfaces (`CLIClient`, `SandboxSpec`, `ReportFunc`) in `types/types.go`.
   - Implements agent sub-packages (`backend/agentwrapper/agy`, `backend/agentwrapper/opencode`, etc.).
   - Registers available CLI agents (`defaultClients` in `supported.go`).
   - Provides global functions for querying models (`GetSupportedCLIsAndModels`), checking quota (`GetQuota`, `CheckQuota`), and retrieving sandbox specifications (`GetSandboxSpec`).
   - Validates user setup/authentication files (`validate.go`).

2. **Outside `agentwrapper` (Where else logic resides)**:
    - **`lib/bwrap/`**: Renders the `AW_AGENTS.md` contract (agent identity metadata + prompt body) and bind-mounts it at the canonical `/session/AW_AGENTS.md` path; consumes `types.SandboxSpec` via `agentwrapper.GetSandboxSpec(cliName)` for skills mounts, directory binds, authentication directories, and extra args.
    - **`cmd/asgard/main.go`**: Invokes validation functions (e.g., `agentwrapper.ValidateAgySetup()`, `agentwrapper.ValidateClaudeSetup()`, `agentwrapper.ValidateOpencodeSetup()`) at server startup.
    - **`backend/agentwrapper/cmd/aw/`**: Asgard's CLI tool (`aw`). Each sub-command (e.g., `backend/agentwrapper/cmd/aw/commands/agy.go`, `claude.go`, `opencode.go`) exposes a CLI command that invokes the agent's `Prompt` and `Usage` methods. Inside the sandbox, `aw` loads the `AW_AGENTS.md` contract (`agentwrapper/common`) and translates it into CLI-native agent definitions (opencode `agents/<id>.md`, simplest custom prompt, agy `GEMINI.md`, claude `--append-system-prompt-file`).
    - **`lib/agents/run/`**: Handles runtime agent execution within Bubblewrap sandboxes and performs quota checks using `agentwrapper.CheckQuota(cli, model)`.
    - **`lib/api/`**: Serves HTTP endpoints (e.g., `/api/quota`) by delegating quota queries to `agentwrapper.GetQuota(ctx)`.

---

## 2. Interface Definitions (`types/types.go`)

To support a new CLI agent, the agent implementation must satisfy the `CLIClient` interface, and optionally the `SandboxSpec` interface if running under Bubblewrap isolation.

### `CLIClient` Interface
```go
type CLIClient interface {
    Usage(ctx context.Context, opts UsageOptions) ([]ModelUsage, error)
    Models(ctx context.Context, opts UsageOptions) ([]string, error)
    Prompt(ctx context.Context, prompt string, opts PromptOptions) (*PromptResult, error)
}
```

### `SandboxSpec` Interface (Optional but recommended for sandbox execution)
```go
type SandboxSpec interface {
    SystemPromptHeader() string
    SystemPromptPeerHeader() string
    SkillsMountPath(home string) string
    MountDirectories(home string) []string
    AuthDirectory(home string) string
    AuthFiles(home string) []string
    ExtraEnv(home string) map[string]string
    ExtraArgs() []string
}
```

Agent identity is NOT delivered through `SandboxSpec`. Instead, the host
renders the `AW_AGENTS.md` contract (agent id/name/team/tool-access metadata
in YAML frontmatter plus the prompt body) and bind-mounts it at the canonical
`/session/AW_AGENTS.md` path (`$AW_AGENTS_PATH`). The `aw` subcommand running
inside the sandbox loads the contract via `agentwrapper/common` and
translates it into CLI-native form:

- **opencode**: writes `~/.config/opencode/agents/<agent-id>.md` (frontmatter
  with `mode: primary` and `permission` rules derived from the contract, e.g.
  doc-only agents lose `bash` while editing stays available) and runs
  `opencode run --agent <agent-id>`, so the contract identity replaces
  opencode's built-in `build` persona instead of arriving as weak ambient
  instructions.
- **simplest**: composes the in-process system prompt from the CLI protocol
  headers plus the contract body, and selects/crops the tool set from the
  contract's `tool_access`.
- **claude**: passes the composed prompt via `--append-system-prompt-file` (a temp file) and
  derives `--disallowedTools` from the contract (`AskUserQuestion` always,
  `Task`/`Agent` for team agents, `Bash` for doc-only). Runs
  `claude --print --verbose --output-format stream-json
  --dangerously-skip-permissions` (the sandbox must not run as root); a new
  session gets a generated `--session-id`, a given one uses `--resume`, and a
  `/low|medium|high|xhigh|max` model suffix maps to `--effort`. Quota comes
  from parsing the text output of `/usage`.

  The sandbox sets `CLAUDE_CONFIG_DIR=~/.claude` (via `SandboxSpec.ExtraEnv`,
  also baked into the Docker image) so claude's `.claude.json` lives inside
  the bound `~/.claude` directory: claude rewrites that file with a rename,
  which would detach a single-file bind mount. Log in with the same variable
  set (`docker exec -it -e CLAUDE_CONFIG_DIR=<home>/.claude <container> claude`).
  `DISABLE_AUTOUPDATER=1` is set too; updates come from rebuilding the image.

  Known limitations of the claude adapter:
  - Subscription accounts only: quota comes from `/usage`, so API-key, Bedrock
    and Vertex accounts fail the quota check. Only the `fable`, `opus`,
    `sonnet` and `haiku` aliases are supported.
  - Subagent messages (lines with `parent_tool_use_id`) are reported as if they
    came from the main agent, and they also overwrite the reported
    `input_tokens`, so the figure can flap while subagents run (and end up as a
    subagent's context if one finishes after the parent's last message).
  - `doc-only` only disallows `Bash`; other command-capable tools (e.g.
    `Monitor`) and source editing stay available. doc-only is being deprecated.
  - `--agent` is accepted (the sandbox passes it to every CLI) but unused:
    the identity travels entirely in the appended contract prompt.
- **agy**: installs the composed prompt into `~/.gemini/GEMINI.md` for the
  duration of the run and restores the previous content afterwards.

To support a new CLI, the adapter only needs a translation step inside `aw`;
no sandbox mount code changes are required. Pass the agent identity on the
`aw <cli>` command line with `--agent <agent-id>` (mapped to
`types.PromptOptions.AgentID`), which complements the contract metadata.

---

## 3. Step-by-Step Guide: How to Add a New CLI Agent

Follow these steps to add support for a new CLI agent (e.g., `myagent`).

### Step 1: Create a Sub-package under `backend/agentwrapper/`
Create a new directory `backend/agentwrapper/myagent/` and implement the `CLIClient` (and optionally `SandboxSpec`) interface.

Example (`backend/agentwrapper/myagent/client.go`):
```go
package myagent

import (
    "context"
    "github.com/AgentDrasil/asgard/agentwrapper/types"
)

type Client struct{}

func NewClient() *Client {
    return &Client{}
}

func (c *Client) Usage(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
    // Implement quota / usage checking logic
    return []types.ModelUsage{}, nil
}

func (c *Client) Models(ctx context.Context, opts types.UsageOptions) ([]string, error) {
    // Return list of supported model names
    return []string{"myagent-v1"}, nil
}

func (c *Client) Prompt(ctx context.Context, prompt string, opts types.PromptOptions) (*types.PromptResult, error) {
    // Implement prompt execution logic (e.g., executing the CLI binary)
    return &types.PromptResult{}, nil
}

// SandboxSpec implementation
func (c *Client) SystemPromptHeader() string {
    return "## Important Instructions\n..."
}

func (c *Client) SystemPromptPeerHeader() string {
    return "## Peer Collaboration Instructions\n..."
}

func (c *Client) SkillsMountPath(home string) string {
    return home + "/.myagent/skills"
}

func (c *Client) MountDirectories(home string) []string {
    return []string{home + "/.myagent"}
}

func (c *Client) AuthDirectory(home string) string {
    return home + "/.myagent"
}

func (c *Client) ExtraArgs() []string {
    return nil
}
```

### Step 2: Register the New Client in `backend/agentwrapper/supported.go`
Register your new client instance in the `defaultClients` map:

```go
import (
    "github.com/AgentDrasil/asgard/agentwrapper/myagent"
)

var defaultClients = map[string]types.CLIClient{
    "agy":      &agy.Client{},
    "claude":   &claude.Client{},
    "opencode": &opencode.Client{},
    "myagent":  &myagent.Client{}, // <-- Register here
}
```

### Step 3: Add Setup Validation (Optional)
If your agent requires specific local credentials/configs, add a validation function in `backend/agentwrapper/validate.go`:

```go
func ValidateMyAgentSetup() error {
    home, err := homeDirFn()
    if err != nil {
        return fmt.Errorf("failed to get user home directory: %w", err)
    }
    authPath := filepath.Join(home, ".myagent", "credentials.json")
    if _, err := os.Stat(authPath); err != nil {
        return fmt.Errorf("myagent setup validation failed: missing auth file %s", authPath)
    }
    return nil
}
```

Then call this validation in `cmd/asgard/main.go` during startup:
```go
if err := agentwrapper.ValidateMyAgentSetup(); err != nil {
    log.Fatal().Err(err).Msg("MyAgent setup validation failed")
}
```

### Step 4: Add CLI Command to `aw` Tool
Create `backend/agentwrapper/cmd/aw/commands/myagent.go` to expose the Cobra command for `aw myagent`:

```go
package commands

import (
    "github.com/spf13/cobra"
    "github.com/AgentDrasil/asgard/agentwrapper/myagent"
)

var myagentCmd = &cobra.Command{
    Use:   "myagent",
    Short: "Run a myagent session",
    RunE: func(cmd *cobra.Command, args []string) error {
        // Implement CLI flags parsing and call myagent.Prompt / myagent.Usage
        return nil
    },
}

func init() {
    // Register flags...
}
```

Register `myagentCmd` in `backend/agentwrapper/cmd/aw/commands/root.go`:
```go
rootCmd.AddCommand(myagentCmd)
```

---

## 4. Testing

Run all unit tests across `agentwrapper` to ensure compatibility:

```bash
go test ./backend/agentwrapper/...
```
