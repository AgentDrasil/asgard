package jev_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/pkg/jev"
)

func TestClient_E2E_LiveAPI(t *testing.T) {
	if os.Getenv("E2E_TEST") != "true" {
		t.Skip("Skipping live E2E test; set E2E_TEST=true to run it.")
	}

	apiKey := os.Getenv("TYPESAFE_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping live E2E test; set TYPESAFE_API_KEY to run it.")
	}

	client, err := jev.NewClient(apiKey)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("ListModels live check", func(t *testing.T) {
		modelsResp, err := client.ListModels(ctx)
		require.NoError(t, err)
		require.NotNil(t, modelsResp)
		assert.NotEmpty(t, modelsResp.Models)

		var hasJevLatest bool
		for _, m := range modelsResp.Models {
			if m.Name == "jev-latest" {
				hasJevLatest = true
				break
			}
		}
		assert.True(t, hasJevLatest, "expected jev-latest model in live models list")
	})

	t.Run("Evaluate live check with all 3 question types", func(t *testing.T) {
		req := jev.EvaluateRequest{
			State: "Help! My payouts have been failing for 3 days.",
			Model: "jev-latest",
			Questions: map[string]jev.Question{
				"is_urgent": jev.NewNoul("Does this convey urgency?", jev.NoulCriteria{
					True:  "Explicitly time-sensitive",
					False: "No urgency expressed",
				}),
				"department": jev.NewChoice("Which team should handle this?", map[string]any{
					"billing":   "Payments, invoicing, refunds",
					"technical": "Bugs, outages, integrations",
					"sales":     "Pricing, upgrades, new accounts",
				}),
				"frustration": jev.NewScore("How frustrated is the customer?", []any{
					"Calm", "Frustrated", "Very angry",
				}),
			},
		}

		resp, err := client.Evaluate(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.NotEmpty(t, resp.Model)
		assert.Greater(t, resp.Usage.InputTokens, 0)

		// Check Noul
		noulAns, ok := resp.Answers["is_urgent"]
		require.True(t, ok)
		assert.Equal(t, jev.QuestionTypeNoul, noulAns.Type)
		assert.GreaterOrEqual(t, noulAns.Noul, 0.0)
		assert.LessOrEqual(t, noulAns.Noul, 1.0)

		// Check Choice
		choiceAns, ok := resp.Answers["department"]
		require.True(t, ok)
		assert.Equal(t, jev.QuestionTypeChoice, choiceAns.Type)
		assert.Contains(t, []string{"billing", "technical", "sales"}, choiceAns.Choice)
		assert.NotEmpty(t, choiceAns.Probabilities)
		assert.GreaterOrEqual(t, choiceAns.Confidence, 0.0)

		// Check Score
		scoreAns, ok := resp.Answers["frustration"]
		require.True(t, ok)
		assert.Equal(t, jev.QuestionTypeScore, scoreAns.Type)
		assert.GreaterOrEqual(t, scoreAns.Score, 0.0)
		assert.NotEmpty(t, scoreAns.Legend)
		assert.NotEmpty(t, scoreAns.Probabilities)
	})
}
