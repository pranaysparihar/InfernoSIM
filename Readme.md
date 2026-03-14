# InfernoSIM


InfernoSIM is not just a proxy—it's a strict deterministic chaos engineering, traffic interception, and CI/CD validation engine. Designed for modern microservices architectures, it enforces strict API contracts, breaks your local services before they break in production, and automatically discovers the performance limits of your infrastructure.

---

## Part 1: Core Guide (Features & Usage)

This section outlines how to configure, run, and utilize the core capabilities of InfernoSIM.

### Installation & Basic Setup
Ensure you have Go 1.22+ installed. Build the agent binary:
=======
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


### 1. Bounded HTTP Body Capture
InfernoSIM cleanly captures both incoming and outgoing payloads without destroying memory. It strictly bounds bodies to 256KB by default. Captured payloads are automatically Base64-encoded and fingerprinted with a SHA-256 hash.

**Usage:**
```bash
./infernosim --mode=proxy --listen=:9000 --log=events.log
```
*Any HTTP(S) traffic routed through the proxy (e.g., `https://api.github.com/`) will log deterministic fingerprints (`bodySha256`) and the `bodyTruncated` status.*

### 2. HTTPS Deep Capture (MITM Decryption)
Standard proxies treat encrypted `CONNECT` tunnels as black boxes. InfernoSIM dynamically intercepts HTTPS handshakes, spins up a dynamic local Certificate Authority (`~/.infernosim/ca/`), generates leaf certificates per-host on the fly, and exposes the decrypted interior payload for logging and replay.

**Usage:**
```bash
./infernosim --mode=proxy --listen=:9000 --https-mode=mitm --log=events_https.log
```
*Note: The client application or OS must trust the local `infernosim-ca.crt` root certificate to smoothly decrypt external domains like `https://api.stripe.com`.*

### 3. gRPC Unary Telemetry via HTTP/2
InfernoSIM utilizes native `golang.org/x/net/http2/h2c` multiplexing to peek inside gRPC streams routed over plaintext proxies. It intercepts the HTTP/2 trailers without requiring custom Protobuf decoders, providing deep visibility into your service calls.

**What it captures:**
- `grpcServiceMethod`
- `grpcStatus`

### 4. Deterministic Fault Injection (Chaos Engineering)
Test how your application behaves when downstream APIs fail, lag, or completely drop connections, all defined by deterministic PRNG states.

**Usage:**
```bash
./infernosim --mode=proxy --listen=:9000 --inject="jitter=50ms,drop=5%,reset=5%,status=503,rate=100%"
```
*For example, this forces a severe outage simulation where 100% of traffic to `https://aws.amazon.com` receives a `503 Service Unavailable` accompanied by latency spikes.*

### 5. Deterministic Replay (Contract Enforcement)
Take previously recorded traffic from production, staging, or integration tests, and replay it locally. InfernoSIM enforces strict determinism. If the target service tries to return a different status code, or if the replayed payload hash mismatches, the simulation loudly aborts (`FAIL_NON_DETERMINISTIC` or `FAIL_SLO_MISSED`).

**Usage:**
```bash
# Replay traffic natively against the original external APIs (e.g., api.stripe.com)
./infernosim replay --log=events.log --target=https://api.stripe.com

# Overwrite target to route external payloads to your local staging environment
./infernosim replay --log=events.log --target=http://localhost:8081
```

### 6. Auto-Envelope Search (Load Boundary Discovery)
Rather than manually guessing load test parameters, pass your production traffic logs into the search engine. InfernoSIM automatically extrapolates concurrency, multiplying traffic fanout until the target application begins to drop requests or break SLOs, reporting the exact maximum stable boundary.

**Usage:**
```bash
./infernosim search --log=events.log --target=https://staging-cluster.internal.com
```

### 7. Inbound Reverse-Proxy Mode
InfernoSIM doesn't just act as an outbound proxy for third-party APIs. It can be spun up as an Inbound sidecar to natively intercept, log, and manipulate traffic coming *into* your service from load balancers or gateways.

**Usage:**
```bash
./infernosim --mode=inbound --forward=http://localhost:8081 --listen=:8080 --log=inbound.log
```

### 8. Portable Cross-Team JSON Logs
Every event is entirely self-contained within flat JSON files. Because payloads are Base64 encoded and hashed directly in the log file, developers can easily share a single `events.log` file securely. Another developer can instantly reproduce the exact traffic state locally without needing database dumps.

### 9. Granular SLA Telemetry Tracking
Every single intercepted event explicitly tracks `bytesSent`, `bytesReceived`, and precise millisecond `duration`. It acts as a lightweight observability agent without needing a massive Datadog or Prometheus setup.

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

