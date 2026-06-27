ARG GO_VERSION=1.26.4
ARG NODE_VERSION=26
ARG DEBIAN_VERSION=bookworm

# Build stage
FROM golang:${GO_VERSION}-${DEBIAN_VERSION} AS builder

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

# Runner Base stage
FROM debian:${DEBIAN_VERSION} AS runner-base

# Install required dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    bubblewrap \
    git \
    bash \
    ca-certificates \
    curl \
    wget \
    ripgrep \
    && rm -rf /var/lib/apt/lists/*

# Install agy
COPY docker-scripts/install-agy.sh /tmp/install-agy.sh
RUN chmod +x /tmp/install-agy.sh && /tmp/install-agy.sh --dir /bin && rm /tmp/install-agy.sh

# Install opencode
COPY docker-scripts/install-opencode.sh /tmp/install-opencode.sh
RUN chmod +x /tmp/install-opencode.sh && /tmp/install-opencode.sh --dir /bin && rm /tmp/install-opencode.sh

# Create group and user with UID/GID 1000
RUN groupadd -g 1000 user && \
    useradd -u 1000 -g user -m -s /bin/bash user

# Copy built binaries to /bin
COPY --from=builder /app/bin/* /bin/

# Runner stage
# This stage will install development tools (node, golang, etc.), you should change based on your requirement.
FROM runner-base AS runner

ARG GO_VERSION
ARG NODE_VERSION

# Install Go (do not reuse from builder stage)
RUN wget -q https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz && \
    tar -C /usr/local -xzf go${GO_VERSION}.linux-amd64.tar.gz && \
    rm go${GO_VERSION}.linux-amd64.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}"

# Install Node.js (v26)
RUN curl -fsSL https://deb.nodesource.com/setup_${NODE_VERSION}.x | bash - && \
    apt-get update && apt-get install -y --no-install-recommends nodejs && \
    rm -rf /var/lib/apt/lists/*

# Set default user and working directory
USER user
WORKDIR /home/user

# Run infinitely loop
CMD ["tail", "-f", "/dev/null"]

