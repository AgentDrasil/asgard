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
