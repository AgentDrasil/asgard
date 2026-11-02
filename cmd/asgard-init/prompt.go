package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

var (
	ErrPromptCancelled = errors.New("prompt cancelled by user")

	promptLabelStyle = lipgloss.NewStyle().Bold(true)
	promptDefStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))            // Yellow
	promptCheckStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("2")) // Green
	promptValueStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))            // Cyan
)

// defaultStdinReader holds a shared *bufio.Reader over os.Stdin to prevent
// buffering desync across fallback prompts.
var defaultStdinReader *bufio.Reader

// isInteractiveTerminal checks whether stdin and stdout are interactive terminals.
var isInteractiveTerminal = func() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
}

// isStdinReader checks if r is reading from standard input.
func isStdinReader(r io.Reader) bool {
	if r == nil || r == os.Stdin {
		return true
	}
	if br, ok := r.(*bufio.Reader); ok && defaultStdinReader != nil && br == defaultStdinReader {
		return true
	}
	return false
}

// PromptOptions configures prompt behavior.
type PromptOptions struct {
	Secret bool
}

// PromptOption sets a prompt option.
type PromptOption func(*PromptOptions)

// WithSecret masks input and redacted summary output (e.g. for credentials/secrets).
func WithSecret(secret bool) PromptOption {
	return func(o *PromptOptions) {
		o.Secret = secret
	}
}

// promptModel is the Bubble Tea model powering interactive text inputs.
type promptModel struct {
	textInput textinput.Model
	question  string
	def       string
	secret    bool
	submitted bool
	quitting  bool
	value     string
	err       error
}

func newPromptModel(question, def string, opts PromptOptions) promptModel {
	ti := textinput.New()
	ti.Focus()
	ti.Prompt = "" // Question label is rendered separately in View()
	ti.CharLimit = 1024
	ti.Width = 60

	if opts.Secret {
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '•'
	}

	if def != "" {
		ti.SetValue(def)
		ti.CursorEnd()
	} else {
		ti.Placeholder = "optional"
	}

	return promptModel{
		textInput: ti,
		question:  question,
		def:       def,
		secret:    opts.Secret,
	}
}

func (m promptModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m promptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEnter:
			val := strings.TrimSpace(m.textInput.Value())
			if val == "" {
				val = m.def
			}
			m.value = val
			m.submitted = true
			m.quitting = true
			return m, tea.Quit

		case tea.KeyCtrlC, tea.KeyEsc:
			m.err = ErrPromptCancelled
			m.quitting = true
			return m, tea.Quit

		case tea.KeyUp:
			// Move cursor to start of line on Up arrow
			m.textInput.CursorStart()
			return m, nil

		case tea.KeyDown:
			// Move cursor to end of line on Down arrow
			m.textInput.CursorEnd()
			return m, nil
		}

	case tea.WindowSizeMsg:
		w := msg.Width - lipgloss.Width(m.question) - 15
		if w < 20 {
			w = 20
		}
		m.textInput.Width = w
	}

	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m promptModel) View() string {
	if m.quitting {
		if m.submitted {
			val := m.value
			if m.secret && val != "" {
				val = "••••••••"
			} else if val == "" {
				val = "(empty)"
			}
			return fmt.Sprintf("%s %s: %s\n", promptCheckStyle.Render("✔"), promptLabelStyle.Render(m.question), promptValueStyle.Render(val))
		}
		return "\n"
	}

	var sb strings.Builder
	sb.WriteString(promptLabelStyle.Render(m.question))
	if m.def != "" && m.textInput.Value() != m.def {
		dispDef := m.def
		if m.secret {
			dispDef = "••••••••"
		}
		fmt.Fprintf(&sb, " %s", promptDefStyle.Render(fmt.Sprintf("[%s]", dispDef)))
	}
	sb.WriteString(": ")
	sb.WriteString(m.textInput.View())
	sb.WriteString("\n")
	return sb.String()
}

// runTeaPrompt executes a Bubble Tea program for single question input.
var runTeaPrompt = func(question, def string, opts PromptOptions) (string, error) {
	p := tea.NewProgram(newPromptModel(question, def, opts))
	finalModel, err := p.Run()
	if err != nil {
		if errors.Is(err, tea.ErrInterrupted) {
			return "", ErrPromptCancelled
		}
		return "", err
	}
	m, ok := finalModel.(promptModel)
	if !ok {
		return "", fmt.Errorf("unexpected model type %T", finalModel)
	}
	if m.err != nil {
		return "", m.err
	}
	if !m.submitted {
		return "", ErrPromptCancelled
	}
	return m.value, nil
}

// prompt prompts the user for configuration. If stdin/stdout are an interactive
// terminal and r is nil, os.Stdin, or the shared defaultStdinReader, it uses
// Charm's Bubble Tea with full cursor arrow key navigation (left, right, up, down).
// Otherwise it falls back to standard line reading.
func prompt(r io.Reader, question, def string, opts ...PromptOption) (string, error) {
	var opt PromptOptions
	for _, fn := range opts {
		fn(&opt)
	}

	if isStdinReader(r) && isInteractiveTerminal() {
		val, err := runTeaPrompt(question, def, opt)
		if err == nil {
			return val, nil
		}
		if errors.Is(err, ErrPromptCancelled) {
			return "", err
		}
		// Fall back gracefully to standard reader if TUI program fails to start
	}
	return promptFallback(r, question, def, opt)
}

func promptFallback(r io.Reader, question, def string, opt PromptOptions) (string, error) {
	dispDef := def
	if opt.Secret && dispDef != "" {
		dispDef = "••••••••"
	}

	if dispDef != "" {
		fmt.Printf("%s%s%s [%s%s%s]: ", colorBold, question, colorReset, colorYellow, dispDef, colorReset)
	} else {
		fmt.Printf("%s%s%s: ", colorBold, question, colorReset)
	}

	var reader *bufio.Reader
	if br, ok := r.(*bufio.Reader); ok {
		reader = br
	} else if r == nil || r == os.Stdin {
		if defaultStdinReader == nil {
			defaultStdinReader = bufio.NewReader(os.Stdin)
		}
		reader = defaultStdinReader
	} else {
		reader = bufio.NewReader(r)
	}

	line, err := reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && len(line) > 0 {
			val := strings.TrimSpace(line)
			if val == "" {
				return def, nil
			}
			return val, nil
		}
		return def, nil
	}
	val := strings.TrimSpace(line)
	if val == "" {
		return def, nil
	}
	return val, nil
}
