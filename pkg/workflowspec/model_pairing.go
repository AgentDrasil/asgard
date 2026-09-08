package workflowspec

import (
	"fmt"
)

// PairTarget identifies one concrete CLI target (CLI name + model) inside a
// model pairing declaration.
type PairTarget struct {
	CLI   string `yaml:"cli"`
	Model string `yaml:"model"`
}

func (t PairTarget) String() string {
	return t.CLI + "/" + t.Model
}

// PairEntry maps one concrete actor target to an ordered reviewer fallback
// list. The reviewer list is resolved at reviewer execution time: the first
// target with usable quota wins; a pay-per-request tail terminates the
// fallback unconditionally.
type PairEntry struct {
	Actor    PairTarget   `yaml:"actor"`
	Reviewer []PairTarget `yaml:"reviewer"`
}

// ModelPairingSpec declares heterogeneous model pairing for one
// action/review group: whenever one of the Actors runs, its actually-used
// target selects the ordered reviewer candidate list the Reviewer node must
// pick from, guaranteeing user-configured model diversity between producer
// and reviewer.
type ModelPairingSpec struct {
	// ID is the unique pairing group identifier.
	ID string `yaml:"id"`
	// Actors lists the action node IDs whose produced content the reviewer
	// reviews.
	Actors []string `yaml:"actors"`
	// Reviewer is the review node ID whose model selection is governed by
	// the pairing table.
	Reviewer string `yaml:"reviewer"`
	// Pairs maps every possible actor target to an ordered reviewer list.
	Pairs []PairEntry `yaml:"pairs"`
}

// actorReviewerKey identifies one (actor node, reviewer node) combination,
// used to enforce cross-group uniqueness of pairing combinations.
type actorReviewerKey struct {
	actor    string
	reviewer string
}

// validateModelPairings performs the structural validation of the
// model_pairings section: reference integrity against nodes, uniqueness of
// groups and pair keys, and non-empty targets. Agent-dependent coverage
// checks (every actor cli-list target appearing as a pair key) live in
// ValidateModelPairingsCoverage because they need the agentspec loader.
func (d *WorkflowDefinition) validateModelPairings() error {
	if len(d.ModelPairings) == 0 {
		return nil
	}

	nodeByID := make(map[string]*NodeSpec, len(d.Nodes))
	for _, node := range d.Nodes {
		nodeByID[node.ID] = node
	}

	// Cross-group prohibition: collect every group's reviewer up front so
	// actor nodes can be checked against the reviewers of ALL groups, not
	// just their own (a reviewer must never review its own output through
	// another group's pairing).
	reviewerGroups := make(map[string]string, len(d.ModelPairings))
	for _, group := range d.ModelPairings {
		if group.Reviewer == "" {
			continue
		}
		if _, seen := reviewerGroups[group.Reviewer]; !seen {
			reviewerGroups[group.Reviewer] = group.ID
		}
	}

	groupIDs := make(map[string]bool, len(d.ModelPairings))
	reviewerOwner := make(map[string]string) // reviewer node ID -> group ID
	pairOwner := make(map[actorReviewerKey]string, len(d.ModelPairings))
	for _, group := range d.ModelPairings {
		if group.ID == "" {
			return fmt.Errorf("model_pairings: group id cannot be empty")
		}
		if groupIDs[group.ID] {
			return fmt.Errorf("model_pairings: duplicate group id %q", group.ID)
		}
		groupIDs[group.ID] = true

		if len(group.Actors) == 0 {
			return fmt.Errorf("model_pairings: group %q must list at least one actor node", group.ID)
		}
		for _, actorID := range group.Actors {
			node, ok := nodeByID[actorID]
			if !ok {
				return fmt.Errorf("model_pairings: group %q references unknown actor node %q", group.ID, actorID)
			}
			if node.Type != NodeTypeAgent {
				return fmt.Errorf("model_pairings: group %q actor node %q must be of type agent (got %q)", group.ID, actorID, node.Type)
			}
			if actorID == group.Reviewer {
				return fmt.Errorf("model_pairings: group %q reviewer node %q must not appear in its own actors", group.ID, actorID)
			}
			if owner, hit := reviewerGroups[actorID]; hit && owner != group.ID {
				return fmt.Errorf("model_pairings: node %q is the reviewer of group %q but appears as an actor in group %q (a reviewer must not act in any group)", actorID, owner, group.ID)
			}
			// A (actor, reviewer) combination may belong to only one group.
			// Checked inside this actor traversal (before the reviewer-owner
			// conflict check below) so this error is independently observable
			// when the same reviewer also spans two groups.
			key := actorReviewerKey{actor: actorID, reviewer: group.Reviewer}
			if owner, dup := pairOwner[key]; dup {
				if owner == group.ID {
					return fmt.Errorf("model_pairings: group %q lists actor node %q more than once", group.ID, actorID)
				}
				return fmt.Errorf("model_pairings: actor %q paired with reviewer %q already belongs to group %q (duplicate combination in group %q)", actorID, group.Reviewer, owner, group.ID)
			}
			pairOwner[key] = group.ID
		}

		if group.Reviewer == "" {
			return fmt.Errorf("model_pairings: group %q must declare a reviewer node", group.ID)
		}
		reviewerNode, ok := nodeByID[group.Reviewer]
		if !ok {
			return fmt.Errorf("model_pairings: group %q references unknown reviewer node %q", group.ID, group.Reviewer)
		}
		if reviewerNode.Type != NodeTypeAgent {
			return fmt.Errorf("model_pairings: group %q reviewer node %q must be of type agent (got %q)", group.ID, group.Reviewer, reviewerNode.Type)
		}
		if other, dup := reviewerOwner[group.Reviewer]; dup {
			return fmt.Errorf("model_pairings: node %q is the reviewer of both group %q and group %q (a node may review only one group)", group.Reviewer, other, group.ID)
		}
		reviewerOwner[group.Reviewer] = group.ID

		if len(group.Pairs) == 0 {
			return fmt.Errorf("model_pairings: group %q must declare at least one pair entry", group.ID)
		}
		actorKeys := make(map[PairTarget]bool, len(group.Pairs))
		for _, pair := range group.Pairs {
			if pair.Actor.CLI == "" || pair.Actor.Model == "" {
				return fmt.Errorf("model_pairings: group %q has a pair entry with an incomplete actor target (cli and model are both required)", group.ID)
			}
			if actorKeys[pair.Actor] {
				return fmt.Errorf("model_pairings: group %q has duplicate pair entries for actor target %s", group.ID, pair.Actor)
			}
			actorKeys[pair.Actor] = true

			if len(pair.Reviewer) == 0 {
				return fmt.Errorf("model_pairings: group %q pair for actor target %s has an empty reviewer list", group.ID, pair.Actor)
			}
			for _, rt := range pair.Reviewer {
				if rt.CLI == "" || rt.Model == "" {
					return fmt.Errorf("model_pairings: group %q pair for actor target %s has an incomplete reviewer target (cli and model are both required)", group.ID, pair.Actor)
				}
			}
		}
	}
	return nil
}

// ModelPairingWarnings returns non-fatal lint warnings for the
// model_pairings section, e.g. a reviewer list containing the very target it
// is paired against (self-review of the same source defeats the diversity
// goal and is usually a configuration mistake).
func (d *WorkflowDefinition) ModelPairingWarnings() []string {
	var warnings []string
	for _, group := range d.ModelPairings {
		for _, pair := range group.Pairs {
			for _, rt := range pair.Reviewer {
				if rt == pair.Actor {
					warnings = append(warnings, fmt.Sprintf(
						"model_pairings: group %q pairs actor target %s with itself in the reviewer list (same-source self-review)", group.ID, pair.Actor))
				}
			}
		}
	}
	return warnings
}

// ValidateModelPairingsCoverage checks that every pairing group covers every
// CLI target each actor node's agent may resolve to. agentCLIs maps agent ID
// to the agent's ordered cli list (typically loaded via agentspec). A missing
// actor target key would leave the reviewer without a candidate list at run
// time, so coverage gaps are load-time errors rather than runtime guesses.
// Actor nodes with an empty AgentID (dynamic/unresolved agents) are silently
// skipped: their cli list cannot be known statically, so there is nothing to
// coverage-check for them.
func (d *WorkflowDefinition) ValidateModelPairingsCoverage(agentCLIs map[string][]PairTarget) error {
	nodeByID := make(map[string]*NodeSpec, len(d.Nodes))
	for _, node := range d.Nodes {
		nodeByID[node.ID] = node
	}

	for _, group := range d.ModelPairings {
		pairKeys := make(map[PairTarget]bool, len(group.Pairs))
		for _, pair := range group.Pairs {
			pairKeys[pair.Actor] = true
		}
		for _, actorID := range group.Actors {
			node := nodeByID[actorID]
			if node == nil || node.AgentID == "" {
				continue
			}
			targets, ok := agentCLIs[node.AgentID]
			if !ok {
				return fmt.Errorf("model_pairings: group %q actor node %q references agent %q whose cli list is unknown to the validator", group.ID, actorID, node.AgentID)
			}
			for _, target := range targets {
				if !pairKeys[target] {
					return fmt.Errorf("model_pairings: group %q does not cover actor node %q target %s (agent %q cli list entry missing from pairs)", group.ID, actorID, target, node.AgentID)
				}
			}
		}
	}
	return nil
}

// PairingGroupForReviewer returns the pairing group in which nodeID is the
// reviewer, or nil when the node is not a paired reviewer.
func (d *WorkflowDefinition) PairingGroupForReviewer(nodeID string) *ModelPairingSpec {
	for _, group := range d.ModelPairings {
		if group.Reviewer == nodeID {
			return group
		}
	}
	return nil
}

// ReviewerCandidates resolves the ordered reviewer candidate list for the
// given actually-used actor target. ok is false when the target has no pair
// entry (a configuration coverage gap).
func (g *ModelPairingSpec) ReviewerCandidates(actor PairTarget) ([]PairTarget, bool) {
	for _, pair := range g.Pairs {
		if pair.Actor == actor {
			return pair.Reviewer, true
		}
	}
	return nil, false
}
