package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/metrics"
)

func setupTestLogDir(t *testing.T) (dir string, cmdID string, filePath string) {
	t.Helper()
	dir = t.TempDir()
	t.Setenv("FAKEBASH_OUTPUT_DIR", dir)

	cmdID = "c-1700000000000-1234-abcd"
	filePath = filepath.Join(dir, cmdID+".log")

	var sb strings.Builder
	sb.WriteString("# CMD: go test ./...\n")
	for i := 1; i <= 100; i++ {
		switch i {
		case 50:
			fmt.Fprintf(&sb, "Line %d: ERROR something went wrong\n", i)
		case 75:
			fmt.Fprintf(&sb, "Line %d: ERROR second failure\n", i)
		default:
			fmt.Fprintf(&sb, "Line %d: PASS normal output\n", i)
		}
	}
	sb.WriteString("# EXIT: 1\n")

	err := os.WriteFile(filePath, []byte(sb.String()), 0o644)
	require.NoError(t, err)

	latestPath := filepath.Join(dir, LatestSymlink)
	err = os.Symlink(cmdID+".log", latestPath)
	require.NoError(t, err)

	return dir, cmdID, filePath
}

func TestShowOutput_Run_Latest(t *testing.T) {
	_, _, filePath := setupTestLogDir(t)

	var stdout, stderr bytes.Buffer
	code := Run([]string{}, &stdout, &stderr)
	assert.Equal(t, 0, code)
	assert.Empty(t, stderr.String())

	rawContent, err := os.ReadFile(filePath)
	require.NoError(t, err)
	// Trimming trailing newline differences for strict comparison
	assert.Equal(t, strings.TrimRight(string(rawContent), "\n"), strings.TrimRight(stdout.String(), "\n"))
}

func TestShowOutput_Run_Tail(t *testing.T) {
	_, cmdID, _ := setupTestLogDir(t)

	tests := []struct {
		name string
		args []string
	}{
		{"flag --tail=10", []string{"--tail=10", cmdID}},
		{"flag -n 10", []string{"-n", "10", cmdID}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tt.args, &stdout, &stderr)
			assert.Equal(t, 0, code)
			assert.Empty(t, stderr.String())

			lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
			assert.Len(t, lines, 10)
			assert.Equal(t, "# EXIT: 1", lines[len(lines)-1])
			assert.Equal(t, "Line 100: PASS normal output", lines[len(lines)-2])
		})
	}
}

func TestShowOutput_Run_Grep(t *testing.T) {
	_, cmdID, _ := setupTestLogDir(t)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"--grep=ERROR", cmdID}, &stdout, &stderr)
	assert.Equal(t, 0, code)
	assert.Empty(t, stderr.String())

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	assert.Len(t, lines, 2)
	assert.Contains(t, lines[0], "Line 50: ERROR something went wrong")
	assert.Contains(t, lines[1], "Line 75: ERROR second failure")
}

func TestShowOutput_Run_Path(t *testing.T) {
	_, cmdID, filePath := setupTestLogDir(t)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"--path", cmdID}, &stdout, &stderr)
	assert.Equal(t, 0, code)
	assert.Empty(t, stderr.String())

	assert.Equal(t, filePath, strings.TrimSpace(stdout.String()))
}

func TestShowOutput_Run_NotFound(t *testing.T) {
	_, _, _ = setupTestLogDir(t)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"c-9999999999999-9999-ffff"}, &stdout, &stderr)
	assert.Equal(t, 1, code)
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "log file not found")
}

func TestShowOutput_Run_InvalidID(t *testing.T) {
	_, _, _ = setupTestLogDir(t)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"../../etc/passwd"}, &stdout, &stderr)
	assert.Equal(t, 1, code)
	assert.Empty(t, stdout.String())
	assert.Contains(t, stderr.String(), "invalid command ID format")
}

// setupMetricsRecorder installs a stand-in backend and returns the events it
// captured.
func setupMetricsRecorder(t *testing.T) *[]metrics.Event {
	t.Helper()
	var (
		mu     sync.Mutex
		events []metrics.Event
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, metrics.EndpointPath, r.URL.Path)
		var batch []metrics.Event
		require.NoError(t, json.NewDecoder(r.Body).Decode(&batch))
		mu.Lock()
		events = append(events, batch...)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	t.Setenv("ASGARD_INTERNAL_API_HOST", server.URL)
	return &events
}

func TestShowOutput_Run_ReportsBytesSurfaced(t *testing.T) {
	_, cmdID, _ := setupTestLogDir(t)
	events := setupMetricsRecorder(t)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"--tail=10", cmdID}, &stdout, &stderr)
	require.Equal(t, 0, code)

	require.Len(t, *events, 1)
	assert.Equal(t, metrics.KindShowOutput, (*events)[0].Kind)
	assert.Equal(t, int64(stdout.Len()), (*events)[0].Bytes)
}

func TestShowOutput_Run_PathOnlyReportsNothing(t *testing.T) {
	_, cmdID, _ := setupTestLogDir(t)
	events := setupMetricsRecorder(t)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"--path", cmdID}, &stdout, &stderr)
	require.Equal(t, 0, code)

	assert.Empty(t, *events, "--path surfaces no raw output, so there is nothing to tally")
}
