module github.com/AgentDrasil/asgard/cmd/ask-user

go 1.27.1

require (
	github.com/AgentDrasil/asgard/pkg/logger v0.0.0
	github.com/rs/zerolog v1.35.1
)

require (
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/AgentDrasil/asgard/pkg/logger => ../../pkg/logger
