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

# Update go mod dependencies across all modules and sync workspace
update-go-deps:
    @for d in {{module_dirs}}; do \
        echo "Updating dependencies in $d..."; \
        (cd "$d" && go get -u -t ./...) || exit 1; \
    done
    @just tidy

# Update pnpm dependencies in webui
update-pnpm-deps:
    cd webui && pnpm update

# Update both Go and pnpm dependencies
update-deps: update-go-deps update-pnpm-deps

# Run golangci-lint
lint:
    golangci-lint run {{module_patterns}}
    cd webui && pnpm run lint

# Build version: UTC build time and short commit
version := `date -u +%Y-%m-%d-%H-%M-%S` + ":" + `git rev-parse --short=8 HEAD`

# Build backend and webui
build:
    mkdir -p build && go build -ldflags "-X github.com/AgentDrasil/asgard/pkg/version.Version={{version}}" -o build/ {{module_patterns}}
    cd webui && pnpm run build

# Test backend and webui
test:
    go test -v {{module_patterns}}
    cd webui && pnpm run test

# Run e2e tests
e2e-test:
    E2E_TEST=true go test -count=1 -v ./agentwrapper/...

# Install aw binary
install-aw:
    go install -ldflags "-X github.com/AgentDrasil/asgard/pkg/version.Version={{version}}" ./agentwrapper/cmd/aw

# Install agystatusline binary
install-agystatusline:
    go install -ldflags "-X github.com/AgentDrasil/asgard/pkg/version.Version={{version}}" ./agystatusline/cmd/agystatusline

# Install agent-validate binary
install-agent-validate:
    go install -ldflags "-X github.com/AgentDrasil/asgard/pkg/version.Version={{version}}" ./cmd/agent-validate

# Install asgard-init binary
install-asgard-init:
    go install -ldflags "-X github.com/AgentDrasil/asgard/pkg/version.Version={{version}}" ./cmd/asgard-init

# Compile protobuf and gRPC definitions for fakebash
compile-proto:
    PATH=$PATH:$(go env GOPATH)/bin protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative fakebash/pb/fakebash.proto

# Compile protobuf and gRPC definitions for the runner/backend protocol (uses buf; needs protoc-gen-go and protoc-gen-go-grpc in GOPATH/bin)
compile-runner-proto:
    cd pkg/runnerpb && PATH=$PATH:$(go env GOPATH)/bin GOWORK=off go run github.com/bufbuild/buf/cmd/buf@v1.50.0 generate

# Docker tag form of the version (':' is not allowed in tags)
image_version := replace(version, ":", "-")

# Build the backend Docker image
build-image-backend:
    docker build --target backend --build-arg ASGARD_VERSION={{version}} -t ghcr.io/agentdrasil/asgard-backend:main -t ghcr.io/agentdrasil/asgard-backend:{{image_version}} -f Dockerfile .

# Build the runner Docker image
build-image-runner:
    docker build --target runner --build-arg ASGARD_VERSION={{version}} -t ghcr.io/agentdrasil/asgard-runner:main -t ghcr.io/agentdrasil/asgard-runner:{{image_version}} -f Dockerfile .

# Build both Docker images
build-image: build-image-backend build-image-runner
