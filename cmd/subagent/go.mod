module github.com/AgentDrasil/asgard/cmd/subagent

go 1.27.1

require (
	github.com/AgentDrasil/asgard/pkg/paths v0.0.0
	github.com/stretchr/testify v1.12.1
)

require go.yaml.in/yaml/v3 v3.0.6 // indirect

replace github.com/AgentDrasil/asgard/pkg/paths => ../../pkg/paths
