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

func TestUsageDeduplicationAndArgonSeparation(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/sh
case "$*" in
  *models*)
    echo '{"command":{"name":"models","data":{"models":[{"id":"gemini-4-argon-medium"},{"id":"gemini-4-argon-high"},{"id":"gemini-3.8-flash-high"},{"id":"gemini-3.8-flash-low"},{"id":"claude-opus-5-5-low"},{"id":"claude-opus-5-5-high"}]}}}'
    ;;
  *"/usage"*)
    echo '{"command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","buckets":[{"id":"gemini-weekly","name":"Weekly Limit","window":"weekly","remaining_fraction":0.45,"reset_time":"2026-11-06T22:35:57Z"},{"id":"gemini-5h","name":"5h Limit","window":"5h","remaining_fraction":0.99,"reset_time":"2026-11-06T03:09:25Z"}]},{"name":"Claude and GPT models","buckets":[{"id":"3p-weekly","window":"weekly","remaining_fraction":0.5,"reset_time":"2026-11-06T01:20:21Z"},{"id":"3p-5h","window":"5h","remaining_fraction":1.0,"reset_time":"2026-11-06T03:10:30Z"}]},{"name":"Gemini 4 Argon","buckets":[{"id":"gemini-argon-wtu-weekly","window":"weekly","remaining_fraction":0.95,"reset_time":"2026-11-11T15:15:24Z"}]}]}}}'
    ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agy"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	usages, err := Usage(context.Background(), types.UsageOptions{Detailed: true})
	require.NoError(t, err)

	// Deduplicated: 3 base models instead of 6 variants
	require.Len(t, usages, 3)

	byModel := make(map[string]types.ModelUsage)
	for _, u := range usages {
		byModel[u.Model] = u
	}

	// 1. Gemini 4 Argon: has its own separate quota (0.95)
	argon, ok := byModel["gemini-4-argon"]
	require.True(t, ok, "expected gemini-4-argon base model")
	require.InDelta(t, 0.95, argon.Remaining, 1e-6)
	require.Len(t, argon.Limits, 1)
	require.Equal(t, "weekly", argon.Limits[0].Name)
	require.InDelta(t, 0.95, argon.Limits[0].Remaining, 1e-6)

	// 2. Gemini 3.8 Flash: uses Gemini Models bucket (0.45 weekly, 0.99 5h)
	flash, ok := byModel["gemini-3.8-flash"]
	require.True(t, ok, "expected gemini-3.8-flash base model")
	require.InDelta(t, 0.45, flash.Remaining, 1e-6)
	require.Len(t, flash.Limits, 2)
	require.Equal(t, "5h", flash.Limits[0].Name)
	require.InDelta(t, 0.99, flash.Limits[0].Remaining, 1e-6)
	require.Equal(t, "weekly", flash.Limits[1].Name)
	require.InDelta(t, 0.45, flash.Limits[1].Remaining, 1e-6)

	// 3. Claude Opus 5.5: uses 3P bucket (0.5 weekly, 1.0 5h)
	opus, ok := byModel["claude-opus-5-5"]
	require.True(t, ok, "expected claude-opus-5-5 base model")
	require.InDelta(t, 0.5, opus.Remaining, 1e-6)
	require.Len(t, opus.Limits, 2)
}
