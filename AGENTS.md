# AGENTS.md

## Commit Rules

- **Semantic commits** are required. Use conventional commit prefixes: `feat:`, `fix:`, `refactor:`, `test:`, `docs:`, `chore:`, `ci:`, `build:`.
- Make changes in **small, reviewable chunks**. Each commit should be a single logical unit.

## Pre-Commit Checks

The following **must pass** before every commit:

```sh
go vet ./...
go test ./...
golangci-lint run
```

Do not commit code that fails any of these checks.

## Testing

- Tests are **required** for all new code where possible.
- Place tests in `_test.go` files alongside the code they test.
- Use table-driven tests where appropriate.

## Code Style

- Follow standard Go conventions (`gofmt`, `goimports`).
- Only add comments where clarification is genuinely needed.
- Use `log/slog` for structured logging.
