module github.com/AgentDrasil/asgard/plugins/notebook

go 1.26.5

require (
	github.com/AgentDrasil/asgard/pkg/pluginsdk v0.0.0
	github.com/AgentDrasil/asgard/pkg/workflowspec v0.0.0
	github.com/goccy/go-yaml v1.19.2
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/robfig/cron/v3 v3.0.1 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)

replace (
	github.com/AgentDrasil/asgard/pkg/pluginsdk => ../../pkg/pluginsdk
	github.com/AgentDrasil/asgard/pkg/workflowspec => ../../pkg/workflowspec
)
