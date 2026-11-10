package api

import "github.com/AgentDrasil/asgard/pkg/agentspec"

// worktreeSupportError explains why chats with agent cannot use git
// worktrees at all, or returns "".
func (s *Server) worktreeSupportError(agent *agentspec.Agent) string {
	switch {
	case agent == nil:
		return "agent not found"
	case agent.Config.Type == "workflow":
		return "worktrees are not supported for workflow agents"
	case !agent.Config.Worktree.Enabled:
		return "this agent does not offer git worktrees"
	case s.runnerClient == nil:
		// Only a configured runner creates worktrees; runs without one are
		// in-process or rejected.
		return "git worktrees need a runner"
	}
	return ""
}

// worktreeOptInError explains why a chat with agent cannot opt in to a git
// worktree of runDir (what the request names, possibly empty), or returns "".
// Whether the directory is a git checkout is known to the runner only, which
// refuses the first run otherwise.
func (s *Server) worktreeOptInError(agent *agentspec.Agent, runDir string) string {
	if msg := s.worktreeSupportError(agent); msg != "" {
		return msg
	}
	if runDir == "" && agent.Config.DefaultRunDir() == "" {
		return "choose a run directory: the agent's default is a pattern, and a worktree needs one concrete directory"
	}
	return ""
}
