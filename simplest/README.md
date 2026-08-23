# simplest

A Go library that reimplements the core of [pi](https://pi.dev/) (the coding agent): one
function to run an agent request, pi-compatible sessions with resume, the
seven built-in tools, OpenAI-compatible and Gemini providers, prompt assembly,
and in-process Go plugins.

Library only — no CLI or TUI. Module: `github.com/AgentDrasil/asgard/simplest`.

Runnable multi-`main` examples for manual verification live in
[`examples/`](examples/) — see its README for `go run` commands.

```bash
go get github.com/AgentDrasil/simplest
```

## Packages

| Package    | Purpose |
|------------|---------|
| `types`    | Shared data model: messages, content blocks, events, `Provider`, `Model`, `Context` |
| `tools`    | 7 built-ins (`read`, `bash`, `edit`, `write`, `find`, `grep`, `ls`), registry, JSON-schema validation, Go-func tool adapter |
| `session`  | pi-compatible JSONL v3 session store (tree entries, resume, compaction) |
| `provider` | `openai-completions` and `google-generative-ai` SSE streaming clients |
| `prompt`   | System-prompt assembly + AGENTS.md / CLAUDE.md context-file loading |
| `agent`    | The run loop: `Run(ctx, Request) <-chan AgentEvent` |

A complete agent is four pieces wired together:

```
agent.Run(ctx, agent.Request{
    Provider:     provider.NewOpenAICompat(apiKey),   // who talks to the model
    Model:        &model,                             // which model/endpoint
    SystemPrompt: sysPrompt,                          // from prompt.BuildSystemPrompt
    Tools:        reg.Tools(),                        // from tools.DefaultRegistry
}) -> <-chan types.AgentEvent                       // stream of lifecycle events
```

## 1. Minimal end-to-end example

```go
package main

import (
	"context"
	"fmt"

	"github.com/AgentDrasil/simplest/agent"
	"github.com/AgentDrasil/simplest/provider"
	"github.com/AgentDrasil/simplest/tools"
	"github.com/AgentDrasil/simplest/types"
)

func main() {
	model := &types.Model{
		ID:            "gemini-3.7-flash",
		Name:          "Gemini 3.7 Flash",
		API:           types.APIGoogleGenerativeAI, // or types.APIOpenAICompat
		Provider:      "google",
		BaseURL:       "", // "" = official endpoint; must include version path if custom
		ContextWindow: 1_000_000,
		MaxTokens:     8192,
		Input:         []string{"text", "image"},
		Cost:          types.ModelCostRates{Input: 0.3, Output: 2.5},
	}

	reg := tools.DefaultRegistry("/path/to/project") // read/bash/edit/write/find/grep/ls

	events := agent.Run(context.Background(), agent.Request{
		SystemPrompt: "You are a helpful coding agent.",
		Messages: []types.Message{
			&types.UserMessage{Content: types.TextOnly("List the Go files in this repo"), Timestamp: now()},
		},
		Model:    model,
		Provider: provider.NewGemini(os.Getenv("GEMINI_API_KEY")),
		Tools:    reg.Tools(),
	})

	for ev := range events {
		if e, ok := ev.(types.AgentEvent); ok {
			switch e.Kind {
			case types.MessageUpdate:
				if e.Message != nil {
					fmt.Print(lastText(e.Message)) // stream tokens as they arrive
				}
			case types.MessageEnd:
				// Errors surface as StopReason==error on the assistant message,
				// not as provider-level StreamErrorEvents.
				if e.Message != nil && e.Message.StopReason == types.StopError {
					fmt.Printf("\n[error] %s\n", e.Message.ErrorMessage)
				}
			case types.AgentEnd:
				fmt.Println("\n--- done ---")
			}
		}
	}
}
```

Events arrive on one buffered channel (64). It always terminates with exactly
one `types.DoneEvent` (assistant-level success/failure) and closes after
`agent_end`; cancellation still yields `agent_end`. See `types/event.go` for
the full event union:

- `AgentEvent` — `agent_start`, `turn_start`, `message_start/update/end`
  (carries the in-flight assistant message plus the raw provider event),
  `tool_execution_start/update/end`, `turn_end`, `agent_end` (final messages)
- `DoneEvent` / `StreamErrorEvent` — provider-level per-stream outcomes (never on the agent event channel; agent errors appear as `message_end` with `StopReason == error`)

## 2. Models & providers

Configure models programmatically; there are no config files or env lookups.

```go
// OpenAI-compatible endpoint (works with any chat-completions server)
p := provider.NewOpenAICompat(os.Getenv("OPENAI_API_KEY"))
model := &types.Model{
	ID: "gpt-4.1", API: types.APIOpenAICompat, Provider: "openai",
	BaseURL: "https://api.openai.com/v1", // client appends /chat/completions
	Cost:    types.ModelCostRates{Input: 2, Output: 8}, // $/MTok
	Input:   []string{"text", "image"},
}

// Per-call key override
opts := &types.StreamOptions{APIKey: "sk-...", ThinkingLevel: types.ThinkingMedium}

// Direct provider use without the agent loop
ctx := &types.Context{SystemPrompt: "...", Messages: msgs}
for ev := range p.Stream(context.Background(), model, ctx, opts) { ... }
```

Both providers parse SSE streams into the same protocol: `start`, block
deltas (`text_*`, `thinking_*`, `toolcall_*`), then `done` or `error`.
Usage and cost accounting is filled automatically from the model's rates.

## 3. Tools

### Built-ins

```go
reg := tools.DefaultRegistry(cwd)      // read bash edit write find grep ls
names := reg.Names()                   // ["read","bash","edit","write","find","grep","ls"]
```

Faithful ports of pi's tools: fuzzy edit matching, 2000-line/50KB truncation,
gitignore-aware find/grep, unified diff rendering.

### Registering your own tool (Go func)

Any function can become a tool via `tools.Func`:

```go
weather := &tools.Func{
	ToolName:        "weather",
	ToolDescription: "Get current weather for a city",
	Snippet:         "query live weather", // shown in the system prompt
	Fn: func(ctx context.Context, callID string, args json.RawMessage, onUpdate types.UpdateFunc) (*types.ToolResult, error) {
		var in struct{ City string `json:"city"` }
		json.Unmarshal(args, &in)
		return &types.ToolResult{
			Content: []types.AssistantContent{
				types.TextContent{Type: "text", Text: "sunny, 22C in " + in.City},
			},
			Details: map[string]any{"source": "example"},
		}, nil
	},
}
reg.Register(weather) // replaces any same-named tool
```

Set `Mode: types.ExecutionSequential` to force sequential execution when
batched with other calls; otherwise parallel batches run concurrently.
Stream partial results by calling `onUpdate`.

### Argument validation

Wire schema validation into the loop:

```go
req.Validate = func(args, parameters json.RawMessage) error {
	_, err := tools.ValidateAgainstSchema(args, parameters)
	return err
}
```

## 4. Sessions

Sessions are append-only trees stored as pi-compatible JSONL v3 under
`<baseDir>/sessions/<encoded-cwd>/<timestamp>_<uuid>.jsonl`. Files written
here load in pi and vice versa.

```go
mgr := session.New(session.DefaultBaseDir()) // ~/.simplest

// New session for this project
sf, err := mgr.Create(cwd, nil)

// Resume the most recent session (creates one if none exists)
sf, err := mgr.ContinueRecent(cwd)

// Open an exact file
sf, err = mgr.Open("~/.simplest/sessions/--home-me-proj--/2026-....jsonl")

// In-memory only
tmp := mgr.InMemory(cwd)
```

Append entries; ids, parent links, and timestamps are filled in:

```go
entryID, _ := sf.AppendMessage(&types.UserMessage{...})
_, _ = sf.AppendModelChange("openai", "gpt-4.1")
_, _ = sf.AppendThinkingLevelChange(types.ThinkingHigh)

compID, _ := sf.AppendCompaction(summary, firstKeptEntryID, tokensBefore, nil, false)

// Branching rewrites nothing; it moves the leaf pointer
sf.Branch(earlierEntryID)
u, _ := sf.AppendMessage(&types.UserMessage{...}) // forks history here
```

Resolve what to send to the LLM (follows the leaf path, applies compaction):

```go
cx, _ := sf.BuildContext("")          // Context{Messages, ThinkingLevel, Model}
_ = cx.Messages                        // []types.Message ready for providers
```

Files are flushed lazily on the first assistant message (matching pi); call
`sf.Flush()` to write early. `session.FindMostRecent(dir, cwd)` and
`session.List(dir)` support discovery.

## 5. Prompts

```go
contextFiles := prompt.LoadProjectContextFiles(cwd, "~/.simplest")
// collects AGENTS.override.md > AGENTS.md > AGENTS.MD > CLAUDE.md > CLAUDE.MD
// from ~/.simplest first, then walking cwd up to root (root-most first)

sysPrompt := prompt.BuildSystemPrompt(prompt.Options{
	CWD:                cwd,
	SelectedTools:      reg.Names(),
	ToolSnippets:       map[string]string{"read": "read file contents", ...},
	PromptGuidelines:   []string{"Always run tests after edits"},
	AppendSystemPrompt: extraRules,
	ContextFiles:       contextFiles,
})
```

Only tools with a `Snippet` appear in "Available tools"; guidelines filter by
active tools (e.g., the bash-only guideline drops when grep/find/ls exist).
Custom tools contribute snippets via `tools.Func.Snippet`.

## 6. The agent loop: hooks, queues, compaction

```go
events := agent.Run(ctx, agent.Request{
	// ... as above ...

	// Before-hook: inspect/block every tool call (validated args provided)
	BeforeToolCall: func(in agent.BeforeToolCallInput) *agent.BeforeToolCallDecision {
		if in.ToolCall.Name == "bash" {
			return &agent.BeforeToolCallDecision{Block: true, Reason: "bash disabled"}
		}
		return nil
	},

	// After-hook: replace fields of the result
	AfterToolCall: func(in agent.AfterToolCallInput) *agent.AfterToolCallOverride {
		return &agent.AfterToolCallOverride{Details: auditLog(in.Result)}
	},

	// Steering: user messages injected between turns while running.
	// The loop POLLS these funcs (at run start and after each turn). The
	// thread-safe agent.Queue is the default implementation:
	GetSteeringMessages: steer.Poll,

	// Follow-ups: queued prompts that restart the loop when it is about to
	// stop. Checked once at the stopping point; if empty, the run ends and
	// later sends are NOT consumed.
	GetFollowUpMessages: followUp.Poll,

	// End the run programmatically after any turn
	ShouldStopAfterTurn: func(s agent.TurnSummary) bool { return s.done },

	// Opt-in context compaction (chars/4 estimate vs context window)
	AutoCompact: &agent.AutoCompactConfig{
		ThresholdFrac: 0.8,
		Summarize: func(ctx context.Context, msgs []types.Message) (string, error) {
			return summarize(ctx, model, msgs) // your own LLM call
		},
	},
})
```

Loop shape per turn: `maybeCompact` → inject pending steering → provider
stream forwarded as `message_*` events → on `toolUse`, execute the batch
(parallel unless a tool declares sequential) with `tool_execution_*` events →
append tool results → `turn_end` → drain steering → repeat. With no pending
work the follow-up queue is checked before `agent_end` closes the channel.
A `length` stop fails all of that message's tool calls (arguments may be
truncated) so the model can re-issue them.

### Sending messages: steer vs follow-up vs new Run

The queue funcs are polled from the loop goroutine. Use `agent.Queue`, which
is safe for concurrent push/poll:

```go
steer := agent.NewQueue()
followUp := agent.NewQueue()

// Mid-run interjection (picked up after the current turn ends):
steer.Push(&types.UserMessage{Content: types.TextOnly("use dry-run"), Timestamp: ...})
```

**Ownership rule:** a message belongs to the loop once pushed. Do not mutate
it afterwards — the loop serializes it to the provider and persists it into
the session concurrently, so mutating a pushed `*UserMessage` is a data race.
If you need to change what you want to send, build a new message and push
that instead. A buffered channel with a non-blocking drain also satisfies the
contract; whatever you use must be safe for concurrent write during polling.

Timing rules:

| Goal | Mechanism | Consumed when |
|------|-----------|---------------|
| Interrupt/redirect while a task is running | steering queue | at run start and after each turn; triggers another LLM call even without tool calls |
| Immediately continue with a next step when the loop would stop | follow-up queue | once, at the stopping point (after steering is drained and no tool calls remain) |
| The run has already ended (`agent_end` seen / channel closed) | start a new `Run` with session context | n/a |

Messages sent to a follow-up queue after the run finished are never consumed.

### Continuing after the run ends

Start a fresh `Run` with the accumulated history. With the session package
this doubles as resume:

```go
// Round 1
sf, _ := mgr.Create(cwd, nil)
// Production code should check errors returned by AppendMessage:
_, _ = sf.AppendMessage(&types.UserMessage{Content: types.TextOnly("task A"), Timestamp: now()})

var final []types.Message
for ev := range agent.Run(ctx, req1) {
	if e, ok := ev.(types.AgentEvent); ok && e.Kind == types.AgentEnd {
		final = e.Messages // assistant + toolResult messages produced by this run
	}
}
for _, m := range final { // persist round-1 output into the session tree
	_, _ = sf.AppendMessage(m)
}

// Round 2: same conversation, new instruction
_, _ = sf.AppendMessage(&types.UserMessage{Content: types.TextOnly("task B"), Timestamp: now()})
cx, _ := sf.BuildContext("") // full leaf path incl. compaction handling

req2 := req1
req2.Messages = cx.Messages
for ev := range agent.Run(ctx, req2) { ... }
```

`Request.Messages` is an immutable starting point, so "continue working" is
always: persist prior output → append a new user message → call `Run` again
with the full context.

## Deviations from pi

Documented, intentional:

- `grep`/`find` use a built-in gitignore-aware walker instead of shelling out to `rg`/`fd`
- Session files are written as v3 only (no v1/v2 migration); worktree shadowing for context files not ported
- Provider compat matrix reduced to the vanilla protocols (no provider-specific thinking formats)
- Skills formatting omitted; docs paths in the base prompt default to repo-relative names
- `before_provider_request` hook (pi extensions API) not ported; only Before/AfterToolCall hooks exist
- read tool returns images at original resolution (no 2000x2000 downscale like pi's `processImage`)
- Base prompt drops pi's `examples/extensions/` docs reference; pi self-referential passages kept verbatim
- Session files have no cross-process file lock: concurrent appends to one file can interleave/corrupt

Run tests with:

```bash
go vet ./... && go test ./...
```
