# List all available recipes
default:
    @just --list

# Format code with goimports
fmt:
    cd backend && goimports -w -local "github.com/AgentDrasil/asgard/backend" .
    cd webui && pnpm run fmt

# Run golangci-lint
lint:
    cd backend && golangci-lint run
    cd webui && pnpm run lint

# Build backend and webui
build:
    cd backend && go build -o build/ ./...
    cd webui && pnpm run build

# Test backend and webui
test:
    cd backend && go test -v ./...
    cd webui && pnpm run test

# Run e2e tests
e2e-test:
    cd backend && E2E_TEST=true go test -v ./...

# Install aw and agystatusline binaries
install-aw:
    cd backend && go install ./cmd/aw
    cd backend && go install ./cmd/agystatusline

# Install agent-validate binary
install-agent-validate:
    cd backend && go install ./cmd/agent-validate

# Compile protobuf and gRPC definitions for fakebash
compile-proto:
    cd backend && PATH=$PATH:$(go env GOPATH)/bin protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative lib/fakebash/pb/fakebash.proto

# Build Docker Image
build-image:
    docker build -t ghcr.io/agentdrasil/asgard:main -f Dockerfile .
