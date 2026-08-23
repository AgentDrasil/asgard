# examples

Runnable `package main` programs, one per subdirectory. Each is a standalone
binary — `go run` it from this module:

```bash
export GEMINI_API_KEY=...   # or OPENAI_API_KEY / OPENAI_BASE_URL

go run ./examples/minimal
go run ./examples/custom-tool
go run ./examples/session-resume
```

- **minimal** — the smallest end-to-end agent run: one provider, one model,
  the built-in tool registry, streamed to stdout.
- **custom-tool** — registers an extra Go-func tool (`weather`) alongside the
  built-ins via `tools.Func`.
- **session-resume** — persists a run into a JSONL session and
  resumes it for a second round.

All three share a tiny event-printing loop; see `print.go` in each directory.
