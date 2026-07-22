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

func TestCleanExit(t *testing.T) {
	t.Parallel()

	// Create a headless terminal
	termCols := uint16(80)
	termRows := uint16(24)
	vterm := term.NewTerm(termCols, termRows)
	t.Cleanup(func() {
		vterm.Close()
	})

	// Run a command that reads input line and exits when receiving input.
	cmdPath, err := exec.LookPath("bash")
	require.NoError(t, err)

	done, err := vterm.RunCommand(context.Background(), []string{cmdPath, "-c", "read line; exit 0"}, nil)
	require.NoError(t, err)

	// Perform clean exit (/exit + Enter)
	start := time.Now()
	CleanExit(vterm, done)
	duration := time.Since(start)

	assert.Less(t, duration, 4*time.Second)
}
