// minimal: smallest end-to-end agent run — provider, model, built-in tools,
// one user message, streamed to stdout.
package main

import (
	"os"

	s "github.com/AgentDrasil/asgard/simplest"
	"github.com/AgentDrasil/asgard/simplest/examples/internal/exampleutil"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	reg := s.DefaultRegistry(cwd) // read bash edit write find grep ls

	exampleutil.RunAndPrint(exampleutil.NewRequest(
		exampleutil.GeminiModel(),
		s.NewGemini(os.Getenv("GEMINI_API_KEY")),
		"You are a helpful coding agent.",
		"List the Go files in this repo (do not modify anything).",
		reg.Tools(),
	))
}
