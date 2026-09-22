package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/AgentDrasil/asgard/pkg/metrics"
)

const (
	DefaultOutputDir = "/tmp/fakebash-outputs"
	LatestSymlink    = "latest.log"
)

var validCmdIDRegex = regexp.MustCompile(`^c-\d+-\d+-[0-9a-fA-F]+$`)

// Run executes show-output logic and returns the exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("show-output", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		tailN    int
		grepPat  string
		showPath bool
	)

	fs.IntVar(&tailN, "tail", 0, "Show the last N lines of output")
	fs.IntVar(&tailN, "n", 0, "Show the last N lines of output (shorthand)")
	fs.StringVar(&grepPat, "grep", "", "Filter output lines matching regex pattern")
	fs.BoolVar(&showPath, "path", false, "Print absolute path to the log file instead of its content")

	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: show-output [options] [cmd-id]\n\n")
		_, _ = fmt.Fprintf(stderr, "Inspect raw outputs of commands executed by fakebash.\n\n")
		_, _ = fmt.Fprintf(stderr, "Arguments:\n")
		_, _ = fmt.Fprintf(stderr, "  [cmd-id]    Specific command ID (e.g. c-1726000000-1234-abcd). If omitted or 'latest', uses the latest command output.\n\n")
		_, _ = fmt.Fprintf(stderr, "Options:\n")
		fs.PrintDefaults()
		_, _ = fmt.Fprintf(stderr, "\nExamples:\n")
		_, _ = fmt.Fprintf(stderr, "  show-output\n")
		_, _ = fmt.Fprintf(stderr, "  show-output c-1726000000-1234-abcd\n")
		_, _ = fmt.Fprintf(stderr, "  show-output --tail=50\n")
		_, _ = fmt.Fprintf(stderr, "  show-output -n 20 c-1726000000-1234-abcd\n")
		_, _ = fmt.Fprintf(stderr, "  show-output --grep='(?i)error'\n")
		_, _ = fmt.Fprintf(stderr, "  show-output --path\n")
	}

	if err := fs.Parse(args); err != nil {
		return 1
	}

	cmdID := "latest"
	if fs.NArg() > 0 {
		cmdID = fs.Arg(0)
	}

	logPath, err := resolveLogPath(cmdID)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}

	if showPath {
		_, _ = fmt.Fprintln(stdout, logPath)
		return 0
	}

	// Content writes are tallied so the backend can report how much raw output
	// was pulled back past the compression pipeline.
	countingStdout := &countingWriter{w: stdout}

	f, err := os.Open(logPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Error opening log file %s: %v\n", logPath, err)
		return 1
	}
	defer func() { _ = f.Close() }()

	var grepRE *regexp.Regexp
	if grepPat != "" {
		re, err := regexp.Compile(grepPat)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error invalid regex pattern %q: %v\n", grepPat, err)
			return 1
		}
		grepRE = re
	}

	scanner := bufio.NewScanner(f)
	// Support long lines (up to 1MB)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		if grepRE != nil && !grepRE.MatchString(line) {
			continue
		}
		if tailN > 0 {
			lines = append(lines, line)
			if len(lines) > tailN {
				lines = lines[1:]
			}
		} else {
			_, _ = fmt.Fprintln(countingStdout, line)
		}
	}

	if err := scanner.Err(); err != nil {
		_, _ = fmt.Fprintf(stderr, "Error reading log file: %v\n", err)
		return 1
	}

	if tailN > 0 {
		for _, line := range lines {
			_, _ = fmt.Fprintln(countingStdout, line)
		}
	}

	reportUsage(countingStdout.n)

	return 0
}

// countingWriter tallies the bytes handed to the wrapped writer.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// reportUsage tells the backend how many times show-output retrieved raw output
// and how many bytes it surfaced. Best-effort: see metrics.Report.
func reportUsage(bytes int64) {
	metrics.Report(context.Background(), metrics.Event{Kind: metrics.KindShowOutput, Bytes: bytes})
}

func resolveLogPath(cmdID string) (string, error) {
	baseDir := os.Getenv("FAKEBASH_OUTPUT_DIR")
	if baseDir == "" {
		baseDir = DefaultOutputDir
	}

	trimmed := strings.TrimSpace(cmdID)
	if trimmed == "" || trimmed == "latest" {
		latestPath := filepath.Join(baseDir, LatestSymlink)
		resolved, err := filepath.EvalSymlinks(latestPath)
		if err != nil {
			return "", fmt.Errorf("no latest log found in %s: %w", baseDir, err)
		}
		return resolved, nil
	}

	if !validCmdIDRegex.MatchString(trimmed) {
		return "", fmt.Errorf("invalid command ID format: %s", trimmed)
	}

	targetPath := filepath.Join(baseDir, trimmed+".log")
	cleanTarget := filepath.Clean(targetPath)
	cleanBase := filepath.Clean(baseDir)
	rel, err := filepath.Rel(cleanBase, cleanTarget)
	if err != nil || strings.HasPrefix(rel, "..") || strings.Contains(rel, string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal attempt detected: %s", cmdID)
	}

	if _, err := os.Stat(cleanTarget); err != nil {
		return "", fmt.Errorf("log file not found for command ID %s", trimmed)
	}

	return cleanTarget, nil
}

func main() {
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
}
