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

# Run mutation testing (gremlins) over the module. Extra flags pass through via
# ARGS, e.g. `just mutation --dry-run`. Run from the module root so gremlins
# discovers go.mod and the test suite.
mutation *ARGS:
    cd {{TOP}} && go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash {{ARGS}}

# Tidy module dependencies.
tidy:
    go mod tidy
