# List all available recipes
default:
    @just --list

# Format code with goimports
fmt:
    goimports -w -local "github.com/AgentDrasil/asgard" .

# Run golangci-lint
lint:
    golangci-lint run

# Run e2e tests
e2e-test:
    E2E_TEST=true go test -v ./...

# Install aw and agystatusline binaries
install-aw:
    go install ./cmd/aw
    go install ./cmd/agystatusline

# Install agent-validate binary
install-agent-validate:
    go install ./cmd/agent-validate

# Start asgard with docker compose
start:
    docker compose down
    docker compose up -d --build

# Stop asgard docker compose
stop:
    docker compose down

# Logs of asgard docker compose
logs:
    docker compose logs asgard -f
