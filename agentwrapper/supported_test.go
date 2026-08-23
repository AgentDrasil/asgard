package agentwrapper

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/AgentDrasil/asgard/agentwrapper/simplest"
	"github.com/AgentDrasil/asgard/agentwrapper/types"
	simplestpkg "github.com/AgentDrasil/asgard/simplest"
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

func TestSupportedCLIs_SimplestRegistration(t *testing.T) {
	origClients := clients
	t.Cleanup(func() { clients = origClients })
	SetClients(nil)

	registered := GetRegisteredCLIs()
	found := false
	for _, name := range registered {
		if name == "simplest" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected simplest to be registered in GetRegisteredCLIs, got: %v", registered)
	}

	spec := GetSandboxSpec("simplest")
	if spec == nil {
		t.Fatalf("expected simplest to implement SandboxSpec")
	}
	if spec.SystemPromptHeader() == "" {
		t.Errorf("expected non-empty SystemPromptHeader for simplest")
	}
	if spec.SystemPromptPeerHeader() == "" {
		t.Errorf("expected non-empty SystemPromptPeerHeader for simplest")
	}
}

func TestSupportedCLIs_SimplestModelsAndQuota(t *testing.T) {
	// Fixture: simplest whitelist config
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	validYAML := `providers:
  google:
    api: google-generative-ai
    apiKey: test-key
models:
  - id: gemini-3.7-flash
    provider: google
  - id: gemini-3.7-pro
    provider: google
`
	if err := os.WriteFile(cfgPath, []byte(validYAML), 0600); err != nil {
		t.Fatalf("writing fixture config: %v", err)
	}
	t.Setenv("SIMPLEST_CONFIG_PATH", cfgPath)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	simplestpkg.ResetGlobalConfig()
	t.Cleanup(simplestpkg.ResetGlobalConfig)

	origClients := clients
	t.Cleanup(func() { clients = origClients })
	clients = map[string]types.CLIClient{"simplest": &simplest.Client{}}

	// 1) GetSupportedCLIsAndModels
	models := GetSupportedCLIsAndModels()
	got := models["simplest"]
	if len(got) != 2 {
		t.Fatalf("expected 2 whitelisted models for simplest, got %v", got)
	}
	expectedModels := map[string]bool{
		"gemini-3.7-flash": true,
		"gemini-3.7-pro":   true,
	}
	for _, m := range got {
		if !expectedModels[m] {
			t.Errorf("unexpected model %q in simplest models: %v", m, got)
		}
	}

	// 2) GetQuota
	quota, err := GetQuota(context.Background())
	if err != nil {
		t.Fatalf("unexpected error from GetQuota: %v", err)
	}
	usages, ok := quota["simplest"]
	if !ok || len(usages) != 2 {
		t.Fatalf("expected 2 usage entries for simplest, got %v", usages)
	}
	for _, u := range usages {
		if !expectedModels[u.Model] {
			t.Errorf("unexpected usage model %q", u.Model)
		}
		if u.Remaining != 1.0 {
			t.Errorf("expected remaining 1.0 for model %q, got %f", u.Model, u.Remaining)
		}
	}
}
