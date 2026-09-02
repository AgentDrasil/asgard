module github.com/AgentDrasil/asgard/cmd/agent-validate

go 1.27.1

require (
	github.com/AgentDrasil/asgard/pkg/agentspec v0.0.0
	github.com/AgentDrasil/asgard/pkg/workflowspec v0.0.0
	github.com/goccy/go-yaml v1.19.2
)

require (
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth v0.23.1 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	github.com/AgentDrasil/asgard/agentwrapper v0.0.0 // indirect
	github.com/AgentDrasil/asgard/llms v0.0.0 // indirect
	github.com/AgentDrasil/asgard/simplest v0.0.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/s2a-go v0.1.9 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.21 // indirect
	github.com/googleapis/gax-go/v2 v2.23.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/rs/zerolog v1.35.1 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.70.0 // indirect
	go.opentelemetry.io/otel v1.45.0 // indirect
	go.opentelemetry.io/otel/metric v1.45.0 // indirect
	go.opentelemetry.io/otel/trace v1.45.0 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/api v0.293.0 // indirect
	google.golang.org/genai v1.69.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260819154853-08b0e4226688 // indirect
	google.golang.org/grpc v1.83.1 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace (
	github.com/AgentDrasil/asgard/agentwrapper => ../../agentwrapper
	github.com/AgentDrasil/asgard/llms => ../../llms
	github.com/AgentDrasil/asgard/pkg/agentspec => ../../pkg/agentspec
	github.com/AgentDrasil/asgard/pkg/workflowspec => ../../pkg/workflowspec
	github.com/AgentDrasil/asgard/simplest => ../../simplest
)
