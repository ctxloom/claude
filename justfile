# claude - ctxloom family library. No binary; the immutable git tag (driven by VERSION) is the reference.
# Library: no binary is produced; the immutable git tag (driven by VERSION) is
# the reference consumers pin (github.com/ctxloom/claude@v0.7.0-pre1 / @v0.7).
#
# Tooling mirrors the ctxloom family: plain-go targets run on the host; the
# tool-dependent targets (lint, mutation) run inside the pre-baked devcontainer
# so the pinned toolchain — not a host install or a go.mod tool tree — is used.
TOP := `git rev-parse --show-toplevel`

# Container runtime (docker or podman) and the devcontainer image tag.
container_cmd := env_var_or_default("CONTAINER_CMD", "docker")
devcontainer_image := "claude-devcontainer"

# List available recipes.
default:
    @just --list

# Show the current version (versionator; reads VERSION + git).
show-version:
    @versionator output version

# Set the release version (the only supported way to bump). Releases are
# merge-triggered: bump here, commit VERSION, merge — CI tags it immutably.
# Example: just set-version 0.7.0
set-version version:
    versionator set {{version}}

# Compile-check all packages (library — no binary to stamp).
build:
    go build {{TOP}}/...

# Run the package tests under -race.
test *ARGS:
    go test -race {{ARGS}} {{TOP}}/...

# Vet all packages.
vet:
    go vet {{TOP}}/...

# Report any unformatted files (CI-friendly; non-zero on drift).
fmt-check:
    @test -z "$(gofmt -l .)" || { echo "unformatted:"; gofmt -l .; exit 1; }

# Format the tree in place.
fmt:
    gofmt -w .

# golangci-lint (v2; config in .golangci.yml) — runs in the devcontainer.
lint: dev-image
    just _run lint

# Mutation testing (gremlins; config in .gremlins.yaml) — runs in the devcontainer.
test-mutation *ARGS: dev-image
    just _run test-mutation {{ARGS}}

# Tidy module dependencies.
tidy:
    go mod tidy

# Full pre-commit / CI gate: format check, vet, lint, tests.
check: fmt-check vet lint test

# ===== devcontainer delegation (shared family pattern) =====

# Build the devcontainer image from .devcontainer/Dockerfile.
dev-image:
    {{container_cmd}} build -t {{devcontainer_image}}:latest -f .devcontainer/Dockerfile .

# Internal helper: run a justfile.container target inside the devcontainer.
# Overlays justfile.container as the in-container justfile (same pattern as
# ctxloom). Short-circuits to a direct invocation when already in CI/devcontainer.
_run +ARGS:
    #!/usr/bin/env bash
    if [ -n "$DEVCONTAINER" ] || [ -n "$CI" ] || [ -n "$GITHUB_ACTIONS" ]; then
        just -f justfile.container {{ARGS}}
    else
        # Skip --user under rootless docker (the daemon already maps container
        # root to the host user); rootful needs it to avoid root-owned files.
        user_flag=(--user "$(id -u):$(id -g)")
        if {{container_cmd}} info 2>/dev/null | grep -q "rootless"; then user_flag=(); fi
        # Module mode (GOWORK=off): deps resolve from go.mod pins via the mounted
        # host module cache, never from a host go.work / sibling checkouts.
        GOWORK=off go mod download
        cache_mount=()
        if [ -d "$HOME/go/pkg/mod" ]; then cache_mount=(-v "$HOME/go/pkg/mod:/tmp/gomodcache:ro"); fi
        {{container_cmd}} run --rm \
            "${user_flag[@]}" \
            "${cache_mount[@]}" \
            -e HOME=/tmp \
            -e GOMODCACHE=/tmp/gomodcache \
            -e GOCACHE=/tmp/.gocache \
            -e GOWORK=off \
            -v "$(pwd):/workspace" \
            -v "$(pwd)/justfile.container:/workspace/justfile:ro" \
            -w /workspace \
            {{devcontainer_image}}:latest \
            just {{ARGS}}
    fi
