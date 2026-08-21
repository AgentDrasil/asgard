# Known Limitations & Residual Boundaries

## Multi-waiter Human Interaction Architecture

The workflow engine supports concurrent and sub-workflow human interactions:
- **Multiple Pending Asks per Session**: Multiple workflow runs (and multiple concurrent `human` nodes within the same run or fan-out sub-workflows) can be suspended waiting for human input (`WAITING_HUMAN`) simultaneously within a single chat session.
- **Precise Reply Routing**: User replies with a `message_id` (deterministic `wf-<run_id>-<node_id>`) are routed directly to the specific suspended node using `ResumeByMessageID` and verified against session ownership to prevent cross-session hijacking (m9).
- **Single-Wait Fallback**: When an empty `message_id` is supplied, the engine checks the total number of suspended interactions in the session; if and only if there is exactly 1 waiting interaction, it gracefully routes the reply to that interaction (R3).

## Residual Boundaries & Operational Edge Cases

1. **Duplicate Reply Interception (m4)**: When the final waiting human node of a run is resumed, subsequent duplicate replies are intercepted and discarded by the in-memory execution guard (`e.executing`), preventing double resumption. A stale snapshot record only remains if the process crashes before completion.
2. **Abandoned Stale Run Interactions (m5)**: If a chat session contains abandoned stale suspended runs, legacy clients that submit replies without a `message_id` will safely no-op (due to `totalWaiting > 1` ambiguity prevention) rather than executing an arbitrary resumption.
3. **Concurrent Sibling Resume Replay Window (m7)**: When multiple parallel human nodes in the same run are resumed concurrently, siblings arriving during the initial replay startup window wait up to 100ms for in-memory registration before falling back safely.
4. **Sub-workflow Restart Settlement (m10 / R5)**: Sub-workflows running in-process fully support suspension and resumption. If the server crashes or restarts, any uncompleted parent run in `RUNNING` status is swept to `FAILED` by the startup cleaner (`ResetAllRunningWorkflows`). While the child run snapshot can be independently inspected or replayed, the parent run does not automatically resume.
5. **Card In-Place Refresh on Re-suspension (n8)**: When a run is resumed from a snapshot and suspends on a downstream human node, existing client UI components refresh in place matching the newly generated suspension message.
