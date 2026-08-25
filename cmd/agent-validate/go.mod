module github.com/AgentDrasil/asgard/cmd/agent-validate

go 1.27.0

require (
	github.com/AgentDrasil/asgard/pkg/agentspec v0.0.0
	github.com/AgentDrasil/asgard/pkg/workflowspec v0.0.0
	github.com/goccy/go-yaml v1.19.2
)

require (
	github.com/AgentDrasil/asgard/agentwrapper v0.0.0 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/rs/zerolog v1.35.1 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace (
	github.com/AgentDrasil/asgard/agentwrapper => ../../agentwrapper
	github.com/AgentDrasil/asgard/pkg/agentspec => ../../pkg/agentspec
	github.com/AgentDrasil/asgard/pkg/workflowspec => ../../pkg/workflowspec
)
