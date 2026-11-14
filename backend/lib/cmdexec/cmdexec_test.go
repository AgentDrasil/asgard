package cmdexec

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/bwrap"
)

// skipInAsgardSandbox skips tests that spawn a real bash: inside an Asgard
// sandbox bash is replaced by fakebash.
func skipInAsgardSandbox(t *testing.T) {
	t.Helper()
	if os.Getenv(bwrap.SandboxedEnv) == "1" {
		t.Skipf("%s=1: bash is fakebash inside an Asgard sandbox", bwrap.SandboxedEnv)
	}
}

func TestRunDirectOutputExitCodeAndEnv(t *testing.T) {
	skipInAsgardSandbox(t)
	dir := t.TempDir()
	var out, errb bytes.Buffer
	code, err := RunDirect(t.Context(), `echo "$FOO"; pwd; echo bad >&2; exit 3`, dir, []string{"FOO=bar", "PATH=" + os.Getenv("PATH")}, &out, &errb)
	require.NoError(t, err)
	assert.Equal(t, 3, code)
	real, _ := filepath.EvalSymlinks(dir)
	assert.Equal(t, "bar\n"+real+"\n", out.String())
	assert.Equal(t, "bad\n", errb.String())
}

func TestRunDirectCancelKillsTheProcessGroup(t *testing.T) {
	skipInAsgardSandbox(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		var out bytes.Buffer
		_, _ = RunDirect(ctx, "sleep 300 & echo $! > pid; wait", dir, nil, &out, &out)
	}()
	var pid int
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	}, 10*time.Second, 20*time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RunDirect did not return after cancel")
	}
	require.Eventually(t, func() bool {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return true
		}
		i := strings.LastIndex(string(b), ")")
		return i >= 0 && len(b) > i+2 && b[i+2] == 'Z'
	}, 10*time.Second, 20*time.Millisecond, "the child of the shell must be killed with its group")
}
