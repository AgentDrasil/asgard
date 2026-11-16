# Subagent team: orchestrator -> planner / planreviewer / impler / implreviewer / fixer

Layout mirrors the etcd key layout (`<prefix>/agents/...`). Each agent has a `config.yaml` and an `AGENTS.md` (system prompt).

```
agents/
├── teams.yaml        # team "dev"
├── orchestrator/     # main agent, claude opus, writable; the entry point, spawns and controls everyone
├── planner/          # subagent, claude opus, lock_mode: readonly; talks to the human through orchestrator
├── planreviewer/     # subagent, opencode GLM-5.3, lock_mode: readonly; new one per plan round
├── impler/           # subagent, agy gemini flash, writable; one per Plan item
├── implreviewer/     # subagent, opencode GLM-5.3, lock_mode: readonly; three per round, one lens each
└── fixer/            # subagent, claude sonnet, writable; one per Plan item, resumed across rounds
```

## Spawn tree

Every agent is spawned directly by orchestrator, so the tree is flat (depth 2; the limit is 4 and the root counts as 1):

```
orchestrator                          depth 1, user-started root chat
├── planner --readonly                depth 2, one for the whole task
├── planreviewer --readonly           depth 2, new per plan round
├── impler (one per Plan item)        depth 2, writable, serialized by the checkout lock
├── implreviewer --readonly x3        depth 2, new per round, one lens each
└── fixer (one per Plan item)         depth 2, writable
```

Flow: planner aligns with the user and writes the plan, up to 3 plan review rounds, then per Plan item impler -> 3 implreviewers -> (fixer -> 3 new implreviewers)* with at most 4 fix rounds, finally orchestrator commits `docs/plans/<topic>.md`.

The impl review fans out to 3 lenses (`correctness-concurrency`, `security-sandbox`, `tests-docs`), not 4: at most 4 children of one parent run at once, and one slot must stay free so an ask can resume the idle planner. Orchestrator merges the three lens files into `summary.md` itself.

## Who talks to whom

Questions go straight to the right instance with `subagent ask --topic <t> -- "<q>"`. Orchestrator registers routes on its own chat with `subagent route add --topic <t> --to <chat-id> --note "..."`. A route maps a topic to a chat uuid, never to a role. `subagent route list` shows the effective table.

| Topic | Registered by | Points to | Ask it |
|---|---|---|---|
| `user` | implicit default | orchestrator (root) | what the human wants; subagents' plain `ask-user` lands here |
| `build` | orchestrator, before spawning impler | orchestrator | building, running, fixing the code |
| `ordering` | orchestrator, before spawning impler | orchestrator | order of Plan items and commits |
| `plan` | orchestrator, right after spawning planner | the planner instance | design, plan, decisions, scope |

| Asker | Asks | Notes |
|---|---|---|
| planner | the human via plain `ask-user` | default `ask_user: escalate`: lands at orchestrator as `[question <qid> ...]` prefixed `(escalated ask-user)`; orchestrator calls `ask-user` and `subagent answer <qid>` |
| planreviewer | `plan` | |
| impler | `plan`, `build`, `ordering` | |
| implreviewer | `plan` | |
| fixer | `plan`, `build` | |
| orchestrator | the human via `ask-user` (root: allowed); never asks the planner | `[plan]` findings and design fallbacks go to the planner with `subagent send` + `wait` (an ask could cycle with the planner's escalated ask-user), before the fixer runs |

Common mechanics:

- Before asking, read `/shared/<topic>/decisions.md`.
- `subagent ask` blocks for the 90s default. Exit 3 prints `pending <qid>`; continue with `subagent ask --resume <qid>`.
- Claude Code's Bash tool kills a command at 120s: use the 90s default or `--timeout 90s`, never `300s`.
- Subagents do not set `ask_user` in their configs: the platform default (root `allow`, subagents `escalate`) applies.

What to do when `ask` fails (match on the error text):

| Error contains | Do |
|---|---|
| `cannot start a turn while`, `leads back to you`, `is archived`, `was killed` | fallback: end the turn with a LAST line `QUESTION: <q>` (reviewers note the doubt in their file instead) |
| `retry in a few seconds` | wait briefly and retry |
| `too many open questions` | `subagent questions`, resume the pending ones |
| `finished without answering`, `question ... not found`, `backend restarted` | ask again |
| `no route for topic`, `no longer exists`, `not in your spawn tree` | run `subagent route list` once, then fall back as in the first row |
| `was replaced by a newer` | another resume is already waiting; do not double-resume |
| `too many running subagents` | wait briefly, retry a few times, then fall back as in the first row |

`QUESTION:` fallback: after every `wait` and pushed finish notice orchestrator checks `subagent status <id> | awk 'NF{l=$0} END{print l}' | grep -E '^[[:space:]]*QUESTION:'`, answers with `subagent send <id> -- ...` and waits again. Before ending a turn, every asker runs `subagent questions` and resumes every pending qid: a late `[answer ...]` restarts a finished asker.

## Does planner restart or resume?

Resumed. The planner is one instance (one uuid) for the whole task, with `session_mode: resume`:

- Plan review `CHANGES_REQUIRED`: orchestrator `subagent send`s the review path to the same planner, which keeps the user Q&A context. The `plan` route stays valid.
- A question from impler, implreviewer, fixer or planreviewer to a finished (`done`) planner starts a resumed turn of that same chat. The planner answers with `subagent answer`, updates `decisions.md` (and `plan.md` if it changes), and ends its turn. Such a resumed turn counts toward orchestrator's 4-running-children cap, hence 3 lens reviewers.
- A `--readonly` spawn takes no checkout lock, so asking the planner never trips the checkout deadlock check, even while a writable impler or fixer holds the checkout. This is why `--readonly` is REQUIRED on the spawn: `lock_mode: readonly` alone still takes a shared lock that conflicts with a writable impler, and an ask to the finished planner would fail fast with `cannot start a turn while <impler> holds the checkout`.
- Reviewers are different on purpose: planreviewer and implreviewer are new per round to avoid anchoring on their earlier verdict. The fixer is resumed across rounds of one item (it knows what it already changed).
- A planner restarted by an ask finishes with a notice to orchestrator that it did not request; orchestrator ignores it.

## /shared namespacing

`/shared` is one directory for the whole spawn tree. `/session` is private per chat and is not visible to other chats, so state passing uses `/shared/<topic>/...` only:

```
/shared/<topic>/plan.md, decisions.md
/shared/<topic>/planreview/r<n>/review.md
/shared/<topic>/impl/<item>/                          impler notes
/shared/<topic>/implreview/<item>/r<n>/<lens>.md, summary.md
```

The orchestrator picks a unique `<topic>`; labels must not contain `]` (it breaks parsing of the `[subagent <id> (<label>) ...]` prefix).

## Install

Copy `agents/*` into your `etcd-tree/agents/` and run `just publish` (`cfgctl push`). Then:

- Change `run_dirs` in `orchestrator/config.yaml` to your repo.
- Change the models to ones your runner's catalogs list.
- All agents must be on the same team (`team: dev`), or `subagent spawn` is refused.

## Notes

- Only `orchestrator` is a main agent and has `run_dirs`; subagents work in their parent's checkout.
- orchestrator commits only `docs/plans/<topic>.md` at the end (index checked first). impler and fixer stage only their own files.
- Writable `impler` and `fixer` take the checkout lock only while their parent waits (`subagent wait`) or is idle-held. A `wait` that returns while a child still runs can leave it holding the checkout: the output says so (`checkout_held_by`), and orchestrator must not modify files until it waits again.
- `planner`, `planreviewer` and `implreviewer` use `lock_mode: readonly`; always pass `--readonly` too: only a `--readonly` spawn skips the checkout lock (see "Does planner restart or resume?").
- All agents use `session_mode: resume`: each spawn is a new chat anyway, and resume keeps context when a parent `send`s a follow-up or answer.

## Worktrees

This example does not support worktree chats as-is. A read-only child in a worktree run cannot see the owner's unmerged commits through git (see the main README's known limitations), which breaks review. To adapt it, give every agent `worktree.enabled: true` with the same `worktree.root` and `run_dirs`, and make the reviewers writable (no `lock_mode: readonly`, which `worktree` rejects, and drop `--readonly` from their spawns).

## Known limitations

- Writable implers and fixers serialize on the checkout lock; there is no per-subagent worktree (see Worktrees).
- orchestrator must not end its turn while children run: an idle-held target cannot take a question while a writable descendant holds the checkout, so every ask toward it fails fast. It stays in `subagent wait --timeout 90s` calls within one turn.
- A question cannot be withdrawn. If the asker ends its turn with it pending, the late `[answer ...]` restarts the finished asker for a stray turn. Askers resume every pending qid before ending, and ignore a stray answer.
- Open questions live in backend memory: a restart loses them, and `ask --resume` then fails with not-found. Re-ask.

## Validation

`pkg/agentspec/examples_test.go` parses every agent with `agentspec.ParseFilesWithCLIs(..., NoCLIValidation())` and checks team consistency, that every `ask --topic` is registered by an ancestor's `route add` (or is `user`), and that no snippet uses a `--timeout` above 90s. Model names are not checked against a live runner.
