module github.com/AgentDrasil/asgard/backend

go 1.26.5

require github.com/goccy/go-yaml v1.19.2

require (
	github.com/AgentDrasil/asgard/agentwrapper v0.0.0
	github.com/AgentDrasil/asgard/fakebash v0.0.0
	github.com/AgentDrasil/asgard/pkg/agentspec v0.0.0
	github.com/AgentDrasil/asgard/pkg/workflowspec v0.0.0
	github.com/creack/pty v1.1.24
	github.com/go-co-op/gocron/v2 v2.22.0
	github.com/google/uuid v1.6.0
	github.com/libtnb/sqlite v1.2.2
	github.com/moznion/go-optional v0.13.0
	github.com/rcarmo/go-te v0.1.0
	github.com/rs/zerolog v1.35.1
	github.com/stretchr/testify v1.12.1
	golang.org/x/oauth2 v0.36.0
	golang.org/x/sync v0.22.0
	google.golang.org/genai v1.69.0
	google.golang.org/grpc v1.83.1
	gorm.io/driver/postgres v1.6.2
	gorm.io/gorm v1.31.2
)

replace (
	github.com/AgentDrasil/asgard/agentwrapper => ../agentwrapper
	github.com/AgentDrasil/asgard/fakebash => ../fakebash
	github.com/AgentDrasil/asgard/pkg/agentspec => ../pkg/agentspec
	github.com/AgentDrasil/asgard/pkg/logger => ../pkg/logger
	github.com/AgentDrasil/asgard/pkg/workflowspec => ../pkg/workflowspec
)

require (
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth v0.23.1 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/s2a-go v0.1.9 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.21 // indirect
	github.com/googleapis/gax-go/v2 v2.23.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/jonboulle/clockwork v0.5.0 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-runewidth v0.0.28 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.70.0 // indirect
	go.opentelemetry.io/otel v1.45.0 // indirect
	go.opentelemetry.io/otel/metric v1.45.0 // indirect
	go.opentelemetry.io/otel/trace v1.45.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/api v0.293.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260819154853-08b0e4226688 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	modernc.org/libc v1.75.3 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.0 // indirect
	modernc.org/sqlite v1.57.0 // indirect
)
