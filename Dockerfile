ARG GO_VERSION=1.27.1
ARG GOLANGCI_LINT_VERSION=v2.13.1
ARG NODE_VERSION=26
ARG DEBIAN_VERSION=bookworm
ARG AGY_VERSION=1.2.15
ARG USER_UID=1000
ARG USER_GID=1000

# Stage 1: base
FROM debian:${DEBIAN_VERSION} AS base

ARG AGY_VERSION

# Install required dependencies
# Add sid repository for ttyd package (not yet in Debian stable/testing)
RUN echo "deb http://deb.debian.org/debian sid main" >> /etc/apt/sources.list && \
    apt update && apt install -y --no-install-recommends \
    build-essential \
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
    less && \
    rm -rf /var/lib/apt/lists/*

# Install agy
COPY docker-scripts/install-agy.sh /tmp/install-agy.sh
RUN chmod +x /tmp/install-agy.sh && /tmp/install-agy.sh --dir /bin && rm /tmp/install-agy.sh

# Install opencode
COPY docker-scripts/install-opencode.sh /tmp/install-opencode.sh
RUN chmod +x /tmp/install-opencode.sh && /tmp/install-opencode.sh --dir /bin && rm /tmp/install-opencode.sh

# Install claude (Claude Code)
COPY docker-scripts/install-claude.sh /tmp/install-claude.sh
RUN chmod +x /tmp/install-claude.sh && /tmp/install-claude.sh --dir /bin && rm /tmp/install-claude.sh

# Install lightpanda
COPY docker-scripts/install-lightpanda.sh /tmp/install-lightpanda.sh
RUN chmod +x /tmp/install-lightpanda.sh && /tmp/install-lightpanda.sh --dir /bin && rm /tmp/install-lightpanda.sh

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

# Stage 4: go-builder (dependencies and sources; shared by the per-image build stages)
FROM golang:${GO_VERSION}-alpine AS go-builder

WORKDIR /app

COPY go.work go.work.sum* ./
COPY backend/go.mod backend/go.sum* ./backend/
COPY agentwrapper/go.mod agentwrapper/go.sum* ./agentwrapper/
COPY agystatusline/go.mod agystatusline/go.sum* ./agystatusline/
COPY fakebash/go.mod fakebash/go.sum* ./fakebash/
COPY llms/go.mod llms/go.sum* ./llms/
COPY pkg/agentspec/go.mod pkg/agentspec/go.sum* ./pkg/agentspec/
COPY pkg/jev/go.mod pkg/jev/go.sum* ./pkg/jev/
COPY pkg/logger/go.mod pkg/logger/go.sum* ./pkg/logger/
COPY pkg/metrics/go.mod pkg/metrics/go.sum* ./pkg/metrics/
COPY pkg/paths/go.mod ./pkg/paths/
COPY pkg/pluginsdk/go.mod pkg/pluginsdk/go.sum* ./pkg/pluginsdk/
COPY pkg/runnerpb/go.mod pkg/runnerpb/go.sum* ./pkg/runnerpb/
COPY pkg/version/go.mod ./pkg/version/
COPY pkg/workflowspec/go.mod pkg/workflowspec/go.sum* ./pkg/workflowspec/
COPY plugins/notebook/go.mod plugins/notebook/go.sum* ./plugins/notebook/
COPY runner/go.mod runner/go.sum* ./runner/
COPY cmd/agent-validate/go.mod cmd/agent-validate/go.sum* ./cmd/agent-validate/
COPY cmd/asgard/go.mod cmd/asgard/go.sum* ./cmd/asgard/
COPY cmd/ask-user/go.mod cmd/ask-user/go.sum* ./cmd/ask-user/
COPY cmd/call-peer/go.mod cmd/call-peer/go.sum* ./cmd/call-peer/
COPY cmd/find-peer/go.mod cmd/find-peer/go.sum* ./cmd/find-peer/
COPY cmd/show-output/go.mod cmd/show-output/go.sum* ./cmd/show-output/
COPY cmd/tester/go.mod cmd/tester/go.sum* ./cmd/tester/
COPY simplest/go.mod simplest/go.sum* ./simplest/

RUN (cd backend && go mod download) && \
    (cd agentwrapper && go mod download) && \
    (cd agystatusline && go mod download) && \
    (cd fakebash && go mod download) && \
    (cd llms && go mod download) && \
    (cd pkg/agentspec && go mod download) && \
    (cd pkg/jev && go mod download) && \
    (cd pkg/logger && go mod download) && \
    (cd pkg/metrics && go mod download) && \
    (cd pkg/paths && go mod download) && \
    (cd pkg/pluginsdk && go mod download) && \
    (cd pkg/runnerpb && go mod download) && \
    (cd pkg/version && go mod download) && \
    (cd pkg/workflowspec && go mod download) && \
    (cd plugins/notebook && go mod download) && \
    (cd runner && go mod download) && \
    (cd cmd/agent-validate && go mod download) && \
    (cd cmd/asgard && go mod download) && \
    (cd cmd/ask-user && go mod download) && \
    (cd cmd/call-peer && go mod download) && \
    (cd cmd/find-peer && go mod download) && \
    (cd cmd/show-output && go mod download) && \
    (cd cmd/tester && go mod download) && \
    (cd simplest && go mod download)

COPY backend/ ./backend/
COPY agentwrapper/ ./agentwrapper/
COPY agystatusline/ ./agystatusline/
COPY fakebash/ ./fakebash/
COPY llms/ ./llms/
COPY pkg/ ./pkg/
COPY plugins/ ./plugins/
COPY cmd/ ./cmd/
COPY runner/ ./runner/
COPY simplest/ ./simplest/

# Stage 4a: go-build-backend (only what the backend image ships)
FROM go-builder AS go-build-backend

ARG ASGARD_VERSION=dev

RUN mkdir -p /app/bin && \
    for name in asgard; do \
    echo "Building $name..."; \
    go build -v -ldflags "-X github.com/AgentDrasil/asgard/pkg/version.Version=${ASGARD_VERSION}" -o "/app/bin/$name" "./cmd/$name"; \
    done

# Stage 4b: go-build-runner (only what the runner image ships)
FROM go-builder AS go-build-runner

ARG ASGARD_VERSION=dev

RUN mkdir -p /app/bin && \
    for name in ask-user call-peer find-peer show-output; do \
    echo "Building $name..."; \
    go build -v -ldflags "-X github.com/AgentDrasil/asgard/pkg/version.Version=${ASGARD_VERSION}" -o "/app/bin/$name" "./cmd/$name"; \
    done && \
    go build -v -ldflags "-X github.com/AgentDrasil/asgard/pkg/version.Version=${ASGARD_VERSION}" -o /app/bin/aw ./agentwrapper/cmd/aw && \
    go build -v -ldflags "-X github.com/AgentDrasil/asgard/pkg/version.Version=${ASGARD_VERSION}" -o /app/bin/fakebash ./fakebash/cmd/fakebash && \
    go build -v -ldflags "-X github.com/AgentDrasil/asgard/pkg/version.Version=${ASGARD_VERSION}" -o /app/bin/fakebashd ./fakebash/cmd/fakebashd && \
    go build -v -ldflags "-X github.com/AgentDrasil/asgard/pkg/version.Version=${ASGARD_VERSION}" -o /app/bin/agystatusline ./agystatusline/cmd/agystatusline && \
    go build -v -ldflags "-X github.com/AgentDrasil/asgard/pkg/version.Version=${ASGARD_VERSION}" -o /app/bin/asgard-runner ./runner/cmd/asgard-runner

# Stage 5: runner
# Runs on the machine that holds the chat directories and executes agents.
# Sandboxing uses bubblewrap, which needs unprivileged user namespaces: run
# with a seccomp profile that allows them (e.g. `--security-opt
# seccomp=unconfined`, or a profile permitting clone/unshare/mount/pivot_root)
# and, where AppArmor restricts user namespaces, `--security-opt
# apparmor=unconfined`. Typical run:
#   docker run --security-opt seccomp=unconfined \
#     -v ./certs:/certs:ro -v /srv/asgard:/srv/asgard \
#     ghcr.io/agentdrasil/asgard-runner:main \
#     -backend backend:9311
FROM base_devtool AS runner

ARG USER_UID
ARG USER_GID

RUN groupadd -g ${USER_GID} user && \
    useradd -u ${USER_UID} -g user -m -s /bin/bash user

COPY --from=go-build-runner /app/bin/* /bin/

# Fixed runner layout: /asgard (agent cache), /session-data (per-session tmp and
# session directories, logs), /certs (TLS credentials).
RUN mkdir -p /asgard /session-data /certs && chown user:user /asgard /session-data /certs

USER user
WORKDIR /home/user

# Keep claude's config (.claude.json) inside ~/.claude, which the sandbox binds
# and masks as a directory; claude rewrites the file via rename, which breaks
# single-file bind mounts. Updates happen by rebuilding the image.
ENV CLAUDE_CONFIG_DIR=/home/user/.claude
ENV DISABLE_AUTOUPDATER=1

ENTRYPOINT ["asgard-runner"]
CMD []

# Stage 6: backend (last, so a plain `docker build` produces the backend image)
# The orchestration server only. Chat directories, agent CLIs, bwrap and ttyd
# live on runners (see the runner stage); the backend talks to them over gRPC.
# git, bash and openssh-client are kept for the SSH agent setup at startup and
# artifact path checks. Without a runner, sandboxed workflow command nodes
# cannot run here (no bwrap/fakebashd); that layout is for development only.
FROM debian:${DEBIAN_VERSION}-slim AS backend

ARG USER_UID
ARG USER_GID

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    bash \
    git \
    openssh-client && \
    rm -rf /var/lib/apt/lists/*

RUN groupadd -g ${USER_GID} user && \
    useradd -u ${USER_UID} -g user -m -s /bin/bash user

COPY --from=go-build-backend /app/bin/* /bin/
COPY --from=webui-builder /webui/dist /opt/asgard/webui

# Fixed backend layout: /asgard (config, logs, data).
RUN mkdir -p /asgard && chown user:user /asgard

USER user
WORKDIR /home/user

CMD ["asgard"]
