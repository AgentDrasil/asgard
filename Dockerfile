ARG GO_VERSION=1.26.5
ARG GOLANGCI_LINT_VERSION=v2.12.2
ARG NODE_VERSION=26
ARG DEBIAN_VERSION=bookworm
ARG AGY_VERSION=1.1.17
ARG USER_UID=1000
ARG USER_GID=1000

# Stage 1: base
FROM debian:${DEBIAN_VERSION} AS base

# Install required dependencies
# Add sid repository for ttyd package (not yet in Debian stable/testing)
RUN echo "deb http://deb.debian.org/debian sid main" >> /etc/apt/sources.list && \
    apt update && apt install -y --no-install-recommends \
    bubblewrap \
    git \
    bash \
    ca-certificates \
    curl \
    wget \
    ripgrep \
    ttyd \
    fish \
    openssh-client \
    python3 \
    sqlite3 \
    less \
    && rm -rf /var/lib/apt/lists/*

# Install agy
COPY docker-scripts/install-agy.sh /tmp/install-agy.sh
RUN chmod +x /tmp/install-agy.sh && /tmp/install-agy.sh --dir /bin && rm /tmp/install-agy.sh

# Install opencode
COPY docker-scripts/install-opencode.sh /tmp/install-opencode.sh
RUN chmod +x /tmp/install-opencode.sh && /tmp/install-opencode.sh --dir /bin && rm /tmp/install-opencode.sh

# Stage 2: base_devtool
FROM base AS base_devtool

ARG GO_VERSION
ARG NODE_VERSION
ARG GOLANGCI_LINT_VERSION

# Install Go
RUN wget -q https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz && \
    mkdir -p /usr/lib/go && \
    tar -C /usr/lib/go --strip-components=1 -xzf go${GO_VERSION}.linux-amd64.tar.gz && \
    rm go${GO_VERSION}.linux-amd64.tar.gz && \
    ln -s /usr/lib/go/bin/go /usr/bin/go && \
    ln -s /usr/lib/go/bin/gofmt /usr/bin/gofmt
ENV PATH="/usr/lib/go/bin:${PATH}"
RUN GOBIN=/usr/bin /usr/bin/go install golang.org/x/tools/cmd/goimports@latest

RUN curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b /usr/bin ${GOLANGCI_LINT_VERSION}

# Install Node.js
RUN curl -fsSL https://deb.nodesource.com/setup_${NODE_VERSION}.x | bash - && \
    apt-get update && apt-get install -y --no-install-recommends nodejs && \
    rm -rf /var/lib/apt/lists/*

# Install pnpm (version pinned to match webui/package.json packageManager)
RUN npm install -g pnpm@11.3.0 && pnpm --version

# Install uv
COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/

# Install just
RUN curl --proto '=https' --tlsv1.2 -sSf https://just.systems/install.sh | bash -s -- --to /usr/local/bin

WORKDIR /workspace

# Run infinitely loop
CMD ["tail", "-f", "/dev/null"]

# Stage 3: webui-builder
FROM node:${NODE_VERSION}-alpine AS webui-builder

RUN npm install -g pnpm@11.3.0

WORKDIR /webui

COPY webui/package.json webui/pnpm-lock.yaml webui/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile

COPY webui/ .
RUN pnpm run build

# Stage 4: go-builder
FROM golang:${GO_VERSION}-alpine AS go-builder

WORKDIR /app

COPY go.work go.work.sum* ./
COPY backend/go.mod backend/go.sum* ./backend/
COPY agentwrapper/go.mod agentwrapper/go.sum* ./agentwrapper/
COPY agystatusline/go.mod agystatusline/go.sum* ./agystatusline/
COPY fakebash/go.mod fakebash/go.sum* ./fakebash/
COPY pkg/logger/go.mod pkg/logger/go.sum* ./pkg/logger/
COPY cmd/agent-validate/go.mod cmd/agent-validate/go.sum* ./cmd/agent-validate/
COPY cmd/asgard/go.mod cmd/asgard/go.sum* ./cmd/asgard/
COPY cmd/ask-user/go.mod cmd/ask-user/go.sum* ./cmd/ask-user/
COPY cmd/call-peer/go.mod cmd/call-peer/go.sum* ./cmd/call-peer/
COPY cmd/find-peer/go.mod cmd/find-peer/go.sum* ./cmd/find-peer/
COPY cmd/tester/go.mod cmd/tester/go.sum* ./cmd/tester/

RUN (cd backend && go mod download) && \
    (cd agentwrapper && go mod download) && \
    (cd agystatusline && go mod download) && \
    (cd fakebash && go mod download) && \
    (cd pkg/logger && go mod download) && \
    (cd cmd/agent-validate && go mod download) && \
    (cd cmd/asgard && go mod download) && \
    (cd cmd/ask-user && go mod download) && \
    (cd cmd/call-peer && go mod download) && \
    (cd cmd/find-peer && go mod download) && \
    (cd cmd/tester && go mod download)

COPY backend/ ./backend/
COPY agentwrapper/ ./agentwrapper/
COPY agystatusline/ ./agystatusline/
COPY fakebash/ ./fakebash/
COPY pkg/ ./pkg/
COPY cmd/ ./cmd/

RUN mkdir -p /app/bin && \
    for d in cmd/*; do \
    if [ -d "$d" ]; then \
    name=$(basename "$d"); \
    echo "Building $name..."; \
    go build -v -o "/app/bin/$name" "./$d"; \
    fi; \
    done && \
    go build -v -o /app/bin/aw ./agentwrapper/cmd/aw && \
    go build -v -o /app/bin/fakebash ./fakebash/cmd/fakebash && \
    go build -v -o /app/bin/fakebashd ./fakebash/cmd/fakebashd && \
    go build -v -o /app/bin/agystatusline ./agystatusline/cmd/agystatusline

# Stage 5: runner
FROM base_devtool AS runner

ARG USER_UID
ARG USER_GID

RUN groupadd -g ${USER_GID} user && \
    useradd -u ${USER_UID} -g user -m -s /bin/bash user

COPY --from=go-builder /app/bin/* /bin/
COPY --from=webui-builder /webui/dist /opt/asgard/webui

USER user
WORKDIR /home/user

CMD ["asgard"]
