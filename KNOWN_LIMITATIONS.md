# Known Limitations

## One pending human interaction per chat session

**What.** At most one workflow run per chat session may be suspended waiting
for a human reply (`WAITING_HUMAN`) at any time. Concretely:

- Workflow definitions must order their `human` nodes totally; two human
  nodes that may run concurrently are rejected at validation time
  (`validateHumanNodes`).
- A workflow referenced by a `workflow` (fan-out) node must not contain
  `human` nodes at all; this is enforced when the parent definition is
  resolved.

**Why.** User replies are routed by session, not by prompt: the backend
looks up *the* suspended run for the chat session
(`FindWaitingHumanBySession`) and delivers the reply to it. With multiple
pending asks in one session, a reply cannot be matched to the ask it answers.

**Impact.** The web UI is capable of rendering multiple concurrent
`ask_user` prompts, but the backend can only resolve one pending reply per
session. Additional pending asks would receive misrouted or dangling
replies, so the restriction is enforced statically instead.

**Future direction.** The groundwork for lifting this already exists:
suspension events carry deterministic MessageIDs (`wf-<run_id>-<node_id>`).
Routing replies by `message_id` (instead of "the suspended run of the
session") and allowing multiple `WAITING_HUMAN` records per session would
unlock parallel human nodes and human nodes inside fan-out sub-workflows.
