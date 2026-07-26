# InfernoSIM

InfernoSIM is an open-source Go sidecar for capturing HTTP traffic and replaying
incidents against an isolated service. It supports inbound request capture,
outbound dependency stubbing, state-aware replay, fault injection, replay diffs,
concurrency pressure, native HTTPS dependency stubbing, semantic request
matching, explicit scenarios, OpenAPI release gates, CI reports, encrypted
incident bundles, and policy-driven privacy controls.

## Feature guide

| Area | Features |
| --- | --- |
| Capture | Inbound reverse proxy, outbound HTTP/HTTPS MITM proxy, HTTP/2 and gRPC exchanges including trailers, bounded payload capture |
| Replay | Timing preservation, density, fanout, safe mode, runtime state substitution, dependency fault injection |
| Virtualization | Captured HTTP/HTTPS and gRPC responses over HTTP/2/h2c, regex/JSONPath/header matching, ignored volatile fields, stateful scenarios |
| Release gates | OpenAPI 3.x request/response validation, status/content-type drift, JUnit, SARIF, and HTML reports |
| Privacy | Built-in secret redaction, configurable redact/drop/tokenize rules, deterministic HMAC tokens |
| Portability | Authenticated encrypted v2 bundles using AES-256-GCM and PBKDF2-HMAC-SHA256 |

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
  explicitly supplied or a privacy policy explicitly enables transformed body
  capture.
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
- `--openapi`: validate captured and replayed exchanges against OpenAPI 3.x
- `--report-formats`: generate `junit`, `sarif`, and/or `html` reports
- `--report-dir`: choose the report output directory
- `--safe-mode`: skip writes; enabled by default
- `--allow-writes`: explicitly disable safe mode
- `--stub-listen`: primary dependency stub address
- `--stub-compat-listen`: optional compatibility stub address
- `--https-stub`: enable native CONNECT/TLS dependency stubbing
- `--stub-ca-dir`: use an isolated replay CA directory
- `--stub-mitm-allow-hosts`: allowlist HTTPS dependency hosts

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

### Semantic matching

The `matching` section allows captured dependency calls to match runtime values
without weakening every request:

```yaml
matching:
  ignored_query_parameters: [timestamp]
  ignored_headers: [X-Request-ID]
  ignored_json_paths: [$.metadata.request_id]
  rules:
    - name: payment
      methods: [POST]
      host_regex: "^payments\\.example\\.test$"
      path_regex: "^/v1/payments/[0-9]+$"
      header_regex:
        X-Tenant: "^tenant-[a-z]+$"
      query_regex:
        mode: "^(sync|async)$"
      jsonpath_regex:
        $.customer.id: "^cust_[0-9]+$"
      compare_headers: true
      compare_json: true
```

Regexes use Go RE2 syntax. InfernoSIM's deterministic JSONPath subset supports
the root `$`, dotted object keys, and numeric indexes such as
`$.orders[0].id`. Query fields and JSON paths with regex predicates are
automatically excluded from exact equality; additional volatile values can be
listed in the ignored fields.

### Explicit stateful scenarios

Scenarios are evaluated before captured events. A matching step returns its
configured response and atomically advances the named scenario:

```yaml
scenarios:
  - name: authentication
    initial_state: logged_out
    steps:
      - name: login
        state: logged_out
        next_state: logged_in
        match:
          methods: [POST]
          path_regex: "^/login$"
        response:
          status: 200
          headers:
            Content-Type: [application/json]
          body: '{"token":"test-token"}'
      - name: profile
        state: logged_in
        match:
          methods: [GET]
          path_regex: "^/profile$"
        response:
          status: 200
          body: '{"name":"Replay User"}'
```

Scenario state resets at the beginning of each replay run. See
[`examples/replay-v2.yaml`](examples/replay-v2.yaml) for the combined schema.

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

### Native HTTPS response stubbing

Replay can terminate application CONNECT/TLS traffic and return captured or
scenario responses without contacting the dependency:

```yaml
stub:
  https:
    enabled: true
    ca_dir: ./.infernosim-ca
    allow_hosts:
      - payments.example.test
```

Start the application with both proxy variables pointing to the replay stub and
trust the printed CA:

```bash
HTTP_PROXY=http://127.0.0.1:19000 \
HTTPS_PROXY=http://127.0.0.1:19000 \
SSL_CERT_FILE="$PWD/incident/.infernosim-ca/infernosim-ca.crt" \
./your-application

./infernosim replay ./incident \
  --https-stub \
  --stub-ca-dir ./incident/.infernosim-ca \
  --stub-mitm-allow-hosts payments.example.test
```

HTTPS stubbing is opt-in and refuses hosts outside the allowlist. It negotiates
HTTP/2 with ALPN inside CONNECT and also accepts cleartext HTTP/2 (h2c), so
captured gRPC response frames and trailers can be replayed without an HTTP test
endpoint. Older captures that contain `grpcStatus` but no response trailer map
are upgraded during replay by emitting the corresponding `grpc-status`
trailer.

Explicit scenarios can also return gRPC data using `body_base64`, `trailers`,
and `grpc_status`. `body_base64` must contain standard gRPC wire frames, not
unframed protobuf bytes.

Current gRPC virtualization is wire-level: it faithfully replays captured unary
responses and bounded streams, but it does not yet use protobuf descriptors for
field-aware matching or dynamically synthesize new protobuf messages. Payloads
larger than the 256 KiB capture bound remain fingerprint-only and cannot be
used as replay bodies.

## OpenAPI validation and release reports

Validate an incident without replaying it:

```bash
./infernosim contract ./incident \
  --spec ./openapi.yaml \
  --report-formats junit,sarif,html
```

Validate both the baseline and a candidate, including status and content-type
drift:

```bash
./infernosim replay ./incident \
  --target-base http://127.0.0.1:8081 \
  --openapi ./openapi.yaml \
  --report-formats junit,sarif,html \
  --report-dir ./artifacts
```

OpenAPI 3.0/3.1 path templates, request/response JSON schemas, local component
schema references, required properties, primitive types, arrays, enums,
documented statuses, and required response headers are supported. Unsupported
external `$ref` values produce explicit findings instead of being silently
accepted.

## Privacy policies

Privacy policies operate on the copy written to the incident; forwarded traffic
is unchanged. Rules can redact, drop, or deterministically tokenize headers,
query parameters, and JSON fields:

```bash
export INFERNOSIM_TOKEN_KEY='replace-with-at-least-16-random-bytes'
./infernosim capture \
  --forward 127.0.0.1:8081 \
  --privacy-policy ./examples/privacy-policy.yaml \
  --out ./incident
```

`capture_bodies: true` stores the transformed body, enabling replay without
retaining the original sensitive value. Tokens are `tok_` plus a keyed
HMAC-SHA256 digest and cannot be reversed. Keep the token key outside the
incident. See [`examples/privacy-policy.yaml`](examples/privacy-policy.yaml).

## Encrypted incident bundles v2

Seal a completed incident into an authenticated portable archive:

```bash
export INFERNOSIM_BUNDLE_PASSPHRASE='use-a-long-random-passphrase'
./infernosim bundle seal \
  --out incident-001.inferno \
  ./incident-001

./infernosim bundle open \
  --out ./incident-001-opened \
  incident-001.inferno
```

Bundle v2 uses AES-256-GCM authenticated encryption and derives its key with
PBKDF2-HMAC-SHA256 using a random salt and 600,000 iterations. Passphrases are
read only from the named environment variable, never a command-line argument.
Extraction rejects path traversal, links, oversized files, and non-empty
destinations.

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
