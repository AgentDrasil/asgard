# Subagent team: architect -> lead -> impler / review-lead -> reviewer

Layout mirrors the etcd key layout (`<prefix>/agents/...`). Each agent has a `config.yaml` and an `AGENTS.md` (system prompt).

```
agents/
├── teams.yaml       # team "dev"
├── architect/       # main agent, claude opus, writable; the only one that talks to the user
├── lead/            # subagent, claude opus, writable; runs the impl/review/fix loop
├── impler/          # subagent, agy gemini flash, writable; one per Plan item
├── review-lead/     # subagent, claude opus, lock_mode: readonly
├── reviewer/        # subagent, opencode GLM-5.3, lock_mode: readonly
└── researcher/      # subagent, agy gemini flash, lock_mode: readonly
```

## Spawn tree

```
architect                           depth 1, user-started root chat
├── researcher --readonly  x<=4     depth 2, parallel
└── lead                            depth 2, writable
    ├── impler  (one per Plan item) depth 3, writable, serialized by the checkout lock
    └── review-lead --readonly      depth 3, one per commit and round
        └── reviewer --readonly x4  depth 4, one lens each
```

The root counts as depth 1, so reviewer sits exactly at the limit of 4 (the counter is shared with `call-peer` chains): reviewer must not spawn or `call-peer`, and architect must be started by the user, not reached through `call-peer`.

`review-lead` exists to keep lead's context clean (lead reads one `summary.md`, not four lens files) and to dedup the four lenses in one place.

## Who talks to whom

Questions go straight to the right instance with `subagent ask --topic <t> -- "<q>"`, not hop by hop through the parent. Each spawner registers routes on its own chat before it spawns, with `subagent route add --topic <t> --to $ASGARD_CHAT_ID --note "..."`. A route maps a topic to a chat uuid, never to a role: two leads (or architects) in different subtrees each resolve the topic to their own instance, and `subagent route list` shows the effective table. A topic resolves through the asker's ancestors, nearest definition wins.

| Topic | Registered by | Points to | Ask it |
|---|---|---|---|
| `user` | implicit default | architect (root) | what the human wants; subagents' plain `ask-user` lands here too |
| `design` | architect, before the first spawn | architect | layout, API, behavior decisions |
| `scope` | architect, before the first spawn | architect | what is in or out of the task |
| `build` | lead, before spawning impler | that lead | building, running, fixing the code |
| `ordering` | lead, before spawning impler | that lead | order of Plan items and commits |
| `review` | review-lead, before spawning reviewers | that review-lead | review scope and lens questions |

| Asker | Asks | Notes |
|---|---|---|
| impler | `design` (architect directly), `build`, `ordering` (lead) | |
| researcher | `design` | continues on a best guess meanwhile |
| reviewer | `design`, `review` | at the depth limit; ask only, never spawn |
| lead, review-lead | `design`, `scope` | |
| architect | the human, via ask-user | only for true scope changes; it is the root, so ask-user is allowed |
| review-lead | `summary.md` | `VERDICT:` on line 1, read by lead; not a question |

Flow:

- Before asking, read `/shared/design/<topic>/decisions.md`.
- `subagent ask --topic design -- "<q>"` blocks for the 90s default. Exit 3 prints `pending <qid>`; continue with `subagent ask --resume <qid>` and do other work in between if there is any.
- The target gets `[question <qid> from ...]`, which wakes its `subagent wait` (`woken_by_message`). It answers with `subagent answer <qid> -- "<text>"`; `subagent questions` lists what is open. architect appends every Q&A to `decisions.md`.
- Claude Code's Bash tool kills a command at 120s: use the 90s default or `--timeout 90s` for `wait` and `ask`, never `300s`.
- Subagents do not set `ask_user` in their configs: the platform default sends a subagent's plain `ask-user` to its `user` route (architect, not the human). The prompts still say to prefer `ask --topic`.

What to do when `ask` fails (match on the error text):

| Error contains | Do |
|---|---|
| `cannot start a turn while`, `leads back to you`, `is archived`, `was killed` | fallback: end the turn with a LAST line `QUESTION: <q>` |
| `retry in a few seconds` | wait briefly and retry |
| `too many open questions` | `subagent questions`, resume the pending ones |
| `finished without answering`, `question ... not found`, `backend restarted` | ask again |
| `no route for topic`, `no longer exists`, `not in your spawn tree` | run `subagent route list` once, then fall back as in the first row |
| `was replaced by a newer` | another resume is already waiting; do not double-resume |

`QUESTION:` fallback: the asker ends its turn with a LAST line `QUESTION: <q>`. After every `wait` and pushed finish notice the parent checks `subagent status <id> | awk 'NF{l=$0} END{print l}' | grep -E '^[[:space:]]*QUESTION:'`, answers with `subagent send <id> -- ...` and waits again. A lead with a question for architect has no running children when it does this.

Before ending a turn, every asker runs `subagent questions` and resumes every pending qid: a late `[answer ...]` restarts a finished asker.

Ids: shell variables do not survive between Bash calls, so write ids literally. `subagent tree` lists every instance with uuid and label (your row is marked `*`; your own id is `$ASGARD_CHAT_ID`).

## /shared namespacing

`/shared` is one directory for the whole spawn tree. When several instances of one role run, use `/shared/<role>/<label>/...` (e.g. `/shared/impler/impl-auth/`); the spawner picks unique labels. `$ASGARD_CHAT_ID` (each chat's unique id, set in the sandbox) is an alternative key. Labels must not contain `]`: it breaks parsing of the `[subagent <id> (<label>) ...]` prefix. Cross-role artifacts use topic paths instead: `/shared/design/<topic>/` (design, decisions, research) and `/shared/review/<feature>/r<n>/` (one dir per review round: `commit.patch`, one file per lens, `summary.md`).

## Install

Copy `agents/*` into your `etcd-tree/agents/` and run `just publish` (`cfgctl push`). Then:

- Change `run_dirs` in `architect/config.yaml` to your repo.
- Change the models to ones your runner's catalogs list.
- All agents must be on the same team (`team: dev`), or `subagent spawn` is refused.

## Notes

- Only `architect` is a main agent and has `run_dirs`; subagents work in their parent's checkout.
- architect commits `docs/design/<topic>.md` before spawning lead, edits only the `/shared` copies while lead runs, then syncs and commits the checkout copy at the end. impler stages only its own files.
- Writable `lead` and `impler` take the checkout lock only while their parent waits (`subagent wait`) or is idle-held. A `wait` that returns while a child still runs (message, timeout, `--any`) can leave such a child holding the checkout: the output says so (`checkout_held_by`), and the parent must not modify files until it waits again.
- `review-lead`, `reviewer` and `researcher` use `lock_mode: readonly`; still pass `--readonly` so the UI badge shows it.
- All agents use `session_mode: resume`: each spawn is a new chat anyway, and resume keeps context when a parent `send`s a follow-up or answer.

## Worktrees

This example does not support worktree chats as-is. A read-only child in a worktree run cannot see the owner's unmerged commits through git (see the main README's known limitations), which breaks review. To adapt it, give every agent `worktree.enabled: true` with the same `worktree.root` and `run_dirs`, make `review-lead` writable (no `lock_mode: readonly`, which `worktree` rejects, and drop `--readonly` from lead's review-lead spawn, since the flag alone makes the child read-only), and have it write `git show <hash>` to `/shared/review/<feature>/r<n>/commit.patch` (already the case here) for the reviewers.

## Known limitations

- Writable implers serialize on the checkout lock; there is no per-subagent worktree (see Worktrees).
- reviewer sits at depth 4, the limit.
- architect, lead and review-lead must not end their turn while children run: an idle-held target cannot take a question while a writable descendant holds the checkout, so every ask toward it fails fast. They stay in `subagent wait --timeout 90s` calls within one turn.
- A question cannot be withdrawn. If the asker ends its turn with it pending, the late `[answer ...]` restarts the finished asker (impler, researcher, reviewer) for a stray turn. Askers resume every pending qid before ending, and ignore a stray answer.
- Open questions live in backend memory: a restart loses them, and `ask --resume` then fails with not-found. Re-ask.

## Validation

`pkg/agentspec/examples_test.go` parses every agent with `agentspec.ParseFilesWithCLIs(..., NoCLIValidation())` and checks team consistency, that every `ask --topic` is registered by an ancestor's `route add` (or is `user`), and that no snippet uses a `--timeout` above 90s. Model names are not checked against a live runner.
