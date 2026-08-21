package agentwrapper

import (
	"context"
	"testing"

	"github.com/AgentDrasil/asgard/backend/lib/agentwrapper/types"
)

func TestGetQuota_DetailedOptions(t *testing.T) {
	var capturedOpts types.UsageOptions

	fake := NewFakeClient()
	fake.UsageFunc = func(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
		capturedOpts = opts
		return []types.ModelUsage{
			{
				Model:     "test-model",
				Remaining: 0.9,
				Limits: []types.QuotaLimit{
					{Name: "5h", Remaining: 0.9},
					{Name: "weekly", Remaining: 1.0},
				},
			},
		}, nil
	}

	origClients := clients
	t.Cleanup(func() { clients = origClients })

	clients = map[string]types.CLIClient{
		"fake-agent": fake,
	}

	res, err := GetQuota(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !capturedOpts.Detailed {
		t.Errorf("expected Detailed option to be true when called from GetQuota, got false")
	}

	usages, ok := res["fake-agent"]
	if !ok || len(usages) != 1 {
		t.Fatalf("expected 1 model usage for fake-agent, got %v", res)
	}

	if len(usages[0].Limits) != 2 {
		t.Errorf("expected 2 limits, got %d", len(usages[0].Limits))
	}
}

func TestCheckQuota_ModelVariant(t *testing.T) {
	fake := NewFakeClient()
	fake.UsageFunc = func(ctx context.Context, opts types.UsageOptions) ([]types.ModelUsage, error) {
		return []types.ModelUsage{
			{
				Model:     "zai-coding-plan/glm-5.3",
				Remaining: 0.85,
			},
		}, nil
	}

	origClients := clients
	t.Cleanup(func() { clients = origClients })

	clients = map[string]types.CLIClient{
		"opencode": fake,
	}

	// Exact match
	q1 := CheckQuota("opencode", "zai-coding-plan/glm-5.3")
	if q1 != 0.85 {
		t.Errorf("expected quota 0.85 for exact model, got %f", q1)
	}

	// Variant match
	q2 := CheckQuota("opencode", "zai-coding-plan/glm-5.3/low")
	if q2 != 0.85 {
		t.Errorf("expected quota 0.85 for model with variant, got %f", q2)
	}

	// Non-matching model
	q3 := CheckQuota("opencode", "other-provider/other-model/low")
	if q3 != 0.0 {
		t.Errorf("expected quota 0.0 for unmatched model, got %f", q3)
	}
}
