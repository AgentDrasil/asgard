---
name: project-update
description: >-
  Runbook for updating dependencies, toolchains, Docker base images, and GitHub
  Actions in the asgard repository. Use when the user asks to update project
  dependencies, upgrade Go/Node/pnpm/Docker/CI versions, or perform a routine
  dependency audit and bump.
---

# Project Update Runbook

Use this workflow when updating dependencies, runtime toolchains, Docker images,
or GitHub Actions across the `asgard` Go workspace (`go.work`) and `webui/`.

Never edit lockfiles (`webui/pnpm-lock.yaml`, `**/go.sum`, `go.work.sum`) by
hand.

## 1. Go Dependencies (`go.work` modules)

1. **Check current & outdated dependencies** across all workspace modules:
   ```bash
   for mod in $(go list -f '{{.Dir}}' -m); do
     echo "=== $mod ==="
     (cd "$mod" && go list -u -m -f '{{if and (not .Indirect) .Update}}{{.Path}}: {{.Version}} -> {{.Update.Version}}{{end}}' all)
   done
   ```
2. **Update Go dependencies**:
   - Routine minor/patch/compatible updates across all workspace modules:
     ```bash
     just update-go-deps
     ```
     (`just update-go-deps` runs `go get -u -t ./...` in every module directory,
     then runs `just tidy` which executes `go mod tidy` and `go work sync`.)
   - For targeted or major version bumps, run `go get <pkg>@<version>` inside
     the specific module directory, then run `just tidy`.
3. **Check for Go vulnerabilities**:
   ```bash
   for mod in $(go list -f '{{.Dir}}' -m); do
     echo "=== $mod ==="
     (cd "$mod" && go run golang.org/x/vuln/cmd/govulncheck@latest ./...)
   done
   ```
4. **Check Go toolchain version**:
   - Check the `go X.Y.Z` directive in `go.work` and module `go.mod` files.
   - Keep the Go version aligned across `go.work`, `Dockerfile` (`ARG GO_VERSION`),
     and `.github/workflows/test-on-push.yml` (`env.GO_VERSION`).

## 2. Web Dependencies (`webui/` with `pnpm`)

Always use `pnpm` (never `npm` or `yarn`).

1. **Check outdated packages**:
   ```bash
   cd webui && pnpm outdated
   ```
2. **Update dependencies**:
   - Compatible updates within `package.json` ranges:
     ```bash
     just update-pnpm-deps
     ```
   - Major/latest updates (review breaking changes first for `vue`, `vue-router`,
     `vue-i18n`, `vite`, `vitest`, `typescript`, `tailwindcss`, `daisyui`,
     `oxlint`, `oxfmt`, etc.):
     ```bash
     cd webui && pnpm update --latest <pkg>
     ```
3. **Audit production dependencies**:
   ```bash
   cd webui && pnpm audit --prod --audit-level=high
   ```
4. **Check Node & pnpm versions**:
   - `webui/package.json` (`packageManager`)
   - Keep `NODE_VERSION` and `PNPM_VERSION` aligned across `webui/package.json`,
     `Dockerfile` (`ARG NODE_VERSION` and `npm install -g pnpm@<ver>`), and
     `.github/workflows/test-on-push.yml`.

## 3. Docker Dependencies & Base Images (`Dockerfile`)

Inspect and update `Dockerfile` (which builds `base`, `base_devtool`,
`webui-builder`, `go-builder`, `runner`, and `backend`):

- Top-level build args:
  - `ARG GO_VERSION` (used for Go tarball in `base_devtool` and `FROM golang:${GO_VERSION}-alpine AS go-builder`)
  - `ARG GOLANGCI_LINT_VERSION` (used in `base_devtool`)
  - `ARG PROTOC_VERSION` (used in `base_devtool`)
  - `ARG NODE_VERSION` (used for NodeSource in `base_devtool` and `FROM node:${NODE_VERSION}-alpine AS webui-builder`)
  - `ARG DEBIAN_VERSION` (used for `FROM debian:${DEBIAN_VERSION} AS base` and `FROM debian:${DEBIAN_VERSION}-slim AS backend`)
  - `ARG AGY_VERSION`
- Pinned `pnpm@<ver>` in both `base_devtool` and `webui-builder` (keep in sync
  with `webui/package.json` `packageManager`).
- If Go modules were added or removed in `go.work`, verify the `COPY .../go.mod`
  and `go mod download` list in stage `go-builder` matches.

Build both runtime images locally to verify:

```bash
just build-image
```

## 4. GitHub Actions & CI-Installed Dependencies

Inspect all workflow files under `.github/workflows/` (`test-on-push.yml`,
`push-image.yml`, `push-base-images.yml`) and `.github/dependabot.yml`.

1. **CI-installed tool & runtime versions** (`.github/workflows/test-on-push.yml` `env:`):
   - `GO_VERSION`
   - `GOIMPORTS_VERSION` (`golang.org/x/tools/cmd/goimports@...`)
   - `GOLANGCI_LINT_VERSION`
   - `CFGCTL_VERSION` (`github.com/elmhuangyu/configs/cmd/cfgctl@...`)
   - `NODE_VERSION`
   - `PNPM_VERSION`
2. **GitHub Action versions** across `test-on-push.yml`, `push-image.yml`, and
   `push-base-images.yml`:
   - `actions/checkout`
   - `actions/setup-go`
   - `pnpm/setup`
   - `docker/login-action`
   - `docker/metadata-action`
   - `docker/build-push-action`
   - Check latest releases with `gh`:
     ```bash
     for repo in \
       actions/checkout \
       actions/setup-go \
       pnpm/setup \
       docker/login-action \
       docker/metadata-action \
       docker/build-push-action
     do
       tag=$(gh api "repos/$repo/releases/latest" --jq '.tag_name')
       echo "$repo: $tag"
     done
     ```
3. **Dependabot** (`.github/dependabot.yml`):
   - If Go modules are added/removed in `go.work`, keep the `gomod` entries in
     `.github/dependabot.yml` in sync.

## 5. Verification

Run the full local suite after all updates:

```bash
just fmt lint build test
cd webui && pnpm run fmt:check
```

If `Dockerfile` was modified, also run:

```bash
just build-image
```

Check `git status` and `git diff` to verify only expected files changed before
committing.
