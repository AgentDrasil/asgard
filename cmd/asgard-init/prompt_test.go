package main

import (
	"bufio"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPromptModel_KeyNavigationAndEditing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		initialVal    string
		defVal        string
		keySequence   []tea.Msg
		expectedPos   int
		expectedVal   string
		expectedSub   bool
		expectedQuit  bool
		expectedErrIs error
	}{
		{
			name:        "arrow keys up and down move cursor to start and end",
			initialVal:  "hello",
			defVal:      "hello",
			keySequence: []tea.Msg{tea.KeyMsg{Type: tea.KeyUp}},
			expectedPos: 0,
			expectedVal: "hello",
		},
		{
			name:        "arrow key down moves cursor to end",
			initialVal:  "hello",
			defVal:      "hello",
			keySequence: []tea.Msg{tea.KeyMsg{Type: tea.KeyUp}, tea.KeyMsg{Type: tea.KeyDown}},
			expectedPos: 5,
			expectedVal: "hello",
		},
		{
			name:        "arrow keys left and right move cursor by character",
			initialVal:  "hello",
			defVal:      "hello",
			keySequence: []tea.Msg{tea.KeyMsg{Type: tea.KeyLeft}, tea.KeyMsg{Type: tea.KeyLeft}},
			expectedPos: 3,
			expectedVal: "hello",
		},
		{
			name:       "arrow key left followed by typing inserts text at cursor",
			initialVal: "abcd",
			defVal:     "abcd",
			keySequence: []tea.Msg{
				tea.KeyMsg{Type: tea.KeyLeft},
				tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")},
			},
			expectedPos: 4,
			expectedVal: "abcXd",
		},
		{
			name:       "enter submits current value",
			initialVal: "custom-value",
			defVal:     "default-val",
			keySequence: []tea.Msg{
				tea.KeyMsg{Type: tea.KeyEnter},
			},
			expectedPos:  12,
			expectedVal:  "custom-value",
			expectedSub:  true,
			expectedQuit: true,
		},
		{
			name:       "enter submits default if value is empty",
			initialVal: "",
			defVal:     "default-val",
			keySequence: []tea.Msg{
				tea.KeyMsg{Type: tea.KeyEnter},
			},
			expectedPos:  0,
			expectedVal:  "default-val",
			expectedSub:  true,
			expectedQuit: true,
		},
		{
			name:       "ctrl+c aborts and sets ErrPromptCancelled",
			initialVal: "foo",
			defVal:     "bar",
			keySequence: []tea.Msg{
				tea.KeyMsg{Type: tea.KeyCtrlC},
			},
			expectedPos:   3,
			expectedVal:   "foo",
			expectedSub:   false,
			expectedQuit:  true,
			expectedErrIs: ErrPromptCancelled,
		},
		{
			name:       "esc aborts and sets ErrPromptCancelled",
			initialVal: "foo",
			defVal:     "bar",
			keySequence: []tea.Msg{
				tea.KeyMsg{Type: tea.KeyEsc},
			},
			expectedPos:   3,
			expectedVal:   "foo",
			expectedSub:   false,
			expectedQuit:  true,
			expectedErrIs: ErrPromptCancelled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := newPromptModel("Test Question", tt.defVal, PromptOptions{})
			if tt.initialVal != tt.defVal {
				m.textInput.SetValue(tt.initialVal)
				m.textInput.CursorEnd()
			}

			// Verify Init
			cmd := m.Init()
			assert.NotNil(t, cmd)

			for _, msg := range tt.keySequence {
				updated, _ := m.Update(msg)
				pm, ok := updated.(promptModel)
				require.True(t, ok)
				m = pm
			}

			assert.Equal(t, tt.expectedPos, m.textInput.Position())
			if tt.expectedSub {
				assert.Equal(t, tt.expectedVal, m.value)
				assert.True(t, m.submitted)
			} else {
				assert.Equal(t, tt.expectedVal, m.textInput.Value())
			}

			if tt.expectedQuit {
				assert.True(t, m.quitting)
			}

			if tt.expectedErrIs != nil {
				assert.ErrorIs(t, m.err, tt.expectedErrIs)
			}
		})
	}
}

func TestPromptModel_SecretMasking(t *testing.T) {
	t.Parallel()

	m := newPromptModel("Secret Key", "initial-secret", PromptOptions{Secret: true})
	assert.True(t, m.secret)
	assert.Equal(t, textinput.EchoPassword, m.textInput.EchoMode)
	assert.Equal(t, '•', m.textInput.EchoCharacter)

	// Active view hides default
	m.textInput.SetValue("typed-something")
	activeView := m.View()
	assert.NotContains(t, activeView, "initial-secret")
	assert.Contains(t, activeView, "••••••••")

	// Submitted view masks value
	subUpdated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	subModel, ok := subUpdated.(promptModel)
	require.True(t, ok)
	require.True(t, subModel.submitted)

	submittedView := subModel.View()
	assert.NotContains(t, submittedView, "typed-something")
	assert.Contains(t, submittedView, "••••••••")
}

func TestPromptModel_WindowSizeAndViews(t *testing.T) {
	t.Parallel()

	m := newPromptModel("Deployment Target", "/tmp/default", PromptOptions{})

	// Test WindowSizeMsg
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	pm, ok := updated.(promptModel)
	require.True(t, ok)
	assert.GreaterOrEqual(t, pm.textInput.Width, 20)

	// Test Active View
	activeView := pm.View()
	assert.Contains(t, activeView, "Deployment Target")
	assert.Contains(t, activeView, "/tmp/default")

	// Test Submitted View
	subUpdated, _ := pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	subModel, ok := subUpdated.(promptModel)
	require.True(t, ok)
	require.True(t, subModel.submitted)
	require.True(t, subModel.quitting)

	submittedView := subModel.View()
	assert.Contains(t, submittedView, "Deployment Target")
	assert.Contains(t, submittedView, "/tmp/default")
	assert.Contains(t, submittedView, "✔")
}

func TestPromptFallback_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		question    string
		def         string
		opts        PromptOptions
		expectedVal string
	}{
		{
			name:        "custom input overrides default",
			input:       "/custom/path\n",
			question:    "Directory",
			def:         "/default/path",
			expectedVal: "/custom/path",
		},
		{
			name:        "empty enter submits default",
			input:       "\n",
			question:    "Directory",
			def:         "/default/path",
			expectedVal: "/default/path",
		},
		{
			name:        "EOF on empty reader returns default",
			input:       "",
			question:    "Port",
			def:         "7000",
			expectedVal: "7000",
		},
		{
			name:        "whitespace input returns default",
			input:       "   \n",
			question:    "Host",
			def:         "http://localhost:7000",
			expectedVal: "http://localhost:7000",
		},
		{
			name:        "secret option masks display default",
			input:       "\n",
			question:    "Secret",
			def:         "super-secret",
			opts:        PromptOptions{Secret: true},
			expectedVal: "super-secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res, err := promptFallback(strings.NewReader(tt.input), tt.question, tt.def, tt.opts)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedVal, res)
		})
	}
}

func TestPrompt_NonTerminalRoutesToFallback(t *testing.T) {
	t.Parallel()

	// In non-interactive test mode (strings.Reader), prompt routes to fallback cleanly
	val, err := prompt(strings.NewReader("my-runner\n"), "Runner ID", "runner-1")
	require.NoError(t, err)
	assert.Equal(t, "my-runner", val)
}

// TestPrompt_SharedStdinReaderRouting verifies that passing the shared
// defaultStdinReader with isInteractiveTerminal=true correctly routes to Bubble Tea.
func TestPrompt_SharedStdinReaderRouting(t *testing.T) {
	origRunTeaPrompt := runTeaPrompt
	origIsInteractive := isInteractiveTerminal
	origDefaultStdinReader := defaultStdinReader
	t.Cleanup(func() {
		runTeaPrompt = origRunTeaPrompt
		isInteractiveTerminal = origIsInteractive
		defaultStdinReader = origDefaultStdinReader
	})

	isInteractiveTerminal = func() bool { return true }
	calledWithOpts := false
	runTeaPrompt = func(q, def string, opts PromptOptions) (string, error) {
		calledWithOpts = opts.Secret
		return "tea-routed-successfully", nil
	}

	// Case 1: passing nil
	val, err := prompt(nil, "Target", "/def")
	require.NoError(t, err)
	assert.Equal(t, "tea-routed-successfully", val)

	// Case 2: passing os.Stdin
	val, err = prompt(os.Stdin, "Target", "/def")
	require.NoError(t, err)
	assert.Equal(t, "tea-routed-successfully", val)

	// Case 3: passing the shared defaultStdinReader created in main.go
	defaultStdinReader = bufio.NewReader(os.Stdin)
	val, err = prompt(defaultStdinReader, "Secret Target", "/def", WithSecret(true))
	require.NoError(t, err)
	assert.Equal(t, "tea-routed-successfully", val)
	assert.True(t, calledWithOpts, "expected WithSecret(true) to propagate to runTeaPrompt")

	// Case 4: passing an external reader (not stdin) must route to fallback, not tea
	val, err = prompt(strings.NewReader("fallback-val\n"), "Other", "/def")
	require.NoError(t, err)
	assert.Equal(t, "fallback-val", val)
}

func TestPrompt_RunTeaPromptMocked(t *testing.T) {
	origRunTeaPrompt := runTeaPrompt
	origIsInteractive := isInteractiveTerminal
	t.Cleanup(func() {
		runTeaPrompt = origRunTeaPrompt
		isInteractiveTerminal = origIsInteractive
	})

	isInteractiveTerminal = func() bool { return true }
	runTeaPrompt = func(q, def string, opts PromptOptions) (string, error) {
		if q == "fail" {
			return "", ErrPromptCancelled
		}
		if q == "tui-error" {
			return "", errors.New("terminal setup failed")
		}
		return "mocked-tea-value", nil
	}

	val, err := prompt(nil, "Deployment directory", "/def")
	require.NoError(t, err)
	assert.Equal(t, "mocked-tea-value", val)

	_, err = prompt(nil, "fail", "/def")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPromptCancelled))

	// If TUI fails with unexpected error, it gracefully falls back to promptFallback
	fallbackVal, err := prompt(strings.NewReader("fallback-from-tui-error\n"), "tui-error", "/def")
	require.NoError(t, err)
	assert.Equal(t, "fallback-from-tui-error", fallbackVal)
}
