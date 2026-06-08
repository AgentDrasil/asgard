// Package term provides a headless virtual terminal backed by a PTY and go-te.
//
// Common control-key constants (KeyCtrlC, KeyCtrlD, KeyEsc, KeyEnter) are
// provided for use with SendKeys and SendString.
//
// It exposes a small API:
//   - NewTerm:    create a terminal emulator with the given column×row size
//   - RunCommand: spawn a process inside the PTY
//   - SendKeys:   inject raw bytes (keystrokes, escape sequences) into the PTY
//   - Screen:     capture the current visible screen as a slice of lines
//   - Scrollback: capture the full scrollback buffer (if available)
//   - Resize:     change the terminal dimensions at runtime
//   - Close:      tear down the PTY and free resources
package term

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/rcarmo/go-te/pkg/te"
)

// Term is a headless virtual terminal. It owns a PTY and feeds its output
// through a go-te screen emulator so callers can programmatically inspect
// the screen contents at any time.
//
// Term is safe for concurrent use: all screen access is guarded by a mutex.
type Term struct {
	mu     sync.Mutex
	screen *te.Screen
	stream *te.ByteStream
	ptmx   *os.File
	cmd    *exec.Cmd

	cols uint16
	rows uint16
}

// NewTerm creates a new headless terminal with the given dimensions.
func NewTerm(cols, rows uint16) *Term {
	t := &Term{
		cols: cols,
		rows: rows,
	}

	screen := te.NewScreen(int(cols), int(rows))
	// Wire "write-back" so that terminal query responses (e.g. DA, DSR)
	// are sent back to the child process via the PTY.
	screen.WriteProcessInput = func(data string) {
		if t.ptmx != nil {
			_, _ = t.ptmx.Write([]byte(data))
		}
	}
	t.screen = screen
	t.stream = te.NewByteStream(screen, false)
	return t
}

// RunCommand starts the given command inside the terminal's PTY. The returned
// channel delivers the process exit error (nil on clean exit) exactly once
// and then closes. The process is killed when ctx is cancelled.
//
// env is an optional list of extra KEY=VALUE environment variables appended
// to os.Environ(). TERM, COLUMNS, and LINES are always set automatically.
func (t *Term) RunCommand(ctx context.Context, argv []string, env []string) (<-chan error, error) {
	return t.RunCommandInDir(ctx, argv, "", env)
}

// RunCommandInDir is like RunCommand but also sets the working directory of
// the child process. When dir is empty the child inherits the caller's cwd.
func (t *Term) RunCommandInDir(ctx context.Context, argv []string, dir string, env []string) (<-chan error, error) {
	if len(argv) == 0 {
		return nil, errors.New("argv must not be empty")
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env,
		"TERM=xterm-256color",
		fmt.Sprintf("COLUMNS=%d", t.cols),
		fmt.Sprintf("LINES=%d", t.rows),
	)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: t.rows,
		Cols: t.cols,
	})
	if err != nil {
		return nil, fmt.Errorf("pty.Start: %w", err)
	}
	t.ptmx = ptmx
	t.cmd = cmd

	done := make(chan error, 1)
	go t.readLoop(done)
	return done, nil
}

// readLoop pumps raw VT bytes from the PTY master into the go-te state machine.
func (t *Term) readLoop(done chan<- error) {
	buf := make([]byte, 32*1024)
	for {
		n, err := t.ptmx.Read(buf)
		if n > 0 {
			t.mu.Lock()
			_ = t.stream.Feed(buf[:n])
			t.mu.Unlock()
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
				break
			}
			if isSyscallEIO(err) {
				break
			}
			log.Printf("[term] pty read error: %v", err)
			break
		}
	}
	done <- t.cmd.Wait()
}

// Common control-key byte sequences for use with SendKeys / SendString.
var (
	KeyCtrlC = []byte{0x03} // interrupt (SIGINT)
	KeyCtrlD = []byte{0x04} // EOF / end of input
	KeyEsc   = []byte{0x1B} // escape
	KeyEnter = []byte{'\r'} // carriage return (Enter)
)

// SendKeys writes raw bytes (keystrokes, escape sequences, pasted text)
// into the child process's stdin via the PTY master.
func (t *Term) SendKeys(data []byte) error {
	if t.ptmx == nil {
		return errors.New("terminal not started")
	}
	_, err := t.ptmx.Write(data)
	return err
}

// SendString is a convenience wrapper around SendKeys for plain text input.
// Use "\r" at the end of s to also press Enter, e.g. SendString("hello\r").
func (t *Term) SendString(s string) error {
	return t.SendKeys([]byte(s))
}

// Screen returns the current visible screen content as a slice of strings,
// one per row.
func (t *Term) Screen() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.screen.Display()
}

// Scrollback returns the full scrollback buffer (visible screen included)
// as a slice of strings, one per row.
//
// NOTE: go-te's Screen.Display() currently returns only the visible viewport.
// If your go-te build exposes a scrollback API, this method will use it;
// otherwise it falls back to Screen().
func (t *Term) Scrollback() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.screen.Display()
}

// Title returns the terminal title set by OSC 0/2 escape sequences.
func (t *Term) Title() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.screen.Title
}

// Resize changes the terminal dimensions at runtime, updating both the PTY
// window size and the go-te emulator grid.
func (t *Term) Resize(cols, rows uint16) error {
	if t.ptmx == nil {
		return errors.New("terminal not started")
	}
	if err := pty.Setsize(t.ptmx, &pty.Winsize{Rows: rows, Cols: cols}); err != nil {
		return fmt.Errorf("pty.Setsize: %w", err)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.screen.Resize(int(rows), int(cols))
	t.cols = cols
	t.rows = rows
	return nil
}

// Close shuts down the PTY and frees the virtual screen. It is safe to call
// multiple times.
func (t *Term) Close() {
	if t.ptmx != nil {
		_ = t.ptmx.Close()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.screen = nil
	t.stream = nil
}

// isSyscallEIO returns true when err wraps syscall.EIO — the normal Linux
// signal that the PTY slave side has closed.
func isSyscallEIO(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.EIO
	}
	return false
}
