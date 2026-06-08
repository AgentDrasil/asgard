// agent-wrapper demo: runs `top` in a headless PTY, waits for it to render,
// prints a screen snapshot, then sends Esc to exit.
//
// Note on "does the terminal go back to a shell?":
// We launch `top` directly — there is no shell underneath. When top exits,
// the PTY slave closes, readLoop returns, and <-done fires. The Term is done.
// If you want a shell prompt after top, wrap it: RunCommand(["bash","-c","top"]).
package main

import (
	"context"
	"fmt"
	"log"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/AgentDrasil/asgard/lib/term"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// ── Create a 220×50 headless terminal ────────────────────────────────────
	t := term.NewTerm(220, 50)
	defer t.Close()

	// ── Run top ───────────────────────────────────────────────────────────────
	done, err := t.RunCommand(ctx, []string{"top"}, nil)
	if err != nil {
		log.Fatalf("RunCommand: %v", err)
	}
	log.Println("top started, waiting for it to render…")

	// ── Wait for top to render its first full frame ───────────────────────────
	// top redraws every 3 s by default; 1.5 s is enough to see the header.
	time.Sleep(1500 * time.Millisecond)

	// ── Capture and print the screen ──────────────────────────────────────────
	lines := t.Screen()
	fmt.Println("=== scrollback snapshot ===")
	for _, line := range lines {
		// Strip embedded \r/\n that go-te may insert for wrapped content.
		line = strings.NewReplacer("\r\n", "", "\r", "", "\n", "").Replace(line)
		// go-te's virtual grid pads every row to full width with spaces;
		// skip rows that are blank after trimming.
		if strings.TrimSpace(line) != "" {
			fmt.Println(strings.TrimRight(line, " "))
		}
	}
	fmt.Println("=== end of snapshot ===")

	// ── Send Esc to quit top ──────────────────────────────────────────────────
	// top does not treat Esc as a quit key in all builds; send 'q' as well.
	// Remove the 'q' line if you want to test Esc-only behaviour.
	log.Println("sending Esc…")
	if err := t.SendKeys(term.KeyEsc); err != nil {
		log.Printf("SendKeys Esc: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	log.Println("sending q (standard quit key for top)…")
	if err := t.SendString("q"); err != nil {
		log.Printf("SendString q: %v", err)
	}

	// ── Wait for top to exit ──────────────────────────────────────────────────
	select {
	case exitErr := <-done:
		if exitErr != nil {
			log.Printf("top exited with error: %v", exitErr)
		} else {
			log.Println("top exited cleanly — PTY closed, no shell underneath")
		}
	case <-ctx.Done():
		log.Println("interrupted")
	}
}
