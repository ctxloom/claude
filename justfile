# claude — ctxloom's Claude Code agent module. Depends only on
# github.com/ctxloom/shared (resolved locally via the org go.work). Tests run on
# the host (no devcontainer, no build tags).
TOP := `git rev-parse --show-toplevel`

# Run the package tests under -race.
test *ARGS:
    go test -race {{ARGS}} {{TOP}}/...

# Vet all packages.
vet:
    go vet {{TOP}}/...

# Tidy module dependencies.
tidy:
    go mod tidy
