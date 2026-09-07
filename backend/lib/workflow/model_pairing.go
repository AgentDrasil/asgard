package workflow

import (
	"fmt"
	"time"

	"github.com/AgentDrasil/asgard/pkg/agentspec"
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

// recordedPairingTarget is the RunValues payload remembering the CLI target a
// node execution actually used, so a paired reviewer can resolve its candidate
// list per iteration (loop re-entries of the actors supersede earlier records).
type recordedPairingTarget struct {
	CLI   string
	Model string
	At    time.Time
}

// pairingTargetKey is the RunValues key under which the actual target of a
// node execution is recorded.
func pairingTargetKey(nodeID string) string {
	return "model_pairing:target:" + nodeID
}

// recordActualTarget remembers the CLI target a node execution actually used.
func recordActualTarget(nctx *NodeContext, nodeID string, cli, model string) {
	if nctx == nil || nctx.Values == nil || cli == "" || model == "" {
		return
	}
	nctx.Values.Set(pairingTargetKey(nodeID), recordedPairingTarget{CLI: cli, Model: model, At: time.Now()})
}

// latestActorTarget returns the actually-used target of the group's most
// recently completed actor execution. Live runs read the per-run RunValues
// records (timestamp-ordered); after a restart from a persisted snapshot the
// upstream node results serve as fallback (ambiguity resolves in favor of the
// actor listed last, matching the loop re-entry convention).
func latestActorTarget(group *workflowspec.ModelPairingSpec, nctx *NodeContext) (workflowspec.PairTarget, string, bool) {
	var best recordedPairingTarget
	var bestNode string
	found := false
	for _, actorID := range group.Actors {
		v, ok := nctx.Values.Get(pairingTargetKey(actorID))
		if !ok {
			continue
		}
		rec, ok := v.(recordedPairingTarget)
		if !ok || rec.CLI == "" || rec.Model == "" {
			continue
		}
		if !found || !rec.At.Before(best.At) {
			best = rec
			bestNode = actorID
			found = true
		}
	}
	if found {
		return workflowspec.PairTarget{CLI: best.CLI, Model: best.Model}, bestNode, true
	}

	// Restart fallback: upstream settled results carry the recorded target.
	// Iterating in reverse resolves multi-actor ambiguity in favor of the
	// actor listed last in group.Actors, matching the doc comment above and
	// the RunValues tie-break. This branch serves both crash recovery and
	// quota-suspension re-drives (each Execute rebuilds fresh RunValues).
	for i := len(group.Actors) - 1; i >= 0; i-- {
		actorID := group.Actors[i]
		res, ok := nctx.Upstreams[actorID]
		if !ok || res == nil || res.CLI == "" || res.Model == "" {
			continue
		}
		if res.Status == workflowspec.StatusSucceeded {
			return workflowspec.PairTarget{CLI: res.CLI, Model: res.Model}, actorID, true
		}
	}
	return workflowspec.PairTarget{}, "", false
}

// pairingPlan captures the resolved reviewer-side model selection for one
// reviewer node execution.
type pairingPlan struct {
	GroupID     string
	ActorNodeID string
	ActorTarget workflowspec.PairTarget
	Candidates  []agentspec.CLITarget
}

// emitModelSelection reports observable model-selection deviations: an actor
// falling back from its preferred cli-list head, or a reviewer skipping the
// first candidate of its pairing entry. The event rides the regular
// node_status_update → SSE pipeline; Metadata carries the actually-used
// (cli, model) for UI rendering.
//
// An explicit node-level `model:` or a model the user forced through a quota
// suspension (userForced) is a user takeover, not a degradation: outside a
// pairing plan it is never reported as "Model fallback". The pairing path
// (including a forced model matched against the pairing candidates) keeps its
// accurate pairing-fallback wording.
func emitModelSelection(nctx *NodeContext, node *workflowspec.NodeSpec, agent *agentspec.Agent, target agentspec.CLITarget, pairing *pairingPlan, userForced bool) {
	if nctx == nil || nctx.EventEmitter == nil {
		return
	}
	if pairing == nil && (node.Model != "" || userForced) {
		return
	}
	var first agentspec.CLITarget
	switch {
	case pairing != nil && len(pairing.Candidates) > 0:
		first = pairing.Candidates[0]
	case agent != nil && len(agent.Config.CLI) > 0:
		first = agent.Config.CLI[0]
	default:
		return
	}
	if target == first {
		return
	}

	var message string
	if pairing != nil {
		message = fmt.Sprintf("Model pairing fallback: %s skipped paired first choice %s/%s and is using %s/%s (pairing group %q, actor %s used %s).",
			node.ID, first.CLI, first.Model, target.CLI, target.Model, pairing.GroupID, pairing.ActorNodeID, pairing.ActorTarget)
	} else {
		message = fmt.Sprintf("Model fallback: %s is using %s/%s instead of preferred %s/%s.",
			node.ID, target.CLI, target.Model, first.CLI, first.Model)
	}

	nctx.EventEmitter(WorkflowEvent{
		Type:      EventNodeStatusUpdate,
		NodeID:    node.ID,
		NodeType:  workflowspec.NodeTypeAgent,
		AgentID:   node.AgentID,
		AgentName: agent.Config.Name,
		Status:    workflowspec.StatusRunning,
		Message:   message,
		EntryType: "activity",
		Metadata:  map[string]any{"cli": target.CLI, "model": target.Model},
	})
}

// resolvePairingPlan determines the ordered reviewer candidate list for a
// node governed by a model_pairings group. It returns nil when the node is
// not a paired reviewer or the user explicitly took over model selection
// (node-level `model:`), and an error when a pairing must hold but cannot be
// resolved (fail-closed per the pairing semantics).
func resolvePairingPlan(nctx *NodeContext, node *workflowspec.NodeSpec) (*pairingPlan, error) {
	if nctx == nil || nctx.Defn == nil || node == nil {
		return nil, nil
	}
	group := nctx.Defn.PairingGroupForReviewer(node.ID)
	if group == nil {
		return nil, nil
	}
	// Explicit node-level model selection means the user takes over: no
	// pairing lookup, no fallback (existing semantics preserved).
	if node.Model != "" {
		return nil, nil
	}

	actorTarget, actorNode, ok := latestActorTarget(group, nctx)
	if !ok {
		return nil, fmt.Errorf("model pairing group %q has no completed actor execution to pair against (actors: %v)", group.ID, group.Actors)
	}
	candidates, ok := group.ReviewerCandidates(actorTarget)
	if !ok {
		return nil, fmt.Errorf("pairing key not found: %s", actorTarget)
	}

	cliTargets := make([]agentspec.CLITarget, 0, len(candidates))
	for _, t := range candidates {
		cliTargets = append(cliTargets, agentspec.CLITarget{CLI: t.CLI, Model: t.Model})
	}
	return &pairingPlan{
		GroupID:     group.ID,
		ActorNodeID: actorNode,
		ActorTarget: actorTarget,
		Candidates:  cliTargets,
	}, nil
}
