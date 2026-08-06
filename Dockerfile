ARG GO_VERSION=1.26.5
ARG NODE_VERSION=26
ARG DEBIAN_VERSION=bookworm
ARG AGY_VERSION=1.1.9
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

# Install Go
RUN wget -q https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz && \
    mkdir -p /usr/lib/go && \
    tar -C /usr/lib/go --strip-components=1 -xzf go${GO_VERSION}.linux-amd64.tar.gz && \
    rm go${GO_VERSION}.linux-amd64.tar.gz && \
    ln -s /usr/lib/go/bin/go /usr/bin/go && \
    ln -s /usr/lib/go/bin/gofmt /usr/bin/gofmt
ENV PATH="/usr/lib/go/bin:${PATH}"

# Install Node.js
RUN curl -fsSL https://deb.nodesource.com/setup_${NODE_VERSION}.x | bash - && \
    apt-get update && apt-get install -y --no-install-recommends nodejs && \
    rm -rf /var/lib/apt/lists/*

# Install uv
COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/

# Install just
RUN curl --proto '=https' --tlsv1.2 -sSf https://just.systems/install.sh | bash -s -- --to /usr/local/bin

WORKDIR /workspace

# Run infinitely loop
CMD ["tail", "-f", "/dev/null"]

# Stage 3: webui-builder
FROM node:${NODE_VERSION}-alpine AS webui-builder

WORKDIR /webui

COPY webui/package*.json ./
RUN npm ci

COPY webui/ .
RUN npm run build

# Stage 4: go-builder
FROM golang:${GO_VERSION}-alpine AS go-builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN mkdir -p /app/bin && \
    for d in cmd/*; do \
    if [ -d "$d" ]; then \
    name=$(basename "$d"); \
    echo "Building $name..."; \
    go build -v -o "/app/bin/$name" "./$d"; \
    fi; \
    done

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
