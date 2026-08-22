# List all available recipes
default:
    @just --list

module_dirs := `go list -m -json | grep '"Dir"' | cut -d'"' -f4 | tr '\n' ' '`
module_patterns := `go list -m -json | grep '"Dir"' | cut -d'"' -f4 | sed 's/$/\/.../' | tr '\n' ' '`

# Format code with goimports
fmt:
    goimports -w -local "github.com/AgentDrasil/asgard" {{module_dirs}}
    cd webui && pnpm run fmt

# Run go mod tidy on all modules and sync workspace
tidy:
    @for d in {{module_dirs}}; do \
        echo "Tidying $d..."; \
        (cd "$d" && go mod tidy) || exit 1; \
    done
    go work sync

# Run golangci-lint
lint:
    golangci-lint run {{module_patterns}}
    cd webui && pnpm run lint

# Build backend and webui
build:
    mkdir -p build && go build -o build/ {{module_patterns}}
    cd webui && pnpm run build

# Test backend and webui
test:
    go test -v {{module_patterns}}
    cd webui && pnpm run test

# Run e2e tests
e2e-test:
    E2E_TEST=true go test -v {{module_patterns}}

# Install aw binary
install-aw:
    go install ./agentwrapper/cmd/aw

# Install agystatusline binary
install-agystatusline:
    go install ./agystatusline/cmd/agystatusline

# Install agent-validate binary
install-agent-validate:
    go install ./cmd/agent-validate

# Compile protobuf and gRPC definitions for fakebash
compile-proto:
    PATH=$PATH:$(go env GOPATH)/bin protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative fakebash/pb/fakebash.proto

# Build Docker Image
build-image:
    docker build -t ghcr.io/agentdrasil/asgard:main -f Dockerfile .
