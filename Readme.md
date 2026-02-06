# InfernoSIM

InfernoSIM is an open-source sidecar-style traffic capture and deterministic replay tool for backend services.

It captures inbound and outbound HTTP traffic as JSONL logs, then replays real incidents against a live service with deterministic timing controls, fault injection, and concurrency pressure.

## Features

- Inbound + outbound capture via lightweight proxies.
- Deterministic replay across repeated runs.
- Causal concurrency pressure with `--fanout`.
- Replay SLO checks with `--window`.
- Fault injection (`latency`, `timeout`) for dependencies.
- Actionable replay summary: limiting factor, sustained envelope, delta from last run.

## Repository Layout

- `cmd/agent/main.go`: InfernoSIM binary entrypoint.
- `pkg/`: capture/replay/stub/injection/event internals.
- `examples/goapp-deterministic`: deterministic Go sample app.
- `examples/nodeapp-deterministic`: deterministic Node sample app.

## Build

```bash
go build -o infernosim ./cmd/agent
```

## Deterministic Compose Smoke Test

Use the built-in smoke workflow for runtime validation without ad-hoc package installs:

```bash
scripts/compose-smoke.sh node
scripts/compose-smoke.sh go
```

What it validates:

- `infernosim:local` image builds.
- Runtime-agnostic capture sidecar (`docker-compose.yml`) boots cleanly.
- Runtime example profile (`docker-compose.examples.yml`) boots cleanly.
- Capture healthcheck passes.
- A live request is executed.
- Runtime incident log contains captured `OutboundCall` events.

Compose files:

- `docker-compose.yml`: capture sidecar only (runtime-agnostic).
- `docker-compose.examples.yml`: optional deterministic Go/Node example services via profiles.

## Capture Modes

### Inbound capture

```bash
./infernosim --mode=inbound --listen=:18080 --forward=localhost:8084 --log=inbound.log
```

### Outbound capture

```bash
./infernosim --mode=proxy --listen=:9000 --log=outbound.log
```

## Quickstart: Deterministic Go

### Terminal 1

```bash
./infernosim --mode=proxy --listen=:9000 --log=outbound.log
```

### Terminal 2

```bash
HTTP_PROXY=http://localhost:9000 \
HTTPS_PROXY=http://localhost:9000 \
PORT=8084 \
go run examples/goapp-deterministic/main.go
```

### Terminal 3

```bash
./infernosim --mode=inbound --listen=:18080 --forward=localhost:8084 --log=inbound.log
```

### Generate + replay

```bash
curl http://localhost:18080/api/demo

# stop capture sidecars first (:9000 and :18080), keep app running
./infernosim replay --incident . --target-base http://localhost:8084 --runs 3
```

## Quickstart: Deterministic Node

### Terminal 1

```bash
./infernosim --mode=proxy --listen=:9000 --log=outbound.log
```

### Terminal 2

```bash
PORT=8083 OUTBOUND_PROXY_PORT=9000 node examples/nodeapp-deterministic/app.js
```

### Terminal 3

```bash
./infernosim --mode=inbound --listen=:18080 --forward=localhost:8083 --log=inbound.log
```

### Generate + replay

```bash
curl http://localhost:18080/api/demo
./infernosim replay --incident . --target-base http://localhost:8083 --runs 3
```

## Replay Command

```bash
./infernosim replay --incident . --target-base http://localhost:8084 --runs 10
```

## Replay Flags

- `--incident` (default `.`): path containing `inbound.log` and `outbound.log`.
- `--target-base` (default `http://localhost:18080`): replay target URL base.
- `--runs` (default `10`): number of replay iterations.
- `--time-scale` (default `1.0`): replay time scaling.
- `--density` (default `1.0`): replay density multiplier.
- `--min-gap` (default `2ms`): minimum inter-event replay gap.
- `--max-wall-time` (default `30s`): max replay wall-clock budget.
- `--max-idle-time` (default `5s`): max idle duration without progress.
- `--max-events` (default `0`): max inbound events replayed (`0` = unlimited).
- `--inject` (repeatable): injection rule.
  - Example: `--inject "dep=worldtimeapi.org latency=+200ms"`
  - Example: `--inject "dep=worldtimeapi.org timeout=1s"`
- `--stub-listen` (default `:19000`): primary replay stub listen address.
- `--stub-compat-listen` (default `:9000`): compatibility listen address for fixed app proxy ports.
- `--fanout` (default `1`): concurrent causal replay workers per run.
- `--window` (default `0s`): replay SLO window; can trigger `FAIL_SLO_MISSED`.

## Example Replay Scenarios

```bash
# baseline
./infernosim replay --incident . --target-base http://localhost:8084 --runs 10

# faster replay
./infernosim replay --incident . --target-base http://localhost:8084 --runs 5 --time-scale 0.1

# dependency fault injection
./infernosim replay --incident . --target-base http://localhost:8084 --runs 5 --inject "dep=worldtimeapi.org latency=+200ms"

# causal concurrency + SLO window
./infernosim replay --incident . --target-base http://localhost:8084 --runs 5 --fanout 167 --window 5m
```

## Summary Semantics

Replay summary includes:

- `Outcome` and `Primary failure reason`.
- Inbound/outbound observed vs expected/target counts.
- Achieved vs target request rate.
- `Limiting factor` classification.
- `SUSTAINABLE ENVELOPE (observed)`.
- `Change from last run` deltas.

## Production Notes

- Keep incident logs immutable between analysis runs.
- Ensure replay stub ports are free (`:19000` and optional compat `:9000`).
- Stop capture sidecars before running replay.
- Commit source only; runtime artifacts are ignored by `.gitignore`.

## License

Add an OSS license file (`LICENSE`) before public release.
