package tools

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestBashToolEchoOutput(t *testing.T) {
	dir := t.TempDir()
	res := mustExec(t, NewBashTool(dir), `{"command":"echo hello_bash"}`)
	if got := textOf(t, res); !strings.Contains(got, "hello_bash") {
		t.Errorf("output = %q, want it to contain hello_bash", got)
	}
}

func TestBashToolStderrCombinedAndExitCode(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name    string
		args    string
		wantSub []string
	}{
		{
			name:    "non-zero exit code",
			args:    `{"command":"echo oops >&2; exit 3"}`,
			wantSub: []string{"oops", "Command exited with code 3"},
		},
		{
			name:    "timeout",
			args:    `{"command":"sleep 30","timeout":0.5}`,
			wantSub: []string{"Command timed out after 0.5 seconds"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := execTool(t, NewBashTool(dir), tt.args)
			if err != nil {
				t.Fatalf("unexpected execution error: %v", err)
			}
			if !result.IsError {
				t.Fatal("expected IsError result")
			}
			text := fmt.Sprintf("%v", result.Content[0])
			for _, sub := range tt.wantSub {
				if !strings.Contains(text, sub) {
					t.Errorf("content %q missing %q", text, sub)
				}
			}
		})
	}
}

func TestBashToolTailTruncationSavesFullOutput(t *testing.T) {
	dir := t.TempDir()
	res := mustExec(t, NewBashTool(dir), `{"command":"seq 1 3000"}`)
	out := textOf(t, res)

	if !strings.Contains(out, "[Showing lines") || !strings.Contains(out, "Full output:") {
		t.Errorf("truncated output missing footer; tail: %q", out[maxInt(0, len(out)-150):])
	}

	details, ok := res.Details.(*BashToolDetails)
	if !ok {
		t.Fatalf("Details type = %T, want *BashToolDetails", res.Details)
	}
	if details.Truncation == nil || !details.Truncation.Truncated {
		t.Error("expected truncation metadata in details")
	} else if details.Truncation.OutputLines != DefaultMaxLines {
		t.Errorf("OutputLines = %d, want %d", details.Truncation.OutputLines, DefaultMaxLines)
	}
	if details.FullOutputPath == "" {
		t.Fatal("FullOutputPath should be set when output is truncated")
	}
	full, err := os.ReadFile(details.FullOutputPath)
	if err != nil {
		t.Fatalf("full output file unreadable: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(full), "\n"), "\n")
	if len(lines) != 3000 || lines[0] != "1" || lines[2999] != "3000" {
		t.Errorf("full output wrong: %d lines, first=%q last=%q", len(lines), lines[0], lines[len(lines)-1])
	}
	_ = os.Remove(details.FullOutputPath)

	// The returned text keeps the tail of the output followed by the footer.
	if !strings.Contains(out, "\n2999\n3000\n\n[Showing lines ") || !strings.HasSuffix(out, "]") {
		t.Errorf("returned output should keep tail lines and end with the footer, got tail %q", out[maxInt(0, len(out)-80):])
	}
}

func TestBashToolLargeOutputStreamedToSpillFile(t *testing.T) {
	dir := t.TempDir()
	res := mustExec(t, NewBashTool(dir), `{"command":"seq 1 100000"}`)
	out := textOf(t, res)

	if !strings.Contains(out, "\n\n[Showing lines ") {
		t.Errorf("output missing truncation footer; tail: %q", out[maxInt(0, len(out)-100):])
	}

	details, ok := res.Details.(*BashToolDetails)
	if !ok || details.Truncation == nil || !details.Truncation.Truncated {
		t.Fatalf("expected truncated *BashToolDetails, got %T %+v", res.Details, res.Details)
	}
	if details.Truncation.TotalLines != 100000 {
		t.Errorf("TotalLines = %d, want 100000", details.Truncation.TotalLines)
	}
	full, err := os.ReadFile(details.FullOutputPath)
	if err != nil {
		t.Fatalf("full output file unreadable: %v", err)
	}
	// "1\n".."9\n".."100000\n": sum of line digits + newlines = 588895.
	if len(full) != 588895 {
		t.Errorf("full output = %d bytes, want %d", len(full), 588895)
	}
	if !strings.Contains(out, "99999\n100000\n\n[Showing lines 98001-100000 of 100000.") {
		t.Errorf("returned output should keep tail lines with global totals, got tail %q", out[maxInt(0, len(out)-120):])
	}
	_ = os.Remove(details.FullOutputPath)
}

func TestBashToolWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "marker.txt", "x")
	res := mustExec(t, NewBashTool(dir), `{"command":"ls marker.txt"}`)
	if got := textOf(t, res); !strings.Contains(got, "marker.txt") {
		t.Errorf("output = %q, want marker.txt listed", got)
	}

	_, err := execTool(t, NewBashTool(dir+"/does-not-exist"), `{"command":"true"}`)
	if err == nil || !strings.Contains(err.Error(), "working directory does not exist") {
		t.Errorf("err = %v, want missing-cwd error", err)
	}
}

func TestBashToolSanitizesANSIAndBinaryOutput(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name    string
		command string
		want    string
		wantSub []string
	}{
		{
			name:    "ANSI colors and CR dropped",
			command: `printf '\033[31mred\033[0m\r\n'`,
			want:    "red\n",
		},
		{
			name:    "binary control bytes dropped",
			command: `printf 'a\0b\001c'`,
			want:    "abc",
		},
		{
			name:    "OSC title stripped",
			command: `printf '\033]0;title\007hello\n'`,
			want:    "hello\n",
		},
		{
			name:    "only ANSI codes means no output",
			command: `printf '\033[31m\033[0m'`,
			want:    "(no output)",
		},
		{
			name:    "progress CR updates collapse",
			command: `printf '1%%\r2%%\r3%%\n'`,
			wantSub: []string{"3%"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := mustExec(t, NewBashTool(dir), fmt.Sprintf(`{"command":%q}`, tt.command))
			got := textOf(t, res)
			if tt.want != "" && got != tt.want {
				t.Errorf("output = %q, want %q", got, tt.want)
			}
			for _, sub := range tt.wantSub {
				if !strings.Contains(got, sub) {
					t.Errorf("output %q missing %q", got, sub)
				}
			}
			if strings.ContainsRune(got, '\r') || strings.ContainsRune(got, '\x1b') {
				t.Errorf("output %q still contains CR/ESC bytes", got)
			}
		})
	}
}

func TestBashTool_SignalExitCode(t *testing.T) {
	// Not running t.Parallel() to avoid cross-test signal interference.
	dir := t.TempDir()
	// 128+signal mapping, table-driven.
	for _, tc := range []struct {
		cmd  string
		code int
	}{
		{"kill -TERM $$", 143},
		{"kill -KILL $$", 137},
	} {
		result, err := execTool(t, NewBashTool(dir), fmt.Sprintf(`{"command":%q}`, tc.cmd))
		if err != nil {
			t.Fatalf("%s: unexpected execution error: %v", tc.cmd, err)
		}
		if !result.IsError {
			t.Errorf("%s: expected IsError result for signal exit", tc.cmd)
		}
		if len(result.Content) == 0 {
			t.Fatalf("%s: expected content in result", tc.cmd)
		}
		text := fmt.Sprintf("%v", result.Content[0])
		if want := fmt.Sprintf("Command exited with code %d", tc.code); !strings.Contains(text, want) {
			t.Errorf("%s: content %q should contain %q", tc.cmd, text, want)
		}
		if strings.Contains(text, "code -1") {
			t.Errorf("%s: content %q should not contain 'code -1'", tc.cmd, text)
		}
	}
}
