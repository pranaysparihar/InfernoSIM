# Contributing to InfernoSIM

Thanks for helping make incident replay safer and more useful. This guide keeps
changes reviewable, deterministic, and safe to run on real incident data.

## Development setup

1. Install Go 1.25.12 or a newer Go 1.25 patch release.
2. Install Docker with Compose if you will run the container smoke profiles.
3. Clone the repository and build the CLI:

   ```bash
   go build -trimpath -o infernosim ./cmd/agent
   ```

4. Run an initial test pass:

   ```bash
   go test ./...
   ./infernosim lint examples/replay-v3.yaml
   ```

The [README](README.md) documents the user-facing capture, replay, scenario,
template, gRPC, OpenAPI, and report workflows.

## How to contribute

1. Start from an up-to-date branch and keep each pull request focused on one
   behavior or documentation change.
2. Add regression tests for every bug fix and behavior tests for new CLI,
   matcher, scenario, template, gRPC, or contract capability.
3. Use deterministic fixtures. Do not commit real credentials, private keys,
   raw production payloads, generated binaries, `dist/`, or local incident
   output.
4. Update the README and configuration examples when a user-visible option,
   safety default, generated file, or report changes.
5. Explain the user impact and validation in the pull request description.

## Quality gate

Run these commands before opening a pull request:

```bash
gofmt -w $(find . -name '*.go' -not -path './dist/*')
go mod tidy -diff
go vet ./...
go test -race ./...
git diff --check
```

For changes in the corresponding areas, also run:

```bash
# Parser and untrusted-input changes
go test ./pkg/matcher -run=^$ -fuzz=FuzzJSONPathValue -fuzztime=10s
go test ./pkg/grpcsim -run=^$ -fuzz=FuzzSplitFrames -fuzztime=10s
go test ./pkg/simtemplate -run=^$ -fuzz=FuzzTemplateValidation -fuzztime=10s

# Container or example changes
scripts/compose-smoke.sh node
scripts/compose-smoke.sh go
```

Run `go generate` only when the source schema or generator requires it, and
include the resulting generated files in the same change. For a release-sized
change, follow the maintainer checklist in [docs/RELEASING.md](docs/RELEASING.md).

## Design and safety expectations

- Preserve the default loopback binding and fail-closed replay behavior.
- Bound memory, body sizes, stream counts, template output, and untrusted input
  processing. Add tests for rejection paths as well as successful paths.
- Keep dynamic values deterministic when the same seed and request are used.
- Do not weaken redaction, tokenization, encryption, or TLS verification without
  an explicit, documented opt-in.
- Prefer standard, reviewable YAML configuration and explain any compatibility
  impact in [docs/UPGRADING.md](docs/UPGRADING.md).

## Reporting bugs and security issues

For ordinary bugs, open an issue with reproduction steps, expected and actual
behavior, OS, Go version, InfernoSIM version or commit, and a sanitized log or
replay summary.

Do not open a public issue for a security vulnerability or include secrets in
an issue. Follow [SECURITY.md](SECURITY.md) instead.

## Feature requests

Open an issue that states the user problem, proposed CLI or configuration
experience, expected safety implications, and why existing capture, replay,
scenario, or contract functionality does not solve it.
