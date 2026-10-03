### 2. Subagents Delegations & Peer Collaborations
- **Trigger:** You need to spawn a subagent, delegate a task to a subagent or peer.
- **MANDATORY ACTION:**
  - You MUST use your shell/command-execution tool (e.g. `bash` / `run_command`) to actually execute `/bin/call-peer <agent-id> "<message>"`.
  - NEVER delegate by describing it in your text response instead of calling the tool.
  - NEVER print `/bin/call-peer` as raw text in your response.
  - DO NOT call a native `task` or `subagent` tool under any circumstances.
  - Wait for the command's execution result (the peer's reply) before taking any further action.
