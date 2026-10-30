# Asgard - AI Coding Orchestrator

An AI-powered coding assistant that runs inside Docker and orchestrates CLI-based coding agents (such as antigravity-cli) to handle programming tasks.

## Overview

Asgard is designed to be a self-hosted AI coding solution that:
- Runs entirely in Docker for easy deployment and isolation
- Executes code generation/editing tasks using CLI-based AI agents like `antigravity-cli`
- Provides a simple, accessible way to get AI coding assistance

## Architecture

Asgard is split into a **backend** and one or more **runners**. There is no scheduler: a chat is pinned to one runner and every run of that chat executes there.

- **Backend** (`asgard`, Go): orchestration, the database (SQLite or PostgreSQL, metadata only), agent definitions (read from etcd), the WebUI and the REST/SSE API. It never executes agents, shell commands, git or ttyd itself for a runner-bound chat.
- **Runner** (`asgard-runner`): runs the CLI agents in the bubblewrap sandbox, and owns everything on disk for its chats: the per-chat tmp/session directories, transcripts (`messages.jsonl`), workflow run files, git operations, ttyd terminals, workflow command nodes and the CLI quota queries.
- **AI engine**: CLI-based coding agents (antigravity-cli/claude/opencode/...), installed on the runner.

```
 WebUI / API clients
        |
     Backend  ---- etcd (config, agent definitions)
        ^  mTLS TCP :9311 (runner dials in), yamux session
        |    runner -> backend: NodeControl (run control + events), AgentCallback
        |    backend -> runner: RunnerService (files, git, ttyd, command, cleanup, quota)
     Runner(s) -> bwrap agent sandbox + command sandbox
```

- **Connection**: the runner dials the backend's registry port (`runner.registry_port`, default `9311`) over mutual TLS. The client certificate CN is the runner's node id and must be listed in `runner.allowed_nodes`. One yamux session carries two gRPC directions: runner to backend (`NodeControl` bidi stream for run dispatch/events/acks, `AgentCallback` for sandbox callbacks) and backend to runner (`RunnerService`).
- **No message queue**: run events are cached on the runner and replayed to the backend on reconnect (sequence numbers and acks make delivery idempotent). While a runner is offline, messages for its chats stay queued and resume when it reconnects.
- **Chat pinning**: a chat is bound to one runner when it is created (the user picks it in the WebUI sidebar runner dropdown) and the binding is immutable. `call-peer` can only reach agents on the same runner; it may also target an agent in the caller's own chat, with cycle detection and a maximum call chain depth of 4.
- **Config**: runner and agent configuration live in etcd (`ETCD_ENDPOINTS`, `ETCD_PREFIX`, default prefix `/asgard/configs`); both backend and runner need etcd access.

## Filesystem Layout

All Asgard-owned paths are rooted at `~/asgard` and are **not configurable**
(single source of truth: [`pkg/paths`](pkg/paths/paths.go)):

```
/asgard/                    # Shared root (backend & runner)
├── config/                 # config.yaml, proxy.yaml, keys.yaml, ca/{ca.crt,ca.key}
├── agents/                 # agent definitions
├── teams.yaml              # agent team definitions
├── agents-cache/           # agent definitions materialized from etcd
├── logs/                   # backend service logs
└── data/                   # backend only
    ├── avatars/            # cached OIDC user avatars (served locally)
    └── data.db             # SQLite database (+ -wal/-shm)

/session-data/              # Runner: cross-session scratch & persistent storage
├── sessions/<chatID>/      # persistent per-session data  → sandbox /session
├── tmp/<chatID>/           # per-session scratch data      → sandbox /tmp
└── logs/                   # runner and sandbox tool logs

/certs/                     # Runner TLS: tls.crt, tls.key, ca.crt (fixed, read-only)
/data/                      # Workspaces, mounted by docker compose (run_dirs / mount_dirs)
```

The layout above exists on every machine that runs an Asgard process. On the **backend** only `config/`, `logs/`, the database, `avatars/` and the materialized agent cache (`data/agents-cache`, built from etcd) are used; `/session-data/sessions` and `/session-data/tmp` (the chat directories) live on the **runner** that owns the chat.

The SQLite database path is fixed (`dsn` is only used for `db: pg`). `aw`'s own
`~/.config/aw/config.yaml` and the web UI static asset path (`webui_path`) are
not part of this layout.

## Sandbox Architecture

To prevent untrusted code generated or executed by the AI agents from compromising the system or stealing sensitive authentication tokens (e.g., credentials stored in `~/.gemini`), Asgard employs a dual-sandbox architecture based on [bubblewrap (bwrap)](https://github.com/containers/bubblewrap).

The sandboxes are started by the **runner** (the backend only dispatches runs to it); the bwrap argument construction lives in [bwrap.go](backend/lib/bwrap/bwrap.go). Sandbox callbacks (ask-user, call-peer, find-peer, show-output, status, metrics) go agent sandbox → runner (`/fakebash/asgard.sock`) → backend over the runner connection.

```mermaid
graph TD
    subgraph Host Process [Runner]
        RunGo[run.Go]
        SockDir[Host Socket Directory]
    end

    Backend[Backend]
    RunGo <-->|mTLS + yamux gRPC| Backend

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
    AW <-->|asgard.sock callbacks| RunGo
```

### 1. The Dual-Sandbox Concept

When executing an agent, Asgard starts two parallel sandboxes using Bubblewrap:

Both sandboxes bind-mount per-chat directories of the runner host: `/session-data/tmp/<chat-id>` at `/tmp` and `/session-data/sessions/<chat-id>` at `/session` (persistent per-chat scratch space, cleaned up together with the session).
The `/session` mount stores the per-session message transcript stream (`messages.jsonl`) and workflow execution run outputs and node logs (`workflows/<runID>/`). Each sandbox instance is strictly isolated to its own single session directory (`/session-data/sessions/<chat-id>`), preventing cross-session data leakage while allowing the agent compliant visibility into its own conversational transcript and intermediate workflow artifacts.

*   **Agent Sandbox**: Runs the agent wrapper process (`aw`).
    *   This sandbox has access to the agent's authentication credentials (e.g., `~/.gemini`, `~/.claude` or `~/.config/opencode`) so it can make API calls to LLM providers.
    *   System directories (`/bin`, `/usr/bin`, etc.) are mounted read-only.
    *   `/bin/bash` and `/usr/bin/bash` are bind-mounted to [fakebash](fakebash/cmd/fakebash/main.go) to intercept any shell command executions by the agent.
*   **Command Execution Sandbox**: Runs the [fakebashd](fakebash/cmd/fakebashd/main.go) daemon.
    *   This is where actual shell commands requested by the agent are executed.
    *   It mounts the active `runDir` read-write, allowing commands to read/write workspace files.
    *   **Credential Masking**: To prevent credential theft, sensitive directories such as `~/.gemini`, `~/.claude` and `~/.local/share/opencode` are masked with empty `tmpfs` mounts, ensuring that commands executed by the agent cannot read authentication keys.

### 2. Communication Protocol (`fakebash` & `fakebashd`)

The host process initializes a temporary host directory and bind-mounts it to `/fakebash` inside both sandboxes.

1.  **Command Interception**: When the agent attempts to run a shell command, it calls `/bin/bash`, invoking the `fakebash` client.
2.  **Allowlist Filtering**: The `fakebash` client checks if the command is in the allowlist (e.g., `agystatusline`, `show-output`). If allowlisted, it runs directly in the Agent Sandbox.
3.  **gRPC Forwarding**: Otherwise, `fakebash` establishes a gRPC connection over the Unix socket file at `/fakebash/fakebash.sock` to the `fakebashd` daemon running in the Command Execution Sandbox.
4.  **Execution & Streaming Persistence**: `fakebashd` executes the command and streams stdout/stderr chunks and exit codes back to `fakebash`. `fakebash` streams output directly to disk under `/tmp/fakebash-outputs/c-<unix-ms>-<pid>-<rand>.log` (atomic symlink at `latest.log`) with $O(1)$ memory usage.
5.  **Adaptive Compression & Escape Hatch (`show-output`)**:
    - **Fast-path Short Circuit**: Commands with output $< 256$ bytes and $\le 5$ lines immediately pass through untouched without evaluation latency.
    - **Level 1 Classifier**: Quick classification using TypeSafe Jev System One (`keep_raw`, `drop_on_success`, `extract_failure`, `summarize`).
    - **Level 2 Summarizer (10KB Threshold)**: Failed or verbose outputs $< 10$KB are summarized by Gemini Flash Lite directly; outputs $\ge 10$KB trigger the Inspector heuristic range extraction mode to prevent token overflow.
    - **Control Flags**:
      - Set `ASGARD_BASH_COMPACT=0` or `ASGARD_BASH_COMPACT_RAW=1` to completely disable output compression and pass raw output directly.
      - Configure `ASGARD_BASH_WATCHDOG_TIMEOUT` (default: 30s) for long-running command auto-passthrough.
      - Configure `ASGARD_GEMINI_TIMEOUT_MS` and `ASGARD_JEV_TIMEOUT_MS` for evaluation timeouts.
    - **show-output CLI**: The agent or developer can view raw, uncompressed outputs at any time via `show-output`:
      ```bash
      show-output              # Inspect output of latest executed command
      show-output <cmd-id>     # Inspect output of a specific command ID
      show-output --tail=50    # View the last 50 lines
      show-output --grep=FAIL  # Filter lines matching regular expression
      show-output --path       # Print absolute path to the raw log file
      ```
      *(Note: `show-output` shares the output directory `/tmp/fakebash-outputs`, filename convention `c-<unix-ms>-<pid>-<rand>.log`, and symlink `latest.log` with `fakebash/storage.go` as a standalone utility with no shared internal package dependencies.)*


## Deploying the Backend and Runners

### Backend configuration

Add a `runner` section to `~/asgard/config/config.yaml` (or the etcd config tree). Without it the runner listener is disabled and the backend cannot run any agent (every chat message fails with "no runner configured"); the WebUI, configuration and metadata APIs still work.

```yaml
runner:
  tls_cert_file: /certs/server.crt   # backend server certificate (required)
  tls_key_file: /certs/server.key    # required
  client_ca_file: /certs/ca.crt      # CA that signed the runner certificates (required)
  registry_port: 9311                # default 9311
  allowed_nodes: [runner-1]          # runner ids (client certificate CNs) allowed to connect
```

An empty `allowed_nodes` is valid and lets no runner connect. The section can be changed through live config reload.

**Agent definitions live in etcd** (`ETCD_ENDPOINTS` is required; `ETCD_PREFIX` defaults to `/asgard/configs`), not in `~/asgard/agents`:

```
<prefix>/agents/teams.yaml                      # teams (global)
<prefix>/agents/<agent_id>/<files>              # global agent: config.yaml, AGENTS.md, workflow.yaml, ...
<prefix>/runners/<node_id>/agents/<agent_id>/…  # agent that only exists on that runner
```

A runner's effective agent list is the global agents plus its own. Defining the same id both globally and under a runner is an error that invalidates that runner's list (the global list is unaffected). A parse error in a global agent or `teams.yaml` is fatal for loading; one under `runners/<node_id>/` only affects that runner.

API additions:
- `GET /api/runners`: `{"backend_version": "...", "runners": [{"node_id", "online", "last_seen", "version", "active_runs", "supported_clis": {cli: [models]}}]}`.
- `GET /api/quota[?runner=<node_id>]`: the quota lives on the runners. With `?runner=` it returns that runner's flat `{"<cli>": [ModelUsage...]}` (404 unknown, 503 offline, 502 runner error). Without it, one online runner returns the same flat object; otherwise `{"runners": {node_id: {...}}, "errors": {node_id: "..."}}` (503 when none is online).
- `POST /api/sessions` and the first `POST /api/agents/{id}/message` of a chat require `runnerId` (when runners are configured).
- Scheduled (cron) workflows of a global agent must set `runner_id` in the workflow definition; runner-owned agents always run on their own runner.

### Runner access (admin assignment)

A runner is **not assigned to any user** when it first connects: nobody can start a chat on it until an administrator assigns it. Assignments live in the `runner_assignments` table (`node_id`, `user_subject`, `assigned_by`, `created_at`); the `users` table lists the users an admin can pick (a user appears after their first login).

- An administrator is a user whose OIDC role claim contains `admin` (the same role that guards `/api/manage/*`). The role alone grants no runner; an admin must assign runners to themselves too.
- `GET /api/runners` only returns the runners assigned to the caller. Starting a new chat (`POST /api/sessions`, first `POST /api/agents/{id}/message`) on a runner that is not assigned to the caller returns `403`. The same check applies to messages and queue operations on an already-bound chat, so revoking a user's assignment cuts them off from that runner's chats too (running work is not interrupted). `call-peer` stays on the caller's runner. Chats have no owner: any user assigned to a runner can use that runner's chats.
- `GET /api/manage/runners` (admin): `{"runners": [{"node_id", "online", "users": [subject]}], "users": [{"subject", "name", "email"}]}`.
- `PUT /api/manage/runners/{node}/users` (admin) with `{"users": [subject, ...]}` replaces the users assigned to that runner. Changes are logged with the acting admin.
- The WebUI shows a "Runner Access" panel in Settings to admins.

### Runner certificates

The runner authenticates with a client certificate whose **CN is its node id**; the backend verifies it against `runner.client_ca_file` and checks the CN against `runner.allowed_nodes`. Example with a private CA (adjust names; the backend certificate must carry a SAN matching the host runners dial, or set the runner's `-server-name`):

```bash
openssl req -x509 -newkey rsa:4096 -nodes -days 3650 -subj "/CN=asgard-ca" -keyout ca.key -out ca.crt

# backend (server) certificate
openssl req -newkey rsa:4096 -nodes -subj "/CN=backend" -keyout server.key -out server.csr
printf 'subjectAltName=DNS:backend\nextendedKeyUsage=serverAuth\n' > server.ext
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 825 -extfile server.ext -out server.crt

# runner (client) certificate: CN = node id
openssl req -newkey rsa:4096 -nodes -subj "/CN=runner-1" -keyout runner-1.key -out runner-1.csr
printf 'extendedKeyUsage=clientAuth\n' > client.ext
openssl x509 -req -in runner-1.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 825 -extfile client.ext -out runner-1.crt
```

TLS 1.3 is required. The runner re-reads its certificate files on every reconnect, so rotation needs no restart. Node ids are also used as path segments in etcd, so they must be simple ids (no `/` or `..`).

### Running a runner

`asgard-runner` takes flags, with environment variables as fallback:

| Flag | Environment | Meaning |
|---|---|---|
| `-backend` | `ASGARD_RUNNER_BACKEND` | backend registry address `host:port` (required) |
| `-server-name` | `ASGARD_RUNNER_SERVER_NAME` | name checked in the backend certificate (default: host of `-backend`) |
| `-allow-unsandboxed-commands` | `ASGARD_RUNNER_ALLOW_UNSANDBOXED_COMMANDS` | **insecure, development only**: lets workflow command nodes with `sandbox: false` run without bubblewrap |

The runner reads TLS certificates from fixed paths inside `/certs`:
- Client certificate: `/certs/tls.crt` (CN is runner node id)
- Client key: `/certs/tls.key`
- CA certificate: `/certs/ca.crt`

The runner also needs the same `ETCD_ENDPOINTS` / `ETCD_PREFIX` as the backend (agent definitions, proxy and credential config), and the agent CLIs and credentials (`~/.claude`, `~/.gemini`, ...) of the user it runs as. Workflow command nodes run in the bubblewrap sandbox by default and fail closed: if the runner's sandbox setup is not ready the node fails; `sandbox: false` is refused unless the operator enabled the flag above (a request can never enable it).

Constraints to know before deploying:
- **Runner root directories must be real paths.** The directories agents and workflows may use (their `run_dirs` and mount dirs) and the runner's data roots are opened without following symlinks. A root that is, or lies under, a symlink is refused (fail closed); mount or configure the resolved path instead.
- **Keep the runner's TLS key out of the sandbox.** The command sandbox bind-mounts the runner user's `$HOME` read-write and only masks specific paths (`~/asgard/config`, `~/asgard/data`, `~/.ssh`, agent credential directories). The runner does **not** verify where its key lives, so a key under `$HOME` (or under any run dir/mount dir) is readable by agents and lets them impersonate the runner. Put the certificate files outside `$HOME` (for example `/certs`, as in the Docker example) with permissions limited to the runner user.
- **bubblewrap needs unprivileged user namespaces.** Inside Docker run the runner with a seccomp profile that permits `clone`/`unshare`/`mount`/`pivot_root` (for example `--security-opt seccomp=unconfined`) and, where AppArmor restricts user namespaces, `--security-opt apparmor=unconfined`.

### Docker images and versions

Two images are built from the `Dockerfile` (CI pushes both): `ghcr.io/agentdrasil/asgard-backend` (orchestration server, WebUI) and `ghcr.io/agentdrasil/asgard-runner` (agent CLIs, bwrap, ttyd, `asgard-runner`). Build locally with `just build-image-backend`, `just build-image-runner` or `just build-image`. Example run of a runner:

```bash
docker run --security-opt seccomp=unconfined \
  -e ETCD_ENDPOINTS=http://etcd:2379 \
  -v ./certs:/certs:ro -v ./runner-asgard:/asgard -v ./session-data:/session-data \
  ghcr.io/agentdrasil/asgard-runner:main \
  -backend backend:9311
```

See [examples/compose.prod.example.yaml](examples/compose.prod.example.yaml) for a backend plus runner compose file.

Builds are stamped with a version `YYYY-MM-DD-HH-MM-SS:abc123ed` (UTC build time and short commit); Docker tags use `-` instead of `:`. Backend and runner versions are shown in the WebUI under Settings → Runners and returned by `GET /api/runners`; the runner also logs its version at startup (`asgard-runner starting`).

### Upgrading a single-machine install

1. Add the `runner` section above and list the runner id in `runner.allowed_nodes`.
2. Generate certificates in `./certs` (`tls.crt`, `tls.key`, `ca.crt`) and start a runner on the same host (or a new one), with the node id from the list. If upgrading from an older layout where chat data lived under `~/asgard/data/{sessions,tmp}` (or `./runner-asgard/data/{sessions,tmp}`), move those directories into the new `/session-data` volume:
   ```bash
   mkdir -p ./session-data
   mv ./runner-asgard/data/sessions ./session-data/sessions
   mv ./runner-asgard/data/tmp ./session-data/tmp
   ```
   The runner owns `/session-data/sessions` and `/session-data/tmp` now.
3. Move the agent definitions and `teams.yaml` into etcd (`agents/…`, see above).
4. At startup the backend binds every existing chat without a runner to the **first** entry of `allowed_nodes` (`BackfillRunnerNodeID`; cron sessions take the runner from the workflow). Put the runner that holds the old chat data first.
5. Scheduled workflows of global agents need `runner_id`.

### Operations notes and known limitations

- **Transcripts live on the runner.** The backend database only holds metadata. While a runner is down its chats' history, files and terminals are unavailable until it reconnects.
- **Cleanup** runs daily on the backend: chats inactive for a month are removed through the runner's `Cleanup` RPC, and the database record is deleted only after the runner confirms. An offline runner is retried the next day.
- **Legacy workflow runs**: `workflow_runs` rows written before the split stored the DAG spec and input in database columns. These are not migrated and are no longer readable; the columns are left untouched.
- Runs lost because a runner restarted are reported as failed and are **not** automatically re-dispatched.
- Finalized run ids are stored in the backend database (`finalized_runs`) and are not pruned.
- Sandbox execution on the runner has so far been validated by unit tests (the generated bwrap arguments); the first real deployment is the actual validation.
- Confined file access resolves paths beneath the root with `openat2`, but bwrap binds directories by path, so a TOCTOU window exists between resolution and bind.
- A `/tmp` absolute path is ambiguous between the sandbox's `/tmp` (the chat's tmp directory) and the runner host's `/tmp`.

## Workflow Orchestration Engine

Asgard includes a DAG-based workflow engine (backend/lib/workflow) that orchestrates multi-step AI development pipelines combining autonomous coding agents, raw LLM calls, shell commands, and human-in-the-loop approvals.

### Key Capabilities
- **Fork-Join Parallel Scheduling**: Concurrently executes independent DAG nodes and aggregates results.
- **Heterogeneous Node Types**:
  - `agent`: Runs CLI-based coding agents (e.g. `agy-coder`) with session policy inheritance (`inherit` or `fresh`). Agent nodes take no `prompt` field; each agent is single-responsibility (one agent per node role, no cross-node reuse) with its instructions in `AGENTS.md`. The node marked `entry: true` receives the raw user input as its prompt; other fresh nodes get a kickoff directive and work off files produced by earlier nodes; resumed sessions get a follow-up directive. Scratch files in `AGENTS.md` use `/tmp/...` paths directly (the session tmp directory is bind-mounted at `/tmp` inside the sandbox); persistent per-chat files can use `/session/...` (bind-mounted from `/session-data/sessions/<chat-id>`).
  - `command`: Executes sandboxed or direct bash shell commands.
  - `llm`: Invokes raw LLM models (e.g. `gemini-3.5-flash`) for fast classification or summarization.
  - `human`: Pauses workflow execution for user review via WebUI / AskUser, persisting state across server restarts.
- **Smart Edge Conditions & Join Rules**:
  - `when`: Dot-notation expressions (e.g. `nodes.build_cmd.exit_code != 0`) to trigger conditional repair or fallback branches. Node result fields addressable in expressions: `status`, `exit_code`, `output`, `error`, `skip_reason`, and `loop_iteration.<loop_id>` (the owning loop's iteration counter snapshotted when the node settled).
  - `join: always`: Runs summary or clean-up nodes regardless of upstream skips or failures.
- **Declarative Loop Primitives (`loops`)**: First-class circuit breakers for self-healing pipelines while keeping the flat DAG architecture. Loops are declared as top-level metadata with per-edge counting markers:
  ```yaml
  max_node_executions: 500        # optional global per-node execution cap (default 100)

  loops:
    - id: fix_loop                # unique loop identifier
      nodes: [review, verdict, fixer]   # member nodes of the loop scope
      max_iterations: 5           # iteration quota (> 0 with on_exhausted)
      on_exhausted: fix_fallback  # node activated when the quota is exhausted (must not belong to any loop)

  nodes:
    - id: fixer
      depends:
        - node: verdict
          when: "nodes.verdict.exit_code == 0"
          counts_loop: fix_loop   # consuming this edge increments the loop counter
                                 # (and resets all descendant loop counters);
                                 # on exhaustion the re-entry is suppressed and
                                 # on_exhausted is activated instead
        - node: fix_fallback
          when: "nodes.fix_fallback.output == 'Retry (reset counter)'"
          resets_loop: fix_loop   # consuming this edge resets the loop counter
  ```
  - Counting edges fire when they drive an enqueue: conditional edges when their `when` matches, unconditional edges when the parent succeeded (or `join: always` allows it). A no-op re-entry (target already queued/running) never consumes an iteration.
  - Loop counters are persisted in `WAITING_HUMAN` snapshots and re-seeded on resume, so circuit breakers survive server restarts. Prompts and commands can interpolate the current counters via `${loops.<id>.iteration}`.
  - An `on_exhausted` target is an orphan: it has no static in-edges, is excluded from initial roots, and — when the quota is never exhausted — is swept to `SKIPPED (NEVER_ACTIVATED)` at settlement so happy paths stay `COMPLETED`. All human nodes (including those in loops, on_exhausted targets, and parallel branches) support concurrent execution. When a loop has no `on_exhausted`, a quota breach fails the re-entry target and settles the run `FAILED` (fail-closed). A reply containing "abort" from an activated `on_exhausted` human node settles the run `CANCELED`.
- **Command Exit Code Whitelist (`allowed_exit_codes`)**: Unix commands use non-zero exits for ordinary boolean results (e.g. `grep` exits 1 on no match). Command nodes can whitelist such codes as success:
  ```yaml
  - id: check_pending_steps
    type: command
    command: "grep -q 'status: pending' ${tmp_dir}/plan/todo.yaml"
    allowed_exit_codes: [0, 1]   # 1 is a normal, routable outcome here
  ```
  The real exit code is always preserved in the node result (success or failure), so downstream `when` edges route on the precise value; codes outside the whitelist settle the node `FAILED`. Only `type: command` nodes may declare the whitelist.
- **Sandbox-Friendly Workspaces**: Isolates intermediate step files under `tmp_dir` (defaults to `/tmp/${session_id}`).
- **Heterogeneous Model Pairing (`model_pairings`)**: Guarantees that a review node never reviews same-source model output silently. Each group maps every CLI target an actor agent may resolve to (its `cli:` list, quota fallback included) to an ordered reviewer fallback list; when the reviewer executes, the engine resolves the group's most recently completed actor's actually-used target and picks the first paired reviewer target with more than 10% quota remaining — per iteration, so mid-loop model drift re-pairs automatically. Every actor `cli:`-list target must be covered by a `pairs` key — the engine enforces this before executing any node (and `agent-validate` reports coverage gaps statically). A reviewer list should end with a pay-per-request target (the quota layer treats PPR as unlimited) so the fallback always terminates. If all paired reviewer targets are exhausted the run suspends (`WAITING_HUMAN`, options: wait / force a target / cancel) instead of silently reviewing with a same-source model; an unresolvable pairing key fails the node closed (`pairing key not found: <cli>/<model>`). A node-level `model:` override bypasses pairing entirely. See [examples/workflows/model-pairing.yaml](examples/workflows/model-pairing.yaml):
  ```yaml
  model_pairings:
    - id: code
      actors: [coding_agent, fix_agent]     # node ids whose output is reviewed
      reviewer: code_review_agent           # governed node (ignores its own cli: list in-workflow)
      pairs:                                # one entry shown; see the example for the full table
        - actor: {cli: agy, model: gemini-3.8-flash-low}
          reviewer:
            - {cli: opencode, model: zai-coding-plan/glm-5.3/high}
            - {cli: openrouter, model: anthropic/claude-sonnet-5}  # PPR tail
  ```
  Actual per-node `(cli, model)` selections (including fallbacks and pairing skips) are surfaced via node status events and persisted node states.

### Example Workflows (`examples/workflows/`)
Ready-to-use workflow definitions are located in [`examples/workflows/`](examples/workflows/):
- **[build-and-fix.yaml](examples/workflows/build-and-fix.yaml)**: Runs code generation, executes build checks, and conditionally triggers a fix agent if the build fails.
- **[human-in-the-loop.yaml](examples/workflows/human-in-the-loop.yaml)**: Generates a plan, pauses for human approval, uses a lightweight LLM classifier to parse natural language feedback, and conditionally proceeds with code execution.
- **[model-pairing.yaml](examples/workflows/model-pairing.yaml)**: Codes, reviews, and fixes with heterogeneous model pairing — the reviewer's model is dictated per iteration by the target the actor actually used.
- **[parallel-review.yaml](examples/workflows/parallel-review.yaml)**: Concurrently runs security and performance review agents and consolidates their reports.

### Validating Workflow & Agent Definitions (`agent-validate`)
Use the `agent-validate` CLI utility to validate workflow YAML syntax, DAG topology (cycle detection), edge expressions, and agent configs:

```bash
# Validate a standalone workflow definition
go run ./cmd/agent-validate ../examples/workflows/build-and-fix.yaml

# Validate an agent directory containing config.yaml & workflow.yaml
go run ./cmd/agent-validate ../.agents/agents/my-workflow-agent/
```

## API Endpoints

Asgard serves an HTTP REST & SSE API for agent orchestration, real-time events, team discovery, and system management:

### 1. Agent Execution & Real-Time Events API
*   **Trigger Execution** (`POST /api/agents/{agent_id}/message`): Triggers single agent or workflow execution. Returns `202 Accepted` asynchronously by default; supports `?wait=true` synchronous mode for CLI consumers (e.g., `call-peer`). With a `runner` section configured, every run executes on the runner the chat is bound to: the body must carry `runnerId` for a chat that has no runner yet (400 otherwise, 400 for an unknown runner), the binding is immutable (a different `runnerId` is `409`), and an offline runner answers `503` (queued messages stay queued and resume when the runner reconnects). `POST /api/sessions` accepts `runnerId` the same way.
*   **Real-Time Events Stream** (`GET /api/sessions/{session_id}/events`): Server-Sent Events (SSE) stream for real-time `SessionEvent` delivery (messages, activities, artifacts, status, done) with `Last-Event-ID` reconnection catch-up.
*   **Workflow command nodes** run on the chat's runner (`RunnerService.RunCommand`), in the same bubblewrap + `fakebashd` sandbox as agent commands; the backend never executes them for a runner-bound chat. The runner resolves the working directory beneath its allowed roots (symlinks and `..` that leave them are refused), streams stdout/stderr with byte offsets (the backend transparently resumes a reset stream by `request_id` and offsets, without re-running the command), enforces `timeout`, kills the whole process group when the node is cancelled, and reports `output_file` as an artifact. The sandbox is the default and fails closed: if the runner's sandbox setup is not ready the node fails. A node with `sandbox: false` runs only if the runner's operator started it with `-allow-unsandboxed-commands` (or `ASGARD_RUNNER_ALLOW_UNSANDBOXED_COMMANDS=1`); this is insecure and meant for development, and a request can never enable it. In sandboxed commands `${tmp_dir}`/`${session_dir}` of the chat's default directories expand to `/tmp` and `/session`; in unsandboxed ones to `${ASGARD_TMP_DIR}`/`${ASGARD_SESSION_DIR}` (the runner's own paths).
*   **List Agents** (`GET /api/agents`): Returns metadata and supported models of all loaded agents. `?runner=<id>` lists what that runner can run (global agents plus the runner's own).

### 2. Workspace & File Browsing API
*   **Workspace File Content** (`GET /api/v1/workspace/file`): Retrieves authorized file content or media streams (`?raw=true`) from session workspace, session tmp, or session `/session` directory. Supports `session_id`, `path`, and optional `scope` (`tmp`, `session`, or `workspace`) disambiguation parameters.
*   **File Content** (`GET /api/files/content`): Reads workspace, session temporary, or session `/session` file content with metadata. Supports `session_id`, `path`, and optional `scope` (`tmp`, `session`, or `workspace`).
*   **File Tree** (`GET /api/files/tree`): Lists directory contents and files within session workspace, session tmp, or session `/session` directory. Supports `session_id`, optional `path`, and optional `scope` (`tmp`, `session`, or `workspace`).
*   **File Search** (`GET /api/files/search`): Searches for files by query inside session workspace, session tmp (`/tmp` prefix), and session directory (`/session` prefix).

### 3. Management & Coordination API
*   **Public Config** (`GET /api/config`): Returns public configuration settings to web clients (including Web Push configuration `firebase_webpush_web`).
*   **Reload Configurations** (`POST /api/manage/reload`): Reloads the local agent definition YAML configurations dynamically without needing to restart the orchestrator server.
*   **Team Discovery** (`GET /team`):
    *   Finds the agent linked to the session queried by chat ID.
    *   Identifies that agent's team configurations.
    *   Returns details on all other agents that belong to the same team.

### 4. Compression Telemetry API
*   **Record Metrics** (`AgentCallbackService.RecordMetrics`, over the runner connection; there is no HTTP endpoint): Ingests batches of `{kind, tokens, bytes}` events reported by `fakebash` and `show-output` as they compress, truncate, or surface command output. The server aggregates them and persists the tally to `~/asgard/data/compression-metrics.json`.
*   **Read Metrics** (`GET /api/compression-metrics`): Returns the aggregated counters rendered by the WebUI compression statistics page: Jev and compass calls, tokens and per-call token averages, compressed outputs, truncations, and `show-output` calls with the raw bytes retrieved. It also derives the **retrieval rate** (show-output calls over compressed outputs), the headline quality signal — the agent calls `show-output` because the summary or classification dropped detail it needed, so a rising rate points at the classifier or summarizer prompt.

## Authentication (required OIDC)

Authentication is **required**: the backend refuses to start without an `auth`
section in `config.yaml`. It provides OIDC/OAuth 2.0 sign-in for the API and the
WebUI.

Sign-in itself is **nearly stateless**: there is no session table and no token
store. The only user-related database state is the `users` table (subject,
name, email, first/last seen) written on login, and the runner assignments
described below. The one piece of on-disk state is the avatar cache in
`~/asgard/data/avatars/`: each user's OIDC `picture` is fetched server-side
(public http/https hosts only, images up to 2 MiB) and served from
`/api/auth/avatar`, so browsers never contact the IdP. It is refetched when the
picture URL changes and has no eviction or TTL. Tokens live in the browser's `localStorage`, are verified
against the provider's JWKS on every request, and the only server-side state is
a short-lived in-memory CSRF `state` map. This means the `state` map is
per-process, so a deployment must run a single replica (or use sticky sessions).

### Configuration

```yaml
# host is the externally reachable base URL. It becomes the OAuth redirect base
# ({host}/auth/callback), and must be an absolute URL when auth is enabled.
host: "https://asgard.example.com"

auth:
  issuer: "https://auth.example.com/application/o/asgard/"
  client_id: "asgard-client-id"
  client_secret: "asgard-client-secret"

  # Which credential the API verifies: "access_token" (default) or "id_token".
  # access_token keeps sessions alive across refreshes (the access token is
  # re-issued, the ID token usually is not); use id_token when the provider's
  # access token is not a signed JWT (e.g. Google).
  token_source: "access_token"

  # Expected "aud" for access_token mode. Defaults to client_id. Providers whose
  # access tokens use another audience (e.g. Keycloak's "account") must set it.
  token_audience: "asgard-client-id"

  # Dot path of the role claim, and the role a user must carry. Both optional:
  # omit required_role to skip role validation entirely.
  role_claim: "roles"
  required_role: "asgard-user"

  # Extra authorization-request parameters for provider-specific requirements,
  # e.g. Google needs these to hand out a refresh token.
  # auth_code_options:
  #   access_type: "offline"
  #   prompt: "consent"
```

Register `{host}/auth/callback` as the redirect URI at the provider.

### Provider notes

This table lists what must be configured for sign-in to work — it is not a
zero-configuration checklist. With `token_source: access_token`, a provider
whose access tokens do not carry `aud=client_id` needs either `token_audience`
or an audience mapper.

| Provider | `token_source` | `token_audience` | `role_claim` | Provider-side setup |
|---|---|---|---|---|
| **Authentik** | `access_token` | default | `roles` (space separated) or `groups` (array) | Add a `roles`/`groups` scope mapping; allow refresh |
| **Authelia** | `access_token` | default | `groups` (array) | Request the `groups` scope and allow it in the policy |
| **Keycloak** | `access_token` | `account`, or add an audience mapper and keep the default | `realm_access.roles` (array) | Audience mapper is recommended; enable `offline_access` for refresh |
| **Dex** | `access_token` | default | `groups` (array) | Configure the `groups` claim |
| **Auth0** | `access_token` | your API identifier | namespaced custom claim | Create an API to define the audience; inject roles via a Rule/Action |
| **Google** | `id_token` | n/a (opaque access token) | none | Nothing; note the ID token is not renewed on refresh, so the session cannot be extended |

### How the API is protected

*   `/api/*`, `/team` and the metrics endpoint require a bearer token. `/api/*`
    requests send it as `Authorization: Bearer <token>`.
*   Channels that cannot set headers — the SSE stream, the TTYD WebSocket
    upgrade and resource loads such as `<img>`/`<iframe>`/`<a href>` — pass
    `?access_token=<token>` instead. Only `GET`/`HEAD` may authenticate this way,
    so a leaked URL cannot be used to mutate state.
*   The WebUI and its assets stay public, as does `GET /api/auth/status`, which
    the frontend uses to discover whether auth is enabled.
*   Status codes distinguish the failure modes: **401** means log in again,
    **403** means the account lacks `required_role` (logging in again cannot
    help), and **503** means the provider is unreachable and the client should
    keep its session and retry.
*   Agent-to-agent calls (`find-peer`, `call-peer`) and the other sandbox calls
    (`ask-user`, metrics) do not use HTTP to the backend at all: they go
    sandbox -> runner (`unix:///fakebash/asgard.sock`) -> backend
    (`AgentCallbackService` over the authenticated runner connection), so they
    are unaffected by the public middleware. The former internal loopback
    listener and its `internal_port` setting are gone (an old `internal_port`
    key in the config file is ignored).

### Security trade-offs

The refresh token is stored in `localStorage`, and the access token is passed in
the URL for header-less channels. Both are deliberate consequences of the
stateless design: any XSS can read the session, and URLs can leak into proxy
logs, browser history and the `Referer` header. Mitigations applied here are
short-lived access tokens with a rotating refresh token and a `Referer-Policy`
style hardening pass. Running behind an authenticating reverse proxy, or moving
to a session-cookie/BFF design, remains the stronger option if the threat model
requires it.

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
