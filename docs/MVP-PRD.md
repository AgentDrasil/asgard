# Product Requirements Document (PRD): AI Agent Orchestrator MVP

## 1. Project Overview

This project aims to develop a locally-hosted/containerized AI Agent Orchestrator. The orchestrator manages multiple CLI-based AI Agents (primarily `gemini-cli` for the MVP phase) through unified underlying scheduling and exposes interactive capabilities via a RESTful API. The default frontend uses Telegram Super Groups/Topics to deliver a seamless conversation and agent-routing experience.

**Target User & Deployment Model:** Single-user private deployment, highest-level access control, no multi-tenancy considered.
**Core Tech Stack:** Golang (Orchestrator logic), Docker (Final deployment environment), Telegram API (User interaction).

---

## 2. Core Definitions

* **AI Agent:** An execution unit that wraps underlying CLI tools (like `gemini-cli`). In the MVP, it is primarily invoked via `gemini --yolo --output=json -p "xxx"`. State tracking relies on the returned JSON output (which includes `session_id` and token statistics).
* **Configuration Management:** Agent definitions are managed via YAML configuration files located at `$AGENT_DIR/agents/<agent_name>/config.yaml`. The orchestrator controls agent behavior by injecting environment variables (e.g., `GEMINI_CLI_SYSTEM_SETTINGS_PATH`).
* **Session Isolation:** In Telegram interactions, **1 Topic = 1 Independent Conversation Session**. The orchestrator queries the database using the Topic ID to find the bound `session_id` and uses `--resume <session_id>` to maintain context.
* **Default Core Agents:**
* **God Agent:** The "Creator" of the system. Responsible for creating new Agent configuration files and working directories through multi-turn conversations with the user. It writes files directly to `$AGENT_DIR/agents/`, and the new Agent takes effect immediately.
* **Reception Agent:** The "Front Desk" of the system. Responsible for greeting users in newly created Topics, understanding their intent, and routing the Topic to the appropriate business Agent (including the God Agent). It does not require a persistent working directory and is allocated a `/tmp/<uuid>` at runtime.



---

## 3. System Architecture Boundaries

1. **API-First Design:** All core functions of the orchestrator must be exposed through a RESTful API. The Telegram Bot is essentially a specific client calling this API. This thoroughly decouples core logic from interaction channels, facilitating the future development of other frontends and automated testing.
2. **Sub-agent Support:** The MVP allows Agents to invoke natively supported sub-agents (e.g., allowing a Coding Planner to run a Code Investigator). Complex custom orchestration across Agents is deferred to future versions.

---

## 4. Core User Journeys (CUJ)

### CUJ 1: Initial Startup & Health Check

Upon startup, the system must sequentially pass the following checks. Any unexpected failure should result in an error and exit:

1. Check if `config.yaml` exists (must include database connection and Telegram configurations).
2. Test the database connection; exit on failure.
3. Check Telegram configurations (`BotToken`, `AllowedSenders`):
    * `AllowedSenders` is a whitelist of Telegram User IDs configured in `config.yaml`.
    * If `AllowedSenders` is missing, start a helper bot to assist the user in retrieving their ID.
4. Check if the core working directory `$AGENT_DIR` and authorization file `$AGENT_DIR/auths/gemini.json` exist (MVP relies on host machine or manually mapped OAuth credentials).
5. **Initialize Default Agents:** Check if `reception` and `god` exist under `$AGENT_DIR/agents/`. If not, create them using templates hardcoded in the orchestrator.
    * *Note: The user can freely modify or delete these two default Agents. If deleted, the system will regenerate them using the code templates upon the next restart.*


6. Start the RESTful API Server and Telegram Bot listening service.

### CUJ 2: Start Conversation & Auto-Routing

1. **Receive Message:** The user creates a new Topic in Telegram and sends a message.
2. **Whitelist Validation:** The orchestrator checks if the sender is in `AllowedSenders`. If not, the message is ignored.
3. **Context Check:** The orchestrator checks the Topic ID in the database:
    * **Agent Assigned:** Extracts the bound `session_id`, forwards the request directly to that Agent, and maintains the conversation via `--resume`.
    * **No Agent Assigned:** Forwards the message to the **Reception Agent** for processing.
4. **Reception Agent Routing Logic:**
    * The Reception Agent calls a built-in capability (via a simple script reading the `description` fields of all `config.yaml` files under `$AGENT_DIR/agents/`) to retrieve a list of available Agents.
    * **Clear Intent (Matching Agent Found):** Reception completes the following sequence:
        1. Reception rewrites the user's request to generate a forwarded message suitable for the target Agent.
        2. The Orchestrator writes the binding between the Topic and the target Agent to the database.
        3. The Orchestrator calls the Telegram API to rename the Topic title to the target Agent's name (e.g., `"Agent: Coding Planner"`).
        4. The Orchestrator sends a user-visible message in that Topic, formatted as:
        ```text
        Reception Agent -> Target Agent Name:
        <Rewritten Message Content>
        ```
        5. The Orchestrator forwards the rewritten message to the target Agent and returns the Agent's reply to the Topic.
    * **Vague Intent (Greeting or No Matching Agent):** Reception continues interacting with the user, asking for requirements and listing all currently known Agents for reference.

### CUJ 3: Dynamic Agent Creation (via God Agent)

1. **Wake God Agent:** The user creates a new Topic and sends a message expressing the intent to create an Agent (e.g., "I want to add a new agent"). Reception identifies this intent and routes the Topic to the God Agent (following the standard routing process in CUJ 2).
2. **Multi-turn Inquiry:** The God Agent collects the following necessary information via multi-turn conversation:
    * **Name** (Agent Name, used as a unique identifier)
    * **Tool** (e.g., `gemini`)
    * **Core Task** (Used to automatically generate a short `description` and a detailed system prompt)
    * **Directory Permissions** (`run_dirs` and `allow_dirs`, defaults to running in a single working directory or its subdirectories)


3. **Asset Generation:** The God Agent directly generates `config.yaml` and the system prompt file under `$AGENT_DIR/agents/<name>/`. *Effective Timing: The new Agent is immediately discoverable the next time Reception calls the list agent skill, requiring no orchestrator restart.*
4. **Auth Mounting:** The orchestrator automatically physically copies the auth files from `$AGENT_DIR/auths/` into the newly created Agent's working directory.
5. **Completion Prompt:** Once creation is complete, the God Agent notifies the user within the Topic that the creation is finished and the Topic can be deleted.

### CUJ 4: Error Handling

* **CLI Invocation Failure** (Timeout, unexpected format, etc.): Return the raw error information (raw JSON or error text) directly to the user's Topic. Do not perform silent retries.
* **Session Recovery (`--resume`):** While `--resume` can recover context in some cases, the MVP phase will not include auto-retry logic for it. Errors are passed directly to the user.

---

## 5. Telegram Interaction Specifications

* **Message Sending Method:** When replying, the orchestrator sends a *new* message within the corresponding Topic (does not use the `reply` quote feature).
* **Typing Status:** While waiting for the CLI response, the orchestrator triggers the `typing` action via the Telegram API.
* **Streaming Output:** Not supported in MVP. The message is sent in its entirety once the CLI returns the complete response.
* **Whitelist Validation:** All messages are strictly validated against the `AllowedSenders` list in `config.yaml`. Unauthorized messages are ignored.

---

## 6. Agent Configuration Specifications (YAML Draft)

Each Agent is defined by `$AGENT_DIR/agents/<name>/config.yaml`:

| Field | Description |
| --- | --- |
| `name` | Unique identifier for the Agent |
| `cli` | Underlying tool used, e.g., `gemini` |
| `args` | Startup arguments, e.g., `["--yolo"]` |
| `description` | Short description, used as a reference for Reception Agent routing |
| `run_dirs` | The root directory where the Agent actually executes commands |
| `allow_dirs` | List of directories the Agent is permitted to access/read (Prep for Future Sandbox) |

---

## 7. Database Design (MVP)

In the MVP phase, the database only persists the following states:

| Field | Description |
| --- | --- |
| `topic_id` | Telegram Topic ID (Unique Key) |
| `agent_name` | The Name of the bound Agent |
| `session_id` | The CLI session ID of the current conversation |

---

## 8. Future Section (TODO / Not Included in MVP)

1. **Reception Fallback Routing:** Introduce a General Agent as a fallback when no specific Agent is found, and prompt the user to create a new one.
2. **Complex Memory System:** Including `load_memory` / `save_memory` mechanisms, and dynamically injecting relevant background memory before the User Prompt.
3. **Context Window Management:** Automatically monitor token usage rate. Trigger a new `session_id` rotation and memory summarization reconstruction when it exceeds 50%.
4. **Agent Handover Mechanism:** Support transferring a Topic across multiple Agents, requiring the database to record the complete Agent chain.
5. **`ask_agent` Support:** Explicit invocation and result-return mechanisms between Agents.
6. **Dynamic Skill Installation:** Grant Agents the ability to dynamically install and mount third-party tools at runtime.
7. **CLI-Specific Config Generation:** Have the God Agent automatically generate detailed `settings.json` required by specific CLIs.
8. **Sandbox Isolation Mechanism:** Implement true underlying execution environment isolation based on `allow_dirs` and `fake-bash`, filtering or hiding auth files. The God Agent will be modified to write files via fake-bash tools instead of direct filesystem manipulation.
9. **Auth File Hook:** Refactor the current hardcoded logic of copying Auth files into an event-based Hook mechanism.
