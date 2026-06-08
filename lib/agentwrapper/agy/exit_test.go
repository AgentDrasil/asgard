package agy

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/lib/term"
)

func TestGratefulShutdown(t *testing.T) {
	t.Parallel()

	// Create a headless terminal
	termCols := uint16(80)
	termRows := uint16(24)
	vterm := term.NewTerm(termCols, termRows)
	t.Cleanup(func() {
		vterm.Close()
	})

	// Run a command that reads from stdin and exits when receiving Ctrl+D twice (or EOF) Let's use "cat".
	cmdPath, err := exec.LookPath("cat")
	require.NoError(t, err)

	done, err := vterm.RunCommand(context.Background(), []string{cmdPath}, nil)
	require.NoError(t, err)

	// Perform grateful shutdown (Ctrl+C, Ctrl+D, Ctrl+D)
	start := time.Now()
	GratefulShutdown(vterm, done)
	duration := time.Since(start)

	// It should exit successfully and within a reasonable timeframe (less than the 5s timeout)
	assert.Less(t, duration, 4*time.Second)
}

func TestCleanExit(t *testing.T) {
	t.Parallel()

	// Create a headless terminal
	termCols := uint16(80)
	termRows := uint16(24)
	vterm := term.NewTerm(termCols, termRows)
	t.Cleanup(func() {
		vterm.Close()
	})

	// Run a command that reads from stdin and exits when receiving Ctrl+D twice (or EOF) Let's use "cat".
	cmdPath, err := exec.LookPath("cat")
	require.NoError(t, err)

	done, err := vterm.RunCommand(context.Background(), []string{cmdPath}, nil)
	require.NoError(t, err)

	// Perform clean exit (Esc, Ctrl+D, Ctrl+D)
	start := time.Now()
	CleanExit(vterm, done)
	duration := time.Since(start)

	// It should exit successfully and within a reasonable timeframe (less than the 5s timeout)
	assert.Less(t, duration, 4*time.Second)
}
