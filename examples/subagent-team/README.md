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

| From | To | How |
|---|---|---|
| user | architect | chat / ask-user; the only user-facing agent |
| architect | user | alignment (step 1) and true scope changes (ask-user) |
| lead | architect | design / spec / scope question: end turn with last line `QUESTION: <q>`; architect answers with `subagent send` |
| impler | lead | `QUESTION: <q>` (or non-blocking `subagent reply`); lead answers, asking architect if it is a design question |
| researcher | architect | best guess, optional `subagent reply`; `QUESTION:` only if blocked |
| reviewer | none | writes its lens file; no questions |
| review-lead | lead | `summary.md` with `VERDICT:` on line 1 |

`QUESTION:` protocol: a child that needs an answer ends its turn with a LAST line `QUESTION: <q>` (keep it short, details in `/shared`; status keeps only the tail of the output). Its finish wakes the parent's `subagent wait` at once. After every `wait` and every pushed finish notice the parent runs `subagent status <id> | awk 'NF{l=$0} END{print l}' | grep -E '^[[:space:]]*QUESTION:'`, (the last non-blank line; the tail printed by `wait` shows it too), answers with `subagent send <id> -- ...` (`session_mode: resume` keeps the child's context) and waits again. Waiting loops use `--timeout 300s`; a non-blocking `subagent reply` is FYI: it also ends a blocked `wait` early (the result says a message arrived), so read it and wait again. A lead that has a question for architect must have no running children.

Before asking, every agent reads `/shared/design/<topic>/decisions.md`. architect appends every Q&A to it.

Shell variables do not survive between Bash calls, so spawners note each printed id, use it literally in later calls, and append `<label> <id>` to `/shared/<role>/ids.txt` to recover ids.

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

- Subagents default to `ask_user: deny`: a subagent's `ask-user` call fails at once instead of blocking in a child chat the user is not watching. The prompts still tell them to use the `QUESTION:` protocol.
- `subagent reply` reaches only the parent, so questions are relayed hop by hop (impler -> lead -> architect).
- Writable implers serialize on the checkout lock; there is no per-subagent worktree.

## Validation

`pkg/agentspec/examples_test.go` parses every agent with `agentspec.ParseFilesWithCLIs(..., NoCLIValidation())` and checks team consistency. Model names are not checked against a live runner.
