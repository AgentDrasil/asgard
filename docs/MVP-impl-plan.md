# MVP Implementation Plan: AI Agent Orchestrator

Based on the Product Requirements Document (`MVP-PRD.md`), the MVP development is split into five manageable phases to ensure iterative progress, clear testing boundaries, and a solid foundation.

## Phase 1: Project Scaffolding & Core Foundations
**Goal:** Establish the foundational architecture, configuration management, and database layer.

* [Done] **Task 1.1: Project Setup**
  * Project already initialized with `cmd` and `lib`.
  * Continue setting up lib directory structure (`lib/db`, `lib/telegram`, `lib/aiagents`, `lib/api`).
* [Done] **Task 1.2: Configuration Management**
  * `lib/config/config.go` already implements basic Telegram config loading.
  * Add `db`, `dsn` and `agent_dir` to `Config` struct. And implement validation for these new fields.
* [Done] **Task 1.3: Database Implementation**
  * Use **GORM** for ORM.
  * Support **pure Go SQLite** (for local/test) and **PostgreSQL** (for prod/containerized).
  * Implement table schema: `topic_id` (Primary Key), `agent_name`, `session_id`.
  * Create repository interfaces and CRUD methods for Topic mapping.
* [Done] **Task 1.4: Startup Checks (CUJ 1)**
  * Implement the sequence of health checks on startup: Config validation, DB connection test, Telegram config presence (`AllowedSenders` check or helper bot startup), core directory (`$AGENT_DIR` and `auths`) validation.

## Phase 2: Agent Execution Engine
**Goal:** Build the engine capable of loading, configuring, and executing underlying CLI agents (specifically `gemini-cli`).

* [Done] **Task 2.1: Agent Configuration Management**
  * Define Go structs for Agent `config.yaml` (`name`, `cli`, `args`, `description`, `run_dirs`, `allow_dirs`).
  * Implement the Agent Loader to scan `$AGENT_DIR/agents/` and parse all configurations.
* [Done] **Task 2.2: CLI Execution Wrapper**
  * Implement the command execution logic (using `os/exec`).
  * Build the command builder to assemble CLI arguments (e.g., `--yolo`, `--output=json`, `--resume <session_id>`).
  * Inject necessary environment variables (e.g., `GEMINI_CLI_SYSTEM_SETTINGS_PATH`).
* **Task 2.3: Output Parsing & Session Management**
  * Parse the JSON output from `gemini-cli` to extract token statistics and the `session_id`.
  * Implement logic to persist the new `session_id` to the database after successful execution.
* **Task 2.4: Error Handling (CUJ 4)**
  * Catch CLI invocation failures and format raw error info to be passed back to the user.
* **Task 2.5: Interface & Mocking**
  * Define a clear `Agent` interface to decouple the orchestrator from the CLI execution.
  * Implement a `FakeAgent` to facilitate unit testing of routing and session logic.

## Phase 3: Telegram Bot Integration & Basic Routing
**Goal:** Connect the orchestrator to Telegram, handle incoming messages, and apply access controls.

* **Task 3.1: Telegram Client Setup**
  * Integrate a Telegram Bot API library.
  * Implement the message listener (polling or webhook).
  * Start the REST API server as the foundation for future decoupling.
* **Task 3.2: Access Control & Context Extraction**
  * Implement whitelist validation against `AllowedSenders`.
  * Extract `topic_id` and message text from incoming Telegram events.
* **Task 3.3: Output & State Interactions**
  * Implement functions to send standard messages as *new* messages in a topic (no reply quotes).
  * Implement Telegram "typing" status indicator during CLI execution.
  * Implement Topic renaming functionality via Telegram API.
* **Task 3.4: Basic Routing Logic (CUJ 2)**
  * Query the DB for the incoming `topic_id`.
  * If an Agent is bound, construct the execution request and forward to Phase 2 Engine.
  * If no Agent is bound, forward the request to the built-in "Reception Agent".

## Phase 4: Built-in Agents & Orchestrator API Integration
**Goal:** Implement the specific logic for the Default Agents and expose internal API capabilities to them.

* **Task 4.1: Default Agent Initialization (CUJ 1)**
  * Implement logic to check for the existence of `reception` and `god` agents on startup.
  * Embed default templates (system prompts and configs) for these agents.
  * Auto-generate them in `$AGENT_DIR/agents/` if they are missing.
* **Task 4.2: Auth File Mounting**
  * Implement a utility to physically copy files from `$AGENT_DIR/auths/` to the newly created agent's working directory (`run_dirs`).
* **Task 4.3: Orchestrator Internal Capabilities for Agents**
  * Implement capability/tool hooks that agents can call:
    * List available agents (for Reception Agent).
    * Bind Topic to Agent & Rename Topic (for Reception Agent).
* **Task 4.4: Reception Agent Routing Handlers**
  * Implement the specific orchestrator handler to intercept Reception's decision, bind the topic, rename it, and output the standard `Reception Agent -> Target Agent Name` message before forwarding to the target agent.

## Phase 5: God Agent Integration & End-to-End Testing
**Goal:** Finalize the Dynamic Agent Creation flow and conduct comprehensive testing.

* **Task 5.1: God Agent Capabilities**
  * Define the specific system prompt for the God Agent to guide users in creating new agents.
  * Ensure the God Agent has the necessary tool integration to write `config.yaml` and prompt files into `$AGENT_DIR/agents/<name>/`.
* **Task 5.2: CUJ 3 E2E Integration**
  * Test the flow: User asks Reception -> Reception routes to God -> God collects info -> God writes files -> Orchestrator mounts auths -> New Agent is available.
* **Task 5.3: Edge Cases & Polish**
  * [Done] Handle missing `AllowedSenders` (start helper bot to print user ID).
  * Review and refine all error messages (timeouts, CLI crashes).
  * Ensure concurrent topic requests are handled safely by the execution engine without race conditions.
