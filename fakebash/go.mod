module github.com/AgentDrasil/asgard/fakebash

go 1.27.1

require (
	github.com/AgentDrasil/asgard/pkg/jev v0.0.0
	github.com/AgentDrasil/asgard/pkg/logger v0.0.0
	github.com/AgentDrasil/asgard/pkg/metrics v0.0.0
	github.com/AgentDrasil/asgard/pkg/paths v0.0.0
	github.com/rs/zerolog v1.35.1
	github.com/stretchr/testify v1.12.1
	google.golang.org/genai v1.77.0
	google.golang.org/grpc v1.85.0
	google.golang.org/protobuf v1.36.12
)

require (
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth v0.24.2 // indirect
	cloud.google.com/go/compute/metadata v0.10.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/s2a-go v0.1.11 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.24 // indirect
	github.com/googleapis/gax-go/v2 v2.27.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/mattn/go-colorable v0.1.16 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.72.0 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/api v0.310.0 // indirect
	google.golang.org/genproto v0.0.0-20261026165753-2ffab32bce7e // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20261026165753-2ffab32bce7e // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261026165753-2ffab32bce7e // indirect
)

replace github.com/AgentDrasil/asgard/pkg/jev => ../pkg/jev

replace github.com/AgentDrasil/asgard/pkg/logger => ../pkg/logger

replace github.com/AgentDrasil/asgard/pkg/paths => ../pkg/paths

replace github.com/AgentDrasil/asgard/pkg/metrics => ../pkg/metrics
