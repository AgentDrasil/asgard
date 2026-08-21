package agy

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/backend/lib/agentwrapper/types"
)

func TestModels(t *testing.T) {
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
