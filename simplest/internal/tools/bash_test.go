package tools

import (
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
			_, err := execTool(t, NewBashTool(dir), tt.args)
			if err == nil {
				t.Fatal("expected error")
			}
			for _, sub := range tt.wantSub {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q missing %q", err.Error(), sub)
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
