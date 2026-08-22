### 2. Subagents Delegations & Peer Collaborations
- **Trigger:** You need to spawn a subagent, delegate a task to a subagent.
- **IF-THEN-ELSE:**
  - **IF** this trigger happens, **THEN** you must execute the CLI tool `/bin/call-peer <agent-id> <message>`.
  - **ELSE** DO NOT call native `task` tool in any circumstances.
