package term

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewTerm verifies that NewTerm returns a non-nil terminal and that
// Screen() returns the expected number of rows before any command is run.
func TestNewTerm(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cols uint16
		rows uint16
	}{
		{"standard size", 80, 24},
		{"wide terminal", 220, 50},
		{"small terminal", 40, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tr := NewTerm(tt.cols, tt.rows)
			require.NotNil(t, tr)
			t.Cleanup(tr.Close)

			lines := tr.Screen()
			assert.Equal(t, int(tt.rows), len(lines), "Screen() should return rows lines before any command")
		})
	}
}

// TestRunCommand_EmptyArgv verifies that RunCommand returns an error when
// called with an empty argv slice.
func TestRunCommand_EmptyArgv(t *testing.T) {
	t.Parallel()

	tr := NewTerm(80, 24)
	t.Cleanup(tr.Close)

	_, err := tr.RunCommand(context.Background(), []string{}, nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "argv must not be empty")
}

// TestRunCommand_InvalidCommand verifies that RunCommand returns an error
// when the command binary does not exist.
func TestRunCommand_InvalidCommand(t *testing.T) {
	t.Parallel()

	tr := NewTerm(80, 24)
	t.Cleanup(tr.Close)

	_, err := tr.RunCommand(context.Background(), []string{"__no_such_binary__"}, nil)
	require.Error(t, err)
}

// TestRunCommand_Top is an integration test that runs `top` in the headless
// terminal and verifies that the screen contains the expected CPU header line.
func TestRunCommand_Top(t *testing.T) {
	t.Parallel()

	tr := NewTerm(220, 50)
	t.Cleanup(tr.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	done, err := tr.RunCommand(ctx, []string{"top"}, nil)
	require.NoError(t, err)

	// Wait for top to render its first frame.
	time.Sleep(1500 * time.Millisecond)

	lines := tr.Screen()
	found := false
	for _, line := range lines {
		if strings.Contains(line, "%Cpu(s)") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected to find %%Cpu(s) in top output, got: %v", lines)

	// Quit top gracefully.
	_ = tr.SendString("q")

	select {
	case <-done:
	case <-ctx.Done():
		t.Log("context timed out waiting for top to exit")
	}
}

// TestSendKeys_BeforeStart verifies that SendKeys returns an error when the
// terminal has not been started (no RunCommand call yet).
func TestSendKeys_BeforeStart(t *testing.T) {
	t.Parallel()

	tr := NewTerm(80, 24)
	t.Cleanup(tr.Close)

	err := tr.SendKeys(KeyEnter)
	require.Error(t, err)
	assert.ErrorContains(t, err, "terminal not started")
}

// TestSendString_BeforeStart verifies that SendString returns an error when
// the terminal has not been started.
func TestSendString_BeforeStart(t *testing.T) {
	t.Parallel()

	tr := NewTerm(80, 24)
	t.Cleanup(tr.Close)

	err := tr.SendString("hello")
	require.Error(t, err)
	assert.ErrorContains(t, err, "terminal not started")
}

// TestResize_BeforeStart verifies that Resize returns an error when no
// command has been started and the PTY is not open.
func TestResize_BeforeStart(t *testing.T) {
	t.Parallel()

	tr := NewTerm(80, 24)
	t.Cleanup(tr.Close)

	err := tr.Resize(120, 40)
	require.Error(t, err)
	assert.ErrorContains(t, err, "terminal not started")
}

// TestResize_WithRunningCommand verifies that Resize succeeds while a command
// is running inside the terminal.
func TestResize_WithRunningCommand(t *testing.T) {
	t.Parallel()

	tr := NewTerm(80, 24)
	t.Cleanup(tr.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	_, err := tr.RunCommand(ctx, []string{"cat"}, nil)
	require.NoError(t, err)

	err = tr.Resize(120, 40)
	assert.NoError(t, err)

	// Terminate cat by sending EOF.
	_ = tr.SendKeys(KeyCtrlD)
}

// TestScreen_ReturnsLines verifies that Screen() always returns a non-nil
// slice, even on a freshly created terminal.
func TestScreen_ReturnsLines(t *testing.T) {
	t.Parallel()

	tr := NewTerm(80, 24)
	t.Cleanup(tr.Close)

	lines := tr.Screen()
	require.NotNil(t, lines)
	assert.NotEmpty(t, lines)
}

// TestClose_Idempotent verifies that calling Close multiple times does not
// panic or return an error.
func TestClose_Idempotent(t *testing.T) {
	t.Parallel()

	tr := NewTerm(80, 24)

	assert.NotPanics(t, func() {
		tr.Close()
		tr.Close()
	})
}

// TestRunCommand_Echo verifies that a short-lived command exits cleanly and
// the done channel is closed with a nil error.
func TestRunCommand_Echo(t *testing.T) {
	t.Parallel()

	tr := NewTerm(80, 24)
	t.Cleanup(tr.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	done, err := tr.RunCommand(ctx, []string{"echo", "hello term"}, nil)
	require.NoError(t, err)

	select {
	case exitErr := <-done:
		assert.NoError(t, exitErr, "echo should exit cleanly")
	case <-ctx.Done():
		t.Fatal("timed out waiting for echo to exit")
	}
}

// TestConcurrentTermInstances verifies that two independent Term instances can
// run top simultaneously without interfering with each other. Both must render
// the "%Cpu(s)" header within the timeout.
func TestConcurrentTermInstances(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	for i := 0; i < 2; i++ {
		i := i
		t.Run(fmt.Sprintf("instance_%d", i), func(t *testing.T) {
			t.Parallel()

			tr := NewTerm(220, 50)
			t.Cleanup(tr.Close)

			done, err := tr.RunCommand(ctx, []string{"top"}, nil)
			require.NoError(t, err)

			time.Sleep(1500 * time.Millisecond)

			found := false
			for _, line := range tr.Screen() {
				if strings.Contains(line, "%Cpu(s)") {
					found = true
					break
				}
			}
			assert.True(t, found, "instance %d: expected %%Cpu(s) in top output", i)

			_ = tr.SendString("q")
			select {
			case <-done:
			case <-ctx.Done():
			}
		})
	}
}
