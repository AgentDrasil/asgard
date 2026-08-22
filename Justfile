# List all available recipes
default:
    @just --list

# Format code with goimports
fmt:
    goimports -w -local "github.com/AgentDrasil/asgard" backend
    cd webui && pnpm run fmt

modules := "./backend/... ./backend/agentwrapper/... ./backend/fakebash/... ./backend/agystatusline/..."

# Run golangci-lint
lint:
    golangci-lint run {{modules}}
    cd webui && pnpm run lint

# Build backend and webui
build:
    mkdir -p build && go build -o build/ {{modules}}
    cd webui && pnpm run build

# Test backend and webui
test:
    go test -v {{modules}}
    cd webui && pnpm run test

# Run e2e tests
e2e-test:
    E2E_TEST=true go test -v {{modules}}

# Install aw binary
install-aw:
    go install ./backend/agentwrapper/cmd/aw

# Install agystatusline binary
install-agystatusline:
    go install ./backend/agystatusline/cmd/agystatusline

# Install agent-validate binary
install-agent-validate:
    go install ./backend/cmd/agent-validate

# Compile protobuf and gRPC definitions for fakebash
compile-proto:
    PATH=$PATH:$(go env GOPATH)/bin protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative backend/fakebash/pb/fakebash.proto

# Build Docker Image
build-image:
    docker build -t ghcr.io/agentdrasil/asgard:main -f Dockerfile .
