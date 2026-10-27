package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func findSetup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, ".gitignore", "node_modules/\n")
	writeFile(t, dir, "README.md", "x\n")
	writeFile(t, dir, "src/main.go", "package main\n")
	writeFile(t, dir, "src/util/helper.go", "package util\n")
	writeFile(t, dir, "src/util/deep/nested.go", "package deep\n")
	writeFile(t, dir, "node_modules/dep.js", "ignored\n")
	return dir
}

func TestFindToolGlobPatterns(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		wantAll []string
		notWant []string
	}{
		{
			name:    "basename glob *.go finds nested matches",
			pattern: "*.go",
			wantAll: []string{"src/main.go", "src/util/helper.go", "src/util/deep/nested.go"},
			notWant: []string{"README.md", "node_modules/dep.js"},
		},
		{
			name:    "doublestar **/*.go",
			pattern: "**/*.go",
			wantAll: []string{"src/main.go", "src/util/helper.go", "src/util/deep/nested.go"},
			notWant: []string{"README.md"},
		},
		{
			name:    "anchored src/**/*.go",
			pattern: "src/**/*.go",
			wantAll: []string{"src/main.go", "src/util/helper.go", "src/util/deep/nested.go"},
		},
		{
			name:    "exact subdirectory prefix",
			pattern: "src/util/*.go",
			wantAll: []string{"src/util/helper.go"},
			notWant: []string{"src/main.go", "src/util/deep/nested.go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := findSetup(t)
			out := textOf(t, mustExec(t, NewFindTool(dir), `{"pattern":`+quote(tt.pattern)+`}`))
			for _, want := range tt.wantAll {
				if !strings.Contains(out, want) {
					t.Errorf("results missing %q:\n%s", want, out)
				}
			}
			for _, banned := range tt.notWant {
				if strings.Contains(out, banned) {
					t.Errorf("results should not contain %q:\n%s", banned, out)
				}
			}
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, "[") || line == "" {
					continue
				}
				if strings.HasPrefix(line, "/") || !strings.Contains(line, ".") && !strings.Contains(line, "/") {
					t.Errorf("expected relative path lines, got %q", line)
				}
			}
		})
	}
}

func quote(s string) string {
	return `"` + s + `"`
}

func TestFindToolLimitNotice(t *testing.T) {
	dir := findSetup(t)
	res := mustExec(t, NewFindTool(dir), `{"pattern":"*.go","limit":2}`)
	out := textOf(t, res)
	if !strings.Contains(out, "[2 results limit reached. Use limit=4 for more]") {
		t.Errorf("missing limit notice:\n%s", out)
	}
	details := res.Details.(*FindToolDetails)
	if details.ResultLimitReached != 2 {
		t.Errorf("ResultLimitReached = %d, want 2", details.ResultLimitReached)
	}
}

func TestFindToolLimitSemantics(t *testing.T) {
	dir := findSetup(t)

	t.Run("limit 0 returns no results", func(t *testing.T) {
		t.Parallel()
		out := textOf(t, mustExec(t, NewFindTool(dir), `{"pattern":"*.go","limit":0}`))
		require.Equal(t, "No files found matching pattern", out)
	})

	t.Run("negative limit returns no results", func(t *testing.T) {
		t.Parallel()
		out := textOf(t, mustExec(t, NewFindTool(dir), `{"pattern":"*.go","limit":-5}`))
		require.Equal(t, "No files found matching pattern", out)
	})

	t.Run("fractional limit floors", func(t *testing.T) {
		t.Parallel()
		res := mustExec(t, NewFindTool(dir), `{"pattern":"*.go","limit":1.5}`)
		full := textOf(t, res)
		out := strings.Split(full, "\n\n")[0]
		assert.Equal(t, "src/main.go", out, "floor(1.5)=1 should return exactly one result")
		assert.Contains(t, full, "[1 results limit reached. Use limit=2 for more]")
		assert.Equal(t, 1, res.Details.(*FindToolDetails).ResultLimitReached)
	})

	t.Run("omitted limit defaults to 1000", func(t *testing.T) {
		t.Parallel()
		out := textOf(t, mustExec(t, NewFindTool(dir), `{"pattern":"*.go"}`))
		assert.Contains(t, out, "src/main.go")
		assert.Contains(t, out, "src/util/helper.go")
		assert.Contains(t, out, "src/util/deep/nested.go")
	})

	t.Run("huge limit does not overflow", func(t *testing.T) {
		t.Parallel()
		out := textOf(t, mustExec(t, NewFindTool(dir), `{"pattern":"*.go","limit":1e300}`))
		assert.Contains(t, out, "src/main.go")
		assert.Contains(t, out, "src/util/helper.go")
		assert.Contains(t, out, "src/util/deep/nested.go")
		assert.NotContains(t, out, "limit reached")
	})

	t.Run("32-bit overflow class limit", func(t *testing.T) {
		t.Parallel()
		out := textOf(t, mustExec(t, NewFindTool(dir), `{"pattern":"*.go","limit":1e10}`))
		assert.Contains(t, out, "src/util/deep/nested.go")
		assert.NotContains(t, out, "limit reached")
	})
}

func TestFindToolNoFilesFound(t *testing.T) {
	dir := findSetup(t)
	out := textOf(t, mustExec(t, NewFindTool(dir), `{"pattern":"*.zzz"}`))
	if out != "No files found matching pattern" {
		t.Errorf("out = %q, want 'No files found matching pattern'", out)
	}
}

func TestFindToolGitignoreRespected(t *testing.T) {
	dir := findSetup(t)
	out := textOf(t, mustExec(t, NewFindTool(dir), `{"pattern":"**/*"}`))
	if strings.Contains(out, "node_modules") {
		t.Errorf("gitignored directory leaked into find results:\n%s", out)
	}
	if !strings.Contains(out, "README.md") || !strings.Contains(out, "src/main.go") {
		t.Errorf("expected tracked files in results:\n%s", out)
	}
}

func TestFindToolCanceledContext(t *testing.T) {
	t.Parallel()
	dir := findSetup(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewFindTool(dir).Execute(ctx, "test-call-id", json.RawMessage(`{"pattern":"*"}`), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "operation aborted")
}

func TestFindToolBadPath(t *testing.T) {
	dir := t.TempDir()
	_, err := execTool(t, NewFindTool(dir), `{"pattern":"*","path":"missing-dir"}`)
	if err == nil || !strings.Contains(err.Error(), "path not found or not a directory") {
		t.Errorf("err = %v, want path error", err)
	}
}
