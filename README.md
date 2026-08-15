# Asgard - AI Coding Orchestrator

An AI-powered coding assistant that runs inside Docker and orchestrates CLI-based coding agents (such as antigravity-cli) to handle programming tasks.

## Overview

Asgard is designed to be a self-hosted AI coding solution that:
- Runs entirely in Docker for easy deployment and isolation
- Executes code generation/editing tasks using CLI-based AI agents like `antigravity-cli`
- Provides a simple, accessible way to get AI coding assistance

## Architecture

- **Backend**: Go orchestration layer
- **AI Engine**: CLI-based coding agents (antigravity-cli/...)
- **Runtime**: Docker container

## Sandbox Architecture

To prevent untrusted code generated or executed by the AI agents from compromising the system or stealing sensitive authentication tokens (e.g., credentials stored in `~/.gemini`), Asgard employs a dual-sandbox architecture based on [bubblewrap (bwrap)](https://github.com/containers/bubblewrap).

The sandbox execution is managed by the orchestrator in [run.go](lib/agents/run/run.go).

```mermaid
graph TD
    subgraph Host Process [Host Orchestrator]
        RunGo[run.Go]
        SockDir[Host Socket Directory]
    end

    subgraph AgentSandbox [Agent Sandbox (bwrap)]
        AW[aw/agent wrapper]
        FakeBashClient[fakebash client]
        AW -->|Runs Shell Cmd| FakeBashClient
    end

    subgraph CmdSandbox [Command Execution Sandbox (bwrap)]
        FakeBashDaemon[fakebashd daemon]
        Shell[bash shell in PTY]
        FakeBashDaemon -->|Spawns / Manages| Shell
    end

    RunGo -->|Spawns| AgentSandbox
    RunGo -->|Spawns| CmdSandbox
    SockDir ---|Mounts to /fakebash| AgentSandbox
    SockDir ---|Mounts to /fakebash| CmdSandbox
    FakeBashClient <-->|gRPC over /fakebash/fakebash.sock| FakeBashDaemon
```

### 1. The Dual-Sandbox Concept

When executing an agent, Asgard starts two parallel sandboxes using Bubblewrap:

*   **Agent Sandbox**: Runs the agent wrapper process (`aw`).
    *   This sandbox has access to the agent's authentication credentials (e.g., `~/.gemini` or `~/.config/opencode`) so it can make API calls to LLM providers.
    *   System directories (`/bin`, `/usr/bin`, etc.) are mounted read-only.
    *   `/bin/bash` and `/usr/bin/bash` are bind-mounted to [fakebash](cmd/fakebash/main.go) to intercept any shell command executions by the agent.
*   **Command Execution Sandbox**: Runs the [fakebashd](cmd/fakebashd/main.go) daemon.
    *   This is where actual shell commands requested by the agent are executed.
    *   It mounts the active `runDir` read-write, allowing commands to read/write workspace files.
    *   **Credential Masking**: To prevent credential theft, sensitive directories such as `~/.gemini` and `~/.local/share/opencode` are masked with empty `tmpfs` mounts, ensuring that commands executed by the agent cannot read authentication keys.

### 2. Communication Protocol (`fakebash` & `fakebashd`)

The host process initializes a temporary host directory and bind-mounts it to `/fakebash` inside both sandboxes.

1.  **Command Interception**: When the agent attempts to run a shell command, it calls `/bin/bash`, invoking the `fakebash` client.
2.  **Allowlist Filtering**: The `fakebash` client checks if the command is in the allowlist (e.g., `agystatusline`). If allowlisted, it runs directly in the Agent Sandbox.
3.  **gRPC Forwarding**: Otherwise, `fakebash` establishes a gRPC connection over the Unix socket file at `/fakebash/fakebash.sock` to the `fakebashd` daemon running in the Command Execution Sandbox.
4.  **Execution in PTY**: `fakebashd` runs a persistent `bash` shell inside a PTY and executes the forwarded command in the specified working directory, forwarding stdout/stderr stream packages and the exit code back to the client.

## Workflow Orchestration Engine

Asgard includes a DAG-based workflow engine (`lib/workflow`) that orchestrates multi-step AI development pipelines combining autonomous coding agents, raw LLM calls, shell commands, and human-in-the-loop approvals.

### Key Capabilities
- **Fork-Join Parallel Scheduling**: Concurrently executes independent DAG nodes and aggregates results.
- **Heterogeneous Node Types**:
  - `agent`: Runs CLI-based coding agents (e.g. `agy-coder`) with session policy inheritance (`inherit` or `fresh`).
  - `command`: Executes sandboxed or direct bash shell commands.
  - `llm`: Invokes raw LLM models (e.g. `gemini-2.5-flash`) for fast classification or summarization.
  - `human`: Pauses workflow execution for user review via WebUI / A2A (`TaskStateInputRequired`), persisting state across server restarts.
- **Smart Edge Conditions & Join Rules**:
  - `when`: Dot-notation expressions (e.g. `nodes.build_cmd.exit_code != 0`) to trigger conditional repair or fallback branches.
  - `join: always`: Runs summary or clean-up nodes regardless of upstream skips or failures.
- **Sandbox-Friendly Workspaces**: Isolates intermediate step files under `tmp_dir` (defaults to `/tmp/${session_id}`).

### Example Workflows (`examples/workflows/`)
Ready-to-use workflow definitions are located in [`examples/workflows/`](file:///home/chao/src/AgentDrasil/asgard/examples/workflows/):
- **[build-and-fix.yaml](file:///home/chao/src/AgentDrasil/asgard/examples/workflows/build-and-fix.yaml)**: Runs code generation, executes build checks, and conditionally triggers a fix agent if the build fails.
- **[human-in-the-loop.yaml](file:///home/chao/src/AgentDrasil/asgard/examples/workflows/human-in-the-loop.yaml)**: Generates a plan, pauses for human approval, uses a lightweight LLM classifier to parse natural language feedback, and conditionally proceeds with code execution.
- **[parallel-review.yaml](file:///home/chao/src/AgentDrasil/asgard/examples/workflows/parallel-review.yaml)**: Concurrently runs security and performance review agents and consolidates their reports.

### Validating Workflow & Agent Definitions (`agent-validate`)
Use the `agent-validate` CLI utility to validate workflow YAML syntax, DAG topology (cycle detection), edge expressions, and agent configs:

```bash
# Validate a standalone workflow definition
go run ./cmd/agent-validate ./examples/workflows/build-and-fix.yaml

# Validate an agent directory containing config.yaml & workflow.yaml
go run ./cmd/agent-validate .agents/agents/my-workflow-agent/
```

## API Endpoints

Asgard serves an HTTP API for agent orchestration, team discovery, and system management:

### 1. A2A Agent Interface
Each configured agent is exposed as an individual endpoint based on the **Agent-to-Agent (A2A)** protocol:
*   `/agents/{agent_id}/`: Root path for executing commands and querying agent status.
*   `/agents/{agent_id}/.well-known/agent-card.json`: Returns the agent card metadata.

### 2. Management & Coordination API
*   **Public Config** (`GET /api/config`): Returns public configuration settings to web clients (including Web Push configuration `firebase_webpush_web`).
*   **Reload Configurations** (`POST /api/manage/reload`): Reloads the local agent definition YAML configurations dynamically without needing to restart the orchestrator server.
*   **Team Discovery** (`GET /team`):
    *   Finds the agent linked to the session queried by chat ID.
    *   Identifies that agent's team configurations.
    *   Returns details on all other agents that belong to the same team.

## SSH Agent Integration

Asgard uses standard `ssh-agent` Socket passthrough to allow agents to perform SSH operations (e.g., `git clone`, `git push`) without exposing private keys (`/home/user/.ssh`) inside the sandboxes.

### How it works:
1. The host (or parent container) runs `ssh-agent` and sets the `SSH_AUTH_SOCK` environment variable.
2. The `SSH_AUTH_SOCK` Unix socket is mounted into the Asgard container.
3. Asgard's `bwrap` sandbox automatically passes through `SSH_AUTH_SOCK` into the agent and command execution sandboxes while keeping `/home/user/.ssh` masked with an empty `tmpfs`.

### Running with Docker Compose:

Ensure your host has `ssh-agent` running and key loaded:

```bash
eval $(ssh-agent)
ssh-add /path/to/your/dedicated_key
```

Then start Asgard via Docker Compose:

```bash
SSH_AUTH_SOCK=$SSH_AUTH_SOCK docker compose up -d
```

## GitHub Account Setup

**Recommended**: Create a separate GitHub account for Asgard to avoid exposing your main account's credentials. If your main account is used and its private tokens leak, an attacker could access all your repositories.

**Alternative**: If you prefer not to set up a separate account, ensure all repositories have:
- Main branch protection enabled
- Require pull requests for all changes

This prevents direct pushes and forces code review even if credentials are compromised.

## License

Apache 2.0
