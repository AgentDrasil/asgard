module github.com/AgentDrasil/asgard/pkg/agentspec

go 1.27.0

require (
	github.com/AgentDrasil/asgard/agentwrapper v0.0.0
	github.com/goccy/go-yaml v1.19.2
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/rs/zerolog v1.35.1 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/AgentDrasil/asgard/agentwrapper => ../../agentwrapper
