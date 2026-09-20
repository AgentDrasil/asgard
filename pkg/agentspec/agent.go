package agentspec

import (
	"github.com/AgentDrasil/asgard/pkg/workflowspec"
)

type Agent struct {
	Config AgentConfig
	Path   string // Absolute path to the agent directory

	// WorkflowPath is the absolute path to workflow.yaml for agents of type
	// "workflow"; empty for regular agents.
	WorkflowPath string
}

// ToWorkflowAgentInfo converts an Agent to workflowspec.AgentInfo for workflow validation.
func (a *Agent) ToWorkflowAgentInfo() *workflowspec.AgentInfo {
	if a == nil {
		return nil
	}
	targets := make([]workflowspec.AgentCLITarget, 0, len(a.Config.CLI))
	for _, t := range a.Config.CLI {
		targets = append(targets, workflowspec.AgentCLITarget{
			CLI:   t.CLI,
			Model: t.Model,
		})
	}
	return &workflowspec.AgentInfo{
		ID:   a.Config.ID,
		CLIs: targets,
	}
}

// AgentsToWorkflowAgentInfos converts a slice of Agents to []*workflowspec.AgentInfo.
func AgentsToWorkflowAgentInfos(agents []*Agent) []*workflowspec.AgentInfo {
	res := make([]*workflowspec.AgentInfo, 0, len(agents))
	for _, a := range agents {
		if a != nil {
			res = append(res, a.ToWorkflowAgentInfo())
		}
	}
	return res
}
