package api

import "github.com/AgentDrasil/asgard/pkg/agentspec"

// worktreeOptInError explains why agent cannot run a chat in a git worktree,
// or returns "" when it can. Whether the chosen directory is a git checkout is
// known to the runner only, which refuses the first run otherwise.
func worktreeOptInError(agent *agentspec.Agent) string {
	switch {
	case agent == nil:
		return "agent not found"
	case agent.Config.Type == "workflow":
		return "worktrees are not supported for workflow agents"
	case !agent.Config.Worktree.Enabled:
		return "this agent does not offer git worktrees"
	}
	return ""
}
