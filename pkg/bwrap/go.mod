module github.com/AgentDrasil/asgard/pkg/bwrap

go 1.27.4

require (
	github.com/AgentDrasil/asgard/agentwrapper v0.0.0
	github.com/AgentDrasil/asgard/fakebash v0.0.0
	github.com/AgentDrasil/asgard/llms v0.0.0
	github.com/AgentDrasil/asgard/pkg/agentspec v0.0.0
	github.com/AgentDrasil/asgard/pkg/paths v0.0.0
	github.com/AgentDrasil/asgard/pkg/proxy v0.0.0
	github.com/moznion/go-optional v0.13.0
	github.com/rs/zerolog v1.35.1
	github.com/stretchr/testify v1.12.1
	golang.org/x/sys v0.50.0
)

require (
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth v0.24.4 // indirect
	cloud.google.com/go/compute/metadata v0.10.0 // indirect
	github.com/AgentDrasil/asgard/pkg/jev v0.0.0 // indirect
	github.com/AgentDrasil/asgard/pkg/metrics v0.0.0 // indirect
	github.com/AgentDrasil/asgard/pkg/workflowspec v0.0.0 // indirect
	github.com/AgentDrasil/asgard/simplest v0.0.0 // indirect
	github.com/andybalholm/brotli v1.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/s2a-go v0.1.11 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.25 // indirect
	github.com/googleapis/gax-go/v2 v2.27.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/josexy/mitmproxy-go v1.2.1 // indirect
	github.com/josexy/websocket v0.0.0-20260219083038-11b2ba10886b // indirect
	github.com/klauspost/compress v1.20.4 // indirect
	github.com/mattn/go-colorable v0.1.16 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/refraction-networking/utls v1.8.2 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.72.0 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.yaml.in/yaml/v3 v3.0.6 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/image v0.48.0 // indirect
	golang.org/x/net v0.63.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	google.golang.org/api v0.316.0 // indirect
	google.golang.org/genai v1.77.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261116203732-fd2c7689a808 // indirect
	google.golang.org/grpc v1.85.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	modernc.org/libc v1.81.2 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.13.0 // indirect
	modernc.org/sqlite v1.62.0 // indirect
)

replace (
	github.com/AgentDrasil/asgard/agentwrapper => ../../agentwrapper
	github.com/AgentDrasil/asgard/fakebash => ../../fakebash
	github.com/AgentDrasil/asgard/llms => ../../llms
	github.com/AgentDrasil/asgard/pkg/agentspec => ../agentspec
	github.com/AgentDrasil/asgard/pkg/jev => ../jev
	github.com/AgentDrasil/asgard/pkg/metrics => ../metrics
	github.com/AgentDrasil/asgard/pkg/paths => ../paths
	github.com/AgentDrasil/asgard/pkg/proxy => ../proxy
	github.com/AgentDrasil/asgard/pkg/workflowspec => ../workflowspec
	github.com/AgentDrasil/asgard/simplest => ../../simplest
)
