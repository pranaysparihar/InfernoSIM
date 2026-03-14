# Contributing to InfernoSIM

Thanks for contributing.

## Development Setup

1. Install Go (1.22+ recommended).
2. Clone the repo.
3. Build:

```bash
go build -o infernosim ./cmd/agent
```

4. Run tests:

```bash
go test ./...
```

## Pull Request Guidelines

- Keep changes scoped and focused.
- Add or update tests for behavior changes.
- Keep user-facing docs in `README.md` aligned with code changes.
- Use clear commit messages.

## Code Quality

Before opening a PR, ensure:

- `go test ./...` passes.
- New flags or summary fields are documented.
- No generated runtime artifacts are committed.

## Reporting Bugs

Please open a GitHub issue with:

- Reproduction steps
- Expected behavior
- Actual behavior
- Environment (OS, Go version, InfernoSIM version/commit)
- Relevant logs or replay summary snippets

## Feature Requests

Open a GitHub issue describing:

- Problem statement
- Proposed UX/CLI behavior
- Why existing flags do not cover the use case
