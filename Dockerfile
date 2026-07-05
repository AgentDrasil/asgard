ARG GO_VERSION=1.26.4
ARG USER_UID=1000
ARG USER_GID=1000

# Build stage
FROM golang:${GO_VERSION}-alpine AS builder

WORKDIR /app

# Copy dependency files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build all commands in cmd/
RUN mkdir -p /app/bin && \
    for d in cmd/*; do \
    if [ -d "$d" ]; then \
    name=$(basename "$d"); \
    echo "Building $name..."; \
    go build -v -o "/app/bin/$name" "./$d"; \
    fi; \
    done

# Runner stage
FROM ghcr.io/agentdrasil/asgard-base-devtool:latest AS runner

ARG USER_UID
ARG USER_GID

# Create group and user
RUN groupadd -g ${USER_GID} user && \
    useradd -u ${USER_UID} -g user -m -s /bin/bash user

# Copy built binaries to /bin
COPY --from=builder /app/bin/* /bin/

# Set default user and working directory
USER user
WORKDIR /home/user

# Run infinitely loop
CMD ["tail", "-f", "/dev/null"]

