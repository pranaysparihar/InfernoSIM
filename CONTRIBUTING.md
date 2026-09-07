# Contributing to InfernoSIM

Thanks for helping make incident replay safer and more useful. This guide keeps
changes reviewable, deterministic, and safe to run on real incident data.

## Development setup

1. Install Go 1.26.6 or a newer patched Go release.
2. Install Docker with Compose if you will run the container smoke profiles.
3. Clone the repository and build the CLI:

   ```bash
   go build -trimpath -o infernosim ./cmd/agent
   ```

4. Run an initial test pass:

   ```bash
   go test ./...
   (cd integrations/testcontainers-go && go test ./...)
   ./infernosim lint examples/replay-v3.yaml
   ```

The [README](Readme.md) documents the user-facing capture, replay, scenario,
template, gRPC, OpenAPI, report, and agent-reliability workflows. Agent changes
must also update the focused [agent guide](docs/AGENT_RELIABILITY.md).

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
(cd integrations/testcontainers-go && go test -race ./...)
(cd integrations/mcp-go && go mod tidy -diff && go vet ./... && go test -race ./...)
bash scripts/reliability-smoke.sh
git diff --check
```

For changes in the corresponding areas, also run:

```bash
# Parser and untrusted-input changes
go test ./pkg/matcher -run=^$ -fuzz=FuzzJSONPathValue -fuzztime=10s
go test ./pkg/grpcsim -run=^$ -fuzz=FuzzSplitFrames -fuzztime=10s
go test ./pkg/simtemplate -run=^$ -fuzz=FuzzTemplateValidation -fuzztime=10s
go test ./pkg/heal -run=^$ -fuzz=FuzzFlattenJSON -fuzztime=10s
go test ./pkg/message -run=^$ -fuzz=FuzzRecordValidate -fuzztime=10s
go test ./pkg/jsonpath -run=^$ -fuzz=FuzzOperations -fuzztime=10s
go test ./pkg/agentreliability -run=^$ -fuzz=FuzzEngineProcess -fuzztime=10s
go test ./pkg/mcpproxy -run=^$ -fuzz=FuzzEquivalentRequest -fuzztime=10s

# Container or example changes
scripts/compose-smoke.sh node
scripts/compose-smoke.sh go
scripts/kafka-smoke.sh
scripts/agent-smoke.sh
scripts/ollama-smoke.sh  # optional; skips when Ollama is absent

# Determinism and secret-leak baseline
go run ./cmd/benchmark --runs 100 --out /tmp/infernosim-benchmark.json
work_dir=$(mktemp -d)
go build -trimpath -o "$work_dir/agentlab" ./examples/agentlab
go run ./cmd/agentbenchmark --runs 20 \
  --agent-command "$work_dir/agentlab" \
  --out /tmp/infernosim-agent-benchmark.json
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
- Healing changes must test held-out values, protected fields, ambiguity
  rejection, deterministic output, and report redaction.
- Kafka changes must test message integrity, privacy transforms, fault-plan
  determinism, AsyncAPI failures, TLS/SASL option validation, and a real
  Redpanda broker through `scripts/kafka-smoke.sh`.
- Agent changes must preserve stable case planning, prove that selected faults
  are reached, test both defensive and intentionally unsafe controls, keep raw
  tool arguments/results out of proofs, and cover every untrusted parser with
  size/depth bounds and rejection tests.
- Provider-model sampling is not a deterministic release oracle. Record a
  sanitized envelope fixture and make it the gate; keep live Ollama/provider
  checks as explicit compatibility smokes.
- Never execute a command taken from an incident, transcript, telemetry file,
  or YAML configuration. Agent commands must remain explicit arguments after
  `--`.
- Do not add a competitor claim without a pinned, reproducible adapter and raw
  benchmark output under `benchmarks/`.

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
