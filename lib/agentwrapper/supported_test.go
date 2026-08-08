package agentwrapper

import (
	"context"
	"testing"

	"github.com/AgentDrasil/asgard/lib/agentwrapper/types"
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
