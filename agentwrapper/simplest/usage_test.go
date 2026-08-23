package simplest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/agentwrapper/types"
	"github.com/AgentDrasil/asgard/simplest"
)

func TestModelsAndUsage(t *testing.T) {
	// Set mock global config in simplest
	t.Cleanup(func() {
		simplest.ResetGlobalConfig()
	})

	simplest.SetGlobalConfig(&simplest.Config{
		Providers: map[string]simplest.ProviderConfig{
			"google": {API: simplest.APIGoogleGenerativeAI, APIKey: "fake-key"},
		},
		Models: []simplest.ModelConfig{
			{
				ID:            "gemini-3.7-flash",
				Name:          "Gemini 3.7 Flash",
				Provider:      "google",
				ContextWindow: 1048576,
			},
			{
				ID:            "unwhitelisted-model",
				Name:          "Unwhitelisted Model",
				Provider:      "google",
				ContextWindow: 100000,
			},
		},
		Whitelist: []string{"gemini-3.7-flash"},
	})

	ctx := context.Background()
	opts := types.UsageOptions{}

	models, err := Models(ctx, opts)
	require.NoError(t, err)
	assert.Equal(t, []string{"gemini-3.7-flash"}, models)

	usages, err := Usage(ctx, opts)
	require.NoError(t, err)
	require.Len(t, usages, 1)
	assert.Equal(t, "gemini-3.7-flash", usages[0].Model)
	assert.Equal(t, 1.0, usages[0].Remaining)
	assert.Equal(t, int64(0), usages[0].RefreshDate)
}
