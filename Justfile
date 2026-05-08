# List all available recipes
default:
    @just --list

# Format code with goimports
fmt:
    goimports -w -local "github.com/AgentDrasil/asgard" .

# Run golangci-lint
lint:
    golangci-lint run
