package agents

type Agent struct {
	Config AgentConfig
	Path   string // Absolute path to the agent directory

	// WorkflowPath is the absolute path to workflow.yaml for agents of type
	// "workflow"; empty for regular agents.
	WorkflowPath string
}
