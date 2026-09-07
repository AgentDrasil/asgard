package workflowspec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const pairingBaseNodes = `
nodes:
  - id: entry_agent
    type: agent
    agent_id: actor-a
    entry: true
  - id: helper_agent
    type: agent
    agent_id: actor-b
  - id: review_agent
    type: agent
    agent_id: reviewer
  - id: other_review
    type: agent
    agent_id: reviewer2
`

func pairingWorkflow(pairings string) string {
	return "name: pairing-test\n" + pairingBaseNodes + "model_pairings:\n" + pairings
}

const validPairing = `
  - id: code
    actors: [entry_agent, helper_agent]
    reviewer: review_agent
    pairs:
      - actor: {cli: agy, model: m1}
        reviewer:
          - {cli: opencode, model: m2}
      - actor: {cli: opencode, model: m2}
        reviewer:
          - {cli: agy, model: m1}
`

func TestValidateModelPairings_Structural(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pairing string
		wantErr string
	}{
		{"valid", validPairing, ""},
		{
			"unknown actor node",
			"  - id: g\n    actors: [missing]\n    reviewer: review_agent\n    pairs:\n      - actor: {cli: agy, model: m1}\n        reviewer: [{cli: opencode, model: m2}]",
			"unknown actor node \"missing\"",
		},
		{
			"unknown reviewer node",
			"  - id: g\n    actors: [entry_agent]\n    reviewer: missing\n    pairs:\n      - actor: {cli: agy, model: m1}\n        reviewer: [{cli: opencode, model: m2}]",
			"unknown reviewer node \"missing\"",
		},
		{
			"reviewer in own actors",
			"  - id: g\n    actors: [entry_agent, review_agent]\n    reviewer: review_agent\n    pairs:\n      - actor: {cli: agy, model: m1}\n        reviewer: [{cli: opencode, model: m2}]",
			"must not appear in its own actors",
		},
		{
			"duplicate group id",
			validPairing + "  - id: code\n    actors: [other_review]\n    reviewer: other_review\n    pairs:\n      - actor: {cli: agy, model: m1}\n        reviewer: [{cli: opencode, model: m2}]",
			"duplicate group id",
		},
		{
			"node reviewing two groups",
			validPairing + "  - id: other\n    actors: [other_review]\n    reviewer: review_agent\n    pairs:\n      - actor: {cli: agy, model: m1}\n        reviewer: [{cli: opencode, model: m2}]",
			"reviewer of both group",
		},
		{
			"reviewer of another group acts in this group",
			validPairing + "  - id: cross\n    actors: [review_agent]\n    reviewer: other_review\n    pairs:\n      - actor: {cli: agy, model: m1}\n        reviewer: [{cli: opencode, model: m2}]",
			`is the reviewer of group "code" but appears as an actor in group "cross"`,
		},
		{
			"same (actor, reviewer) combination in two groups",
			validPairing + "  - id: dup_pair\n    actors: [entry_agent]\n    reviewer: review_agent\n    pairs:\n      - actor: {cli: agy, model: m1}\n        reviewer: [{cli: opencode, model: m2}]",
			`already belongs to group "code"`,
		},
		{
			"actors shared across groups with different reviewers is valid",
			validPairing + "  - id: shared\n    actors: [entry_agent, helper_agent]\n    reviewer: other_review\n    pairs:\n      - actor: {cli: agy, model: m1}\n        reviewer: [{cli: opencode, model: m2}]\n      - actor: {cli: opencode, model: m2}\n        reviewer: [{cli: agy, model: m1}]",
			"",
		},
		{
			"duplicate pair key",
			"  - id: g\n    actors: [entry_agent]\n    reviewer: review_agent\n    pairs:\n      - actor: {cli: agy, model: m1}\n        reviewer: [{cli: opencode, model: m2}]\n      - actor: {cli: agy, model: m1}\n        reviewer: [{cli: opencode, model: m3}]",
			"duplicate pair entries",
		},
		{
			"empty reviewer list",
			"  - id: g\n    actors: [entry_agent]\n    reviewer: review_agent\n    pairs:\n      - actor: {cli: agy, model: m1}\n        reviewer: []",
			"empty reviewer list",
		},
		{
			"incomplete target",
			"  - id: g\n    actors: [entry_agent]\n    reviewer: review_agent\n    pairs:\n      - actor: {cli: agy, model: m1}\n        reviewer: [{cli: opencode}]",
			"incomplete reviewer target",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseDefinition([]byte(pairingWorkflow(tt.pairing)))
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidateModelPairingsCoverage(t *testing.T) {
	t.Parallel()
	defn, err := ParseDefinition([]byte(pairingWorkflow(validPairing)))
	require.NoError(t, err)

	full := map[string][]PairTarget{
		"actor-a": {{CLI: "agy", Model: "m1"}, {CLI: "opencode", Model: "m2"}},
		"actor-b": {{CLI: "agy", Model: "m1"}},
	}
	require.NoError(t, defn.ValidateModelPairingsCoverage(full))

	partial := map[string][]PairTarget{
		"actor-a": {{CLI: "agy", Model: "m1"}, {CLI: "opencode", Model: "m3"}},
		"actor-b": {{CLI: "agy", Model: "m1"}},
	}
	err = defn.ValidateModelPairingsCoverage(partial)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `does not cover actor node "entry_agent" target opencode/m3`)

	err = defn.ValidateModelPairingsCoverage(map[string][]PairTarget{"actor-a": {{CLI: "agy", Model: "m1"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "whose cli list is unknown")
}

func TestModelPairingWarnings(t *testing.T) {
	t.Parallel()
	yaml := pairingWorkflow(`
  - id: g
    actors: [entry_agent]
    reviewer: review_agent
    pairs:
      - actor: {cli: agy, model: m1}
        reviewer:
          - {cli: opencode, model: m2}
          - {cli: agy, model: m1}
`)
	defn, err := ParseDefinition([]byte(yaml))
	require.NoError(t, err)
	warnings := defn.ModelPairingWarnings()
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "same-source self-review")
}

func TestReviewerCandidatesAndGroupLookup(t *testing.T) {
	t.Parallel()
	defn, err := ParseDefinition([]byte(pairingWorkflow(validPairing)))
	require.NoError(t, err)

	assert.Nil(t, defn.PairingGroupForReviewer("entry_agent"))
	group := defn.PairingGroupForReviewer("review_agent")
	require.NotNil(t, group)
	assert.Equal(t, "code", group.ID)

	cands, ok := group.ReviewerCandidates(PairTarget{CLI: "agy", Model: "m1"})
	require.True(t, ok)
	assert.Equal(t, []PairTarget{{CLI: "opencode", Model: "m2"}}, cands)

	_, ok = group.ReviewerCandidates(PairTarget{CLI: "agy", Model: "unknown"})
	assert.False(t, ok)
}
