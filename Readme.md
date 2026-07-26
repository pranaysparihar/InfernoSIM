# InfernoSIM

InfernoSIM is an open-source Go sidecar for capturing HTTP traffic and replaying
incidents against an isolated service. It supports inbound request capture,
outbound dependency stubbing, state-aware replay, fault injection, replay diffs,
and concurrency pressure.

## Requirements

- Go 1.25.12 or newer in the 1.25 line
- Docker with Compose for the container examples
- Linux with `NET_ADMIN` only for optional transparent replay

## Build and test

```bash
go build -trimpath -o infernosim ./cmd/agent
go test -race ./...
go vet ./...
```

## Safety defaults

- Listeners bind to loopback unless another address is explicitly supplied.
- Replay skips POST, PUT, PATCH, and DELETE by default.
- Legacy replay rewrites traffic to `--target`; it never intentionally calls the
  captured origin.
- Capture redacts authorization, cookie, API-key, and set-cookie values.
- Raw bodies are fingerprinted but omitted unless `--capture-sensitive-data` is
  explicitly supplied.
- Incident logs and metadata use owner-only file permissions.
- Forward proxies block loopback, link-local, and private destinations unless
  `--allow-private-destinations` is supplied.

Do not use `--allow-writes`, `--capture-sensitive-data`,
`--allow-private-destinations`, or `--insecure-upstream` without understanding
their scope.

## Capture an incident

Start the application, then run:

```bash
./infernosim capture \
  --listen 127.0.0.1:18080 \
  --forward 127.0.0.1:8081 \
  --out ./incident-001
```

This starts an inbound reverse proxy at `127.0.0.1:18080` and an outbound
forward proxy at `127.0.0.1:8084`. Configure the application process to use the
outbound proxy:

```bash
HTTP_PROXY=http://127.0.0.1:8084 \
HTTPS_PROXY=http://127.0.0.1:8084 \
./your-application
```

Exercise the application through the inbound proxy, then press Ctrl-C. The
incident directory contains `incident.json`, `inbound.log`, and `outbound.log`.

For a local private dependency and replayable payload bytes:

```bash
./infernosim capture \
  --listen 127.0.0.1:18080 \
  --forward 127.0.0.1:8081 \
  --outbound-listen 127.0.0.1:8084 \
  --allow-private-destinations \
  --capture-sensitive-data \
  --out ./incident-001
```

`--capture-sensitive-data` can store credentials and PII. Treat such incident
bundles as secrets. Existing non-empty bundles are rejected unless `--append`
is explicitly supplied.

## Inspect and verify

```bash
./infernosim inspect ./incident-001
./infernosim verify ./incident-001
```

`inspect` prints the request timeline and discovered state chains. `verify`
reports side effects, missing dependencies, and expired JWTs.

## Replay

Safe replay:

```bash
./infernosim replay ./incident-001 \
  --target-base http://127.0.0.1:8081 \
  --runs 3
```

The positional incident directory may appear before the flags. Write requests
are skipped unless explicitly enabled:

```bash
./infernosim replay ./incident-001 \
  --target-base http://127.0.0.1:8081 \
  --allow-writes \
  --runs 3
```

Only use `--allow-writes` with an isolated target whose data may be changed.
Inbound-only incidents are supported and produce a weak pass because dependency
behavior was not verified.

### Replay flags

- `--target-base`: isolated service receiving captured inbound requests
- `--runs`: number of replay iterations
- `--time-scale`: scale captured timing
- `--density`: compress request gaps
- `--min-gap`: minimum gap between requests
- `--max-wall-time`: total replay budget
- `--max-idle-time`: per-request progress budget
- `--max-events`: maximum inbound events
- `--fanout`: concurrent replay workers
- `--window`: optional SLO completion window
- `--inject`: dependency latency/timeout/retry rule
- `--diff`: show status, stable-header, body-hash, and latency changes
- `--safe-mode`: skip writes; enabled by default
- `--allow-writes`: explicitly disable safe mode
- `--stub-listen`: primary dependency stub address
- `--stub-compat-listen`: optional compatibility stub address

Examples:

```bash
./infernosim replay ./incident-001 \
  --target-base http://127.0.0.1:8081 \
  --inject "dep=payments.test latency=+200ms"

./infernosim replay ./incident-001 \
  --target-base http://127.0.0.1:8081 \
  --fanout 8 \
  --window 30s
```

## Replay configuration

An incident may contain `replay.yaml`:

```yaml
target: http://127.0.0.1:8081
runs: 3
time_scale: 1.0
safe_mode: true
chaos:
  latency:
    request: 2
    delay: 250ms
state:
  file: ./state.json
```

Unknown fields and invalid values are rejected. Relative state paths are
resolved relative to `replay.yaml`.

## Standalone capture proxy

Inbound:

```bash
./infernosim --mode=inbound \
  --listen 127.0.0.1:18080 \
  --forward 127.0.0.1:8081 \
  --log inbound.log
```

Outbound with repeatable fault injection:

```bash
./infernosim --mode=proxy \
  --listen 127.0.0.1:9000 \
  --log outbound.log \
  --inject "jitter=50ms,drop=5%,status=503,rate=10%" \
  --inject-seed 42
```

## HTTPS MITM

MITM capture is opt-in:

```bash
./infernosim --mode=proxy \
  --listen 127.0.0.1:9000 \
  --https-mode mitm \
  --mitm-allow-hosts api.example.test \
  --log outbound.log
```

The generated CA is stored under `~/.infernosim/ca`. The client must trust that
CA. Only explicitly allowlisted hosts receive leaf certificates. Local/private
MITM additionally requires `--allow-private-destinations`.

HTTPS dependency replay still requires the application to support an HTTP test
endpoint or another TLS termination layer in front of the stub. Native
CONNECT/TLS response stubbing is not yet implemented.

## Docker examples

```bash
scripts/compose-smoke.sh node
scripts/compose-smoke.sh go
```

The default capture container is unprivileged. Transparent replay requires an
explicit root user and `NET_ADMIN`; do not grant those permissions to ordinary
capture deployments.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md), [SCENARIOS.md](SCENARIOS.md), and
[SECURITY.md](SECURITY.md).

## License

MIT. See [LICENSE](LICENSE).
