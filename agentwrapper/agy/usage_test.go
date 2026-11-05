package agy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/llms"
)

func TestModels(t *testing.T) {
	// Isolate HOME so the real agy CLI cannot write its state into the
	// developer's home; without credentials the run degrades to a skip.
	t.Setenv("HOME", t.TempDir())

	ctx := context.Background()
	models, err := Models(ctx, types.UsageOptions{})
	if err != nil {
		t.Skipf("Skipping TestModels: agy models returned error (e.g. unauthenticated): %v", err)
	}
	require.NotEmpty(t, models)

	hasGemini := false
	for _, m := range models {
		if strings.HasPrefix(m, "gemini") {
			hasGemini = true
			break
		}
	}
	require.True(t, hasGemini, "Expected models to include gemini models, got: %v", models)
}

func TestUsageNoCredentials(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
case "$*" in
  *models*) echo '{"command":{"name":"models","data":{"models":[{"id":"gemini-x"}]}}}' ;;
  *) echo 'Authentication required' ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agy"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := Usage(context.Background(), types.UsageOptions{})
	require.Error(t, err)
	require.True(t, llms.IsNoCredentialsText(err.Error()), err.Error())
}
