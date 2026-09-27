package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string {
	return &s
}

func TestModel_SupportsThinkingLevel_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		model         *Model
		level         ThinkingLevel
		wantSupported bool
	}{
		// Non-reasoning model tests (critical: reject non-off levels)
		{
			name: "non-reasoning model rejects low",
			model: &Model{
				ID:        "gpt-4o",
				Reasoning: false,
			},
			level:         ThinkingLow,
			wantSupported: false,
		},
		{
			name: "non-reasoning model rejects high",
			model: &Model{
				ID:        "gpt-4o",
				Reasoning: false,
			},
			level:         ThinkingHigh,
			wantSupported: false,
		},
		{
			name: "non-reasoning model accepts off",
			model: &Model{
				ID:        "gpt-4o",
				Reasoning: false,
			},
			level:         ThinkingOff,
			wantSupported: true,
		},
		{
			name: "non-reasoning model accepts empty string",
			model: &Model{
				ID:        "gpt-4o",
				Reasoning: false,
			},
			level:         "",
			wantSupported: true,
		},
		// Nil model pointer
		{
			name:          "nil model pointer accepts off",
			model:         nil,
			level:         ThinkingOff,
			wantSupported: true,
		},
		{
			name:          "nil model pointer accepts empty",
			model:         nil,
			level:         "",
			wantSupported: true,
		},
		{
			name:          "nil model pointer rejects low",
			model:         nil,
			level:         ThinkingLow,
			wantSupported: false,
		},
		// ThinkingLevelMap tests
		{
			name: "explicit nil in map disables level",
			model: &Model{
				ID:        "custom-reasoner",
				Reasoning: true,
				ThinkingLevelMap: ThinkingLevelMap{
					ThinkingMinimal: nil,
					ThinkingLow:     strPtr("low"),
					ThinkingHigh:    strPtr("high"),
				},
			},
			level:         ThinkingMinimal,
			wantSupported: false,
		},
		{
			name: "mapped alias in map is supported",
			model: &Model{
				ID:        "custom-reasoner",
				Reasoning: true,
				ThinkingLevelMap: ThinkingLevelMap{
					ThinkingXHigh: strPtr("max"),
				},
			},
			level:         ThinkingXHigh,
			wantSupported: true,
		},
		{
			name: "unmapped in ThinkingLevelMap falls back to ReasoningEffort whitelist",
			model: &Model{
				ID:              "fallback-model",
				Reasoning:       true,
				ReasoningEffort: []string{"low", "high"},
				ThinkingLevelMap: ThinkingLevelMap{
					ThinkingMinimal: nil,
				},
			},
			level:         ThinkingLow,
			wantSupported: true,
		},
		{
			name: "unmapped in ThinkingLevelMap rejected by ReasoningEffort whitelist",
			model: &Model{
				ID:              "fallback-model",
				Reasoning:       true,
				ReasoningEffort: []string{"low", "high"},
				ThinkingLevelMap: ThinkingLevelMap{
					ThinkingMinimal: nil,
				},
			},
			level:         ThinkingMax,
			wantSupported: false,
		},
		{
			name: "unmapped in ThinkingLevelMap without ReasoningEffort defaults to true",
			model: &Model{
				ID:        "open-reasoner",
				Reasoning: true,
				ThinkingLevelMap: ThinkingLevelMap{
					ThinkingMinimal: nil,
				},
			},
			level:         ThinkingHigh,
			wantSupported: true,
		},
		// Legacy ReasoningEffort only (ThinkingLevelMap is nil)
		{
			name: "legacy ReasoningEffort matches case-insensitively",
			model: &Model{
				ID:              "legacy-model",
				Reasoning:       true,
				ReasoningEffort: []string{"low", "high"},
			},
			level:         "LOW",
			wantSupported: true,
		},
		{
			name: "legacy ReasoningEffort rejects unlisted",
			model: &Model{
				ID:              "legacy-model",
				Reasoning:       true,
				ReasoningEffort: []string{"low", "high"},
			},
			level:         ThinkingMedium,
			wantSupported: false,
		},
		{
			name: "reasoning model with no restrictions defaults to true",
			model: &Model{
				ID:        "unrestricted-model",
				Reasoning: true,
			},
			level:         ThinkingMedium,
			wantSupported: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			supported := tt.model.SupportsThinkingLevel(tt.level)
			assert.Equal(t, tt.wantSupported, supported)
		})
	}
}

func TestModel_ClampThinkingLevel_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		model         *Model
		inputLevel    ThinkingLevel
		wantLevel     ThinkingLevel
		wantWireVal   *string
		wantUnclamped bool
	}{
		{
			name: "supported level returns as-is with ok=true",
			model: &Model{
				ID:        "reasoner",
				Reasoning: true,
				ThinkingLevelMap: ThinkingLevelMap{
					ThinkingLow:  strPtr("low"),
					ThinkingHigh: strPtr("high"),
				},
			},
			inputLevel:    ThinkingLow,
			wantLevel:     ThinkingLow,
			wantWireVal:   strPtr("low"),
			wantUnclamped: true,
		},
		{
			name: "mapped alias returned as-is with ok=true",
			model: &Model{
				ID:        "reasoner",
				Reasoning: true,
				ThinkingLevelMap: ThinkingLevelMap{
					ThinkingXHigh: strPtr("max"),
				},
			},
			inputLevel:    ThinkingXHigh,
			wantLevel:     ThinkingXHigh,
			wantWireVal:   strPtr("max"),
			wantUnclamped: true,
		},
		{
			name: "strictly downward convergence: high clamps to low when medium unsupported",
			model: &Model{
				ID:              "clamping-model",
				Reasoning:       true,
				ReasoningEffort: []string{"low", "off"},
				ThinkingLevelMap: ThinkingLevelMap{
					ThinkingOff: strPtr("none"),
					ThinkingLow: strPtr("low"),
				},
			},
			inputLevel:    ThinkingHigh,
			wantLevel:     ThinkingLow,
			wantWireVal:   strPtr("low"),
			wantUnclamped: false,
		},
		{
			name: "strictly downward convergence: minimal clamps to off when only medium/high supported",
			model: &Model{
				ID:              "med-high-only",
				Reasoning:       true,
				ReasoningEffort: []string{"medium", "high"},
			},
			inputLevel:    ThinkingMinimal,
			wantLevel:     ThinkingOff,
			wantWireVal:   nil,
			wantUnclamped: false,
		},
		{
			name: "non-reasoning model clamps any requested level to ThinkingOff",
			model: &Model{
				ID:        "non-reasoning",
				Reasoning: false,
			},
			inputLevel:    ThinkingHigh,
			wantLevel:     ThinkingOff,
			wantWireVal:   nil,
			wantUnclamped: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotLevel, gotWireVal, gotOk := tt.model.ClampThinkingLevel(tt.inputLevel)
			assert.Equal(t, tt.wantLevel, gotLevel)
			assert.Equal(t, tt.wantUnclamped, gotOk)
			if tt.wantWireVal == nil {
				assert.Nil(t, gotWireVal)
			} else {
				require.NotNil(t, gotWireVal)
				assert.Equal(t, *tt.wantWireVal, *gotWireVal)
			}
		})
	}
}

func TestModel_SupportsReasoningEffort_Deprecated(t *testing.T) {
	t.Parallel()

	m := &Model{
		ID:              "test-model",
		Reasoning:       true,
		ReasoningEffort: []string{"low", "high"},
	}

	assert.True(t, m.SupportsReasoningEffort("low"))
	assert.True(t, m.SupportsReasoningEffort("LOW"))
	assert.False(t, m.SupportsReasoningEffort("max"))

	nonReasoning := &Model{
		ID:        "non-reasoner",
		Reasoning: false,
	}
	assert.True(t, nonReasoning.SupportsReasoningEffort("off"))
	assert.True(t, nonReasoning.SupportsReasoningEffort(""))
	assert.False(t, nonReasoning.SupportsReasoningEffort("low"))
}

func TestModel_SupportsImage_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		model     *Model
		wantImage bool
	}{
		{
			name:      "nil model returns false",
			model:     nil,
			wantImage: false,
		},
		{
			name: "model without image input returns false",
			model: &Model{
				ID:    "text-only",
				Input: []string{"text"},
			},
			wantImage: false,
		},
		{
			name: "model with image input returns true",
			model: &Model{
				ID:    "multimodal",
				Input: []string{"text", "image"},
			},
			wantImage: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.wantImage, tt.model.SupportsImage())
		})
	}
}

func TestModel_GetImageLimits_TableDriven(t *testing.T) {
	t.Parallel()

	customLimits := &ModelImageInputLimits{
		Resize: &ModelImageResizeOptions{
			MaxWidth:    1024,
			MaxHeight:   768,
			MaxBytes:    1048576,
			JPEGQuality: 75,
		},
		MaxPerMessage: 5,
		MaxPerRequest: 10,
	}

	tests := []struct {
		name       string
		model      *Model
		wantResize *ModelImageResizeOptions
	}{
		{
			name:  "nil model returns default limits",
			model: nil,
			wantResize: &ModelImageResizeOptions{
				MaxWidth:    2000,
				MaxHeight:   2000,
				MaxBytes:    4718592,
				JPEGQuality: 80,
			},
		},
		{
			name: "model with nil InputLimits returns default limits",
			model: &Model{
				ID: "no-limits",
			},
			wantResize: &ModelImageResizeOptions{
				MaxWidth:    2000,
				MaxHeight:   2000,
				MaxBytes:    4718592,
				JPEGQuality: 80,
			},
		},
		{
			name: "model with nil Images in InputLimits returns default limits",
			model: &Model{
				ID: "nil-images",
				InputLimits: &ModelInputLimits{
					MaxRequestBytes: 10000000,
				},
			},
			wantResize: &ModelImageResizeOptions{
				MaxWidth:    2000,
				MaxHeight:   2000,
				MaxBytes:    4718592,
				JPEGQuality: 80,
			},
		},
		{
			name: "model with custom limits returns configured limits",
			model: &Model{
				ID: "custom-limits",
				InputLimits: &ModelInputLimits{
					Images: customLimits,
				},
			},
			wantResize: customLimits.Resize,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.model.GetImageLimits()
			require.NotNil(t, got)
			assert.Equal(t, tt.wantResize, got.Resize)
		})
	}
}
