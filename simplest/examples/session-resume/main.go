// session-resume: run round 1, persist it into a pi-compatible JSONL
// session, then resume with a second instruction.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/AgentDrasil/asgard/simplest/agent"
	"github.com/AgentDrasil/asgard/simplest/examples/internal/exampleutil"
	"github.com/AgentDrasil/asgard/simplest/provider"
	"github.com/AgentDrasil/asgard/simplest/session"
	"github.com/AgentDrasil/asgard/simplest/types"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	model := exampleutil.GeminiModel()
	p := provider.NewGemini(os.Getenv("GEMINI_API_KEY"))
	sys := "You are a helpful coding agent."

	mgr := session.New(session.DefaultBaseDir()) // ~/.simplest
	sf, err := mgr.Create(cwd, nil)
	if err != nil {
		panic(err)
	}
	fmt.Printf("session file: %s\n", sf.Path())

	base := exampleutil.NewRequest(model, p, sys, "Remember the number 42.", nil)

	// Round 1
	var final []types.Message
	for ev := range agent.Run(context.Background(), base) {
		if ev.Kind == types.AgentEnd {
			final = ev.Messages
		}
	}
	for _, m := range final {
		if _, err := sf.AppendMessage(m); err != nil {
			panic(err)
		}
	}

	// Round 2: same session, new instruction
	if _, err := sf.AppendMessage(&types.UserMessage{
		Content:   types.TextOnly("What number did I ask you to remember?"),
		Timestamp: time.Now().UnixMilli(),
	}); err != nil {
		panic(err)
	}
	cx, err := sf.BuildContext("")
	if err != nil {
		panic(err)
	}
	req2 := base
	req2.Messages = cx.Messages
	exampleutil.RunAndPrint(req2)
}
