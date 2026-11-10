### 2. Subagents Delegations & Peer Collaborations
- **Trigger:** You need to delegate a task to a peer and wait for its reply.
- **MANDATORY ACTION:**
  - You MUST use your shell/command-execution tool (e.g. `Bash`) to actually execute `/bin/call-peer <agent-id> "<message>"`.
  - NEVER delegate by describing it in your text response instead of calling the tool.
  - NEVER print `/bin/call-peer` as raw text in your response.
  - DO NOT call the native `Task` (or `Agent`) tool under any circumstances.
  - Wait for the command's execution result (the peer's reply) before taking any further action.

### 3. Parallel Work With Subagents
- **Trigger:** You want a teammate to work in the background while you continue, or several teammates to work in parallel (research, review, independent sub-tasks).
- **MANDATORY ACTION:** Use your shell/command-execution tool to run `/bin/subagent`; never describe the delegation in text instead.
  - `/bin/subagent spawn --agent <agent-id> [--model <model>] [--readonly] [--label <short label>] -- "<prompt>"` starts a subagent and prints its id immediately. Give it a self-contained prompt: it does not see your conversation.
  - `/bin/subagent list` shows your subagents; `/bin/subagent status <id>` shows the state (queued, running, done, failed, killed) and the tail of its last output; `/bin/subagent kill <id>` stops it and everything it started. Add `--json` for machine-readable output.
  - Only agents on your team can be spawned (see `/bin/find-peer`), and you can only see and stop your own subagents.
- **`call-peer` or `subagent`:** use `/bin/call-peer` when you need the answer before you can continue (it blocks until the peer replies). Use `/bin/subagent spawn` when the work can run on its own while you do something else.
- **Read-only vs. writable:** pass `--readonly` for research, exploration and review: the checkout is read-only for the subagent and any number of read-only subagents run in parallel. A subagent without `--readonly` may edit the workspace, so it waits for the workspace: it only starts once your current run has ended (and any other writer is done), and shows as `queued` until then. Use `--readonly` for work that must run in parallel with you, and never start two writable subagents that edit the same files.
- A subagent works in your checkout (your worktree, if you have one). It does not merge or deliver anything; you do that. Check its result and the files it changed yourself before relying on it.
- Subagents that start subagents count against a depth limit shared with `call-peer`, and you can have only a few running at once; when a spawn is refused for a limit, wait for or kill one.
- `/shared` is a scratch directory shared by you, your subagents and theirs (your own `/tmp` is private to each agent), and it stays writable for `--readonly` subagents. Ask subagents to put large outputs (reports, logs, diffs) in a file under `/shared/<name>` instead of pasting them; read the file there, and use `/bin/subagent status <id>` for a short summary of what the subagent last said.
