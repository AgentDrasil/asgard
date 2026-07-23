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

The sandbox execution is managed by the orchestrator in [run.go](src/AgentDrasil/asgard/lib/agents/run/run.go).

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
    *   `/bin/bash` and `/usr/bin/bash` are bind-mounted to [fakebash](src/AgentDrasil/asgard/cmd/fakebash/main.go) to intercept any shell command executions by the agent.
*   **Command Execution Sandbox**: Runs the [fakebashd](src/AgentDrasil/asgard/cmd/fakebashd/main.go) daemon.
    *   This is where actual shell commands requested by the agent are executed.
    *   It mounts the active `runDir` read-write, allowing commands to read/write workspace files.
    *   **Credential Masking**: To prevent credential theft, sensitive directories such as `~/.gemini` and `~/.local/share/opencode` are masked with empty `tmpfs` mounts, ensuring that commands executed by the agent cannot read authentication keys.

### 2. Communication Protocol (`fakebash` & `fakebashd`)

The host process initializes a temporary host directory and bind-mounts it to `/fakebash` inside both sandboxes.

1.  **Command Interception**: When the agent attempts to run a shell command, it calls `/bin/bash`, invoking the `fakebash` client.
2.  **Allowlist Filtering**: The `fakebash` client checks if the command is in the allowlist (e.g., `agystatusline`). If allowlisted, it runs directly in the Agent Sandbox.
3.  **gRPC Forwarding**: Otherwise, `fakebash` establishes a gRPC connection over the Unix socket file at `/fakebash/fakebash.sock` to the `fakebashd` daemon running in the Command Execution Sandbox.
4.  **Execution in PTY**: `fakebashd` runs a persistent `bash` shell inside a PTY and executes the forwarded command in the specified working directory, forwarding stdout/stderr stream packages and the exit code back to the client.

## API Endpoints

Asgard serves an HTTP API for agent orchestration, team discovery, and system management:

### 1. A2A Agent Interface
Each configured agent is exposed as an individual endpoint based on the **Agent-to-Agent (A2A)** protocol:
*   `/agents/{agent_id}/`: Root path for executing commands and querying agent status.
*   `/agents/{agent_id}/.well-known/agent-card.json`: Returns the agent card metadata.

### 2. Management & Coordination API
*   **Reload Configurations** (`POST /api/manage/reload`): Reloads the local agent definition YAML configurations dynamically without needing to restart the orchestrator server.
*   **Team Discovery** (`GET /team`):
    *   Finds the agent linked to the session queried by chat ID.
    *   Identifies that agent's team configurations.
    *   Returns details on all other agents that belong to the same team.

## GitHub Account Setup

**Recommended**: Create a separate GitHub account for Asgard to avoid exposing your main account's credentials. If your main account is used and its private tokens leak, an attacker could access all your repositories.

**Alternative**: If you prefer not to set up a separate account, ensure all repositories have:
- Main branch protection enabled
- Require pull requests for all changes

This prevents direct pushes and forces code review even if credentials are compromised.

## License

Apache 2.0
