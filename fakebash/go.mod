module github.com/AgentDrasil/asgard/fakebash

go 1.26.5

require (
	github.com/AgentDrasil/asgard/pkg/logger v0.0.0
	github.com/rs/zerolog v1.35.1
	github.com/stretchr/testify v1.12.1
	google.golang.org/grpc v1.83.1
	google.golang.org/protobuf v1.36.12
)

replace github.com/AgentDrasil/asgard/pkg/logger => ../pkg/logger

require (
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	go.opentelemetry.io/otel v1.45.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.45.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260819154853-08b0e4226688 // indirect
)
