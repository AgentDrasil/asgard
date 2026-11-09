package agy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/common"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
)

const fakeResult = `{"event":"result","result":{"conversation_id":"c1","status":"SUCCESS","response":"done","num_turns":1}}`

// fakeAgy installs an `agy` shell script on PATH, isolated from any real
// contract or home directory.
func fakeAgy(t *testing.T, script string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(common.EnvVar, filepath.Join(t.TempDir(), "missing-AW_AGENTS.md"))
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agy"), []byte("#!/bin/bash\n"+script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestPromptSendsPromptOnStdinAndClosesAfterResult(t *testing.T) {
	inFile := filepath.Join(t.TempDir(), "in")
	// The trailing cat only returns on EOF: a hang here means stdin stayed open.
	fakeAgy(t, `read -r line
echo "$line" > `+inFile+`
echo '`+fakeResult+`'
cat > /dev/null`)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := Prompt(ctx, "hello", types.PromptOptions{Dir: t.TempDir()})
	require.NoError(t, err)
	assert.Equal(t, "done", res.LastContent)

	got, err := os.ReadFile(inFile)
	require.NoError(t, err)
	assert.JSONEq(t, `{"event":"user","message":{"content":"hello"}}`, string(got))
}

func TestPromptInjectMidRun(t *testing.T) {
	inFile := filepath.Join(t.TempDir(), "in")
	fakeAgy(t, `read -r l1
read -r l2
printf '%s\n%s\n' "$l1" "$l2" > `+inFile+`
echo '`+fakeResult+`'
echo '`+fakeResult+`'
cat > /dev/null`)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	calls := 0
	var injectErr error
	_, err := Prompt(ctx, "first", types.PromptOptions{Dir: t.TempDir(), OnInjectable: func(inject func(string) error) {
		calls++
		injectErr = inject("second")
	}})
	require.NoError(t, err)
	require.NoError(t, injectErr)
	assert.Equal(t, 1, calls)

	got, err := os.ReadFile(inFile)
	require.NoError(t, err)
	assert.Contains(t, string(got), `"content":"first"`)
	assert.Contains(t, string(got), `"content":"second"`)
}

func TestPromptInjectAfterFinishIsRejected(t *testing.T) {
	var saved func(string) error
	fakeAgy(t, `read -r line
echo '`+fakeResult+`'
cat > /dev/null`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Prompt(ctx, "hello", types.PromptOptions{Dir: t.TempDir(), OnInjectable: func(inject func(string) error) { saved = inject }})
	require.NoError(t, err)
	require.NotNil(t, saved)
	assert.ErrorIs(t, saved("late"), types.ErrNotInjectable)
}
