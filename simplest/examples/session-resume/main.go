// session-resume: run round 1, persist it into a JSONL
// session, then resume with a second instruction.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	s "github.com/AgentDrasil/asgard/simplest"
	"github.com/AgentDrasil/asgard/simplest/examples/internal/exampleutil"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	model := exampleutil.GeminiModel()
	p := s.NewGemini(os.Getenv("GEMINI_API_KEY"))
	sys := "You are a helpful coding agent."

	mgr := s.New(s.DefaultBaseDir()) // ~/.simplest
	sf, err := mgr.Create(cwd, nil)
	if err != nil {
		panic(err)
	}
	fmt.Printf("session file: %s\n", sf.Path())

	base := exampleutil.NewRequest(model, p, sys, "Remember the number 42.", nil)

	// Round 1
	var final []s.Message
	for ev := range s.Run(context.Background(), base) {
		if ev.Kind == s.AgentEnd {
			final = ev.Messages
		}
	}
	for _, m := range final {
		if _, err := sf.AppendMessage(m); err != nil {
			panic(err)
		}
	}

	// Round 2: same session, new instruction
	if _, err := sf.AppendMessage(&s.UserMessage{
		Content:   s.TextOnly("What number did I ask you to remember?"),
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
