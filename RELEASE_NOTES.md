# InfernoSIM v3.1.0

Status: release candidate

This release hardens capture and replay safety, repairs the incident
record/replay contract, and makes replay comparisons response-aware.

## Highlights

- Recorded incidents now contain usable outbound `OutboundCall` exchanges.
- Dependency stubs replay captured status, stable headers, and response bodies.
- Replay determinism includes response-body fingerprints.
- State-aware replay maps captured response values to fresh runtime values.
- Replay and diff detect status, response-header, body-hash, and latency drift.
- Fanout dependency matching uses an unordered captured-call multiset rather
  than one racy global sequence.

## Security and safety

- POST, PUT, PATCH, and DELETE replay is blocked by default.
- `--allow-writes` is required to replay non-idempotent requests.
- Legacy `strict-replay` and `search` rewrite calls to the selected target.
- Capture redacts credentials/cookies and omits raw bodies by default.
- `--capture-sensitive-data` is required for raw replayable payloads.
- Incident logs and metadata are created with mode `0600`.
- Proxy listeners bind to loopback by default.
- Private, loopback, link-local, and unspecified destinations are blocked unless
  `--allow-private-destinations` is supplied.
- Destination validation and dialing use the same resolved IP to prevent DNS
  rebinding between checks.
- Upstream TLS verification is enabled unless `--insecure-upstream` is supplied.
- MITM certificates are restricted to an explicit hostname allowlist and cached
  per host.
- Updated `x/net`, `x/text`, gRPC, and the required Go toolchain to patched
  versions identified by `govulncheck`.

## Capture and proxy reliability

- Body capture uses a bounded prefix buffer while forwarding the complete
  stream.
- Listener binding happens synchronously, so port conflicts are returned to the
  CLI instead of terminating later from a goroutine.
- HTTP servers now set header and idle limits.
- Hop-by-hop headers are removed before forwarding.
- HTTP/2 MITM connections are served with the HTTP/2 server implementation.
- Fault injection uses a concurrency-safe PRNG and validates percentages,
  status codes, durations, malformed tokens, and unknown keys.
- `--inject-seed` provides repeatable standalone proxy injection.

## CLI and configuration

- `infernosim replay <incident> --flags...` and
  `infernosim diff <incident> --flags...` accept flags after the incident path.
- Missing `outbound.log` now produces a supported inbound-only weak replay.
- Replay artifacts are written into the incident directory instead of global
  working-directory files.
- YAML parsing rejects unknown fields and invalid values.
- YAML state adapters and request-scoped chaos latency are wired into replay.
- `record` starts a real outbound forward proxy using `--outbound-listen`.
- `inspect` and `verify` correlate paired inbound responses before analysis.

## Packaging and project hygiene

- Added GitHub Actions checks for formatting, module consistency, vet, race
  tests, CLI build, shell syntax, and Compose validation.
- The container uses pinned Alpine variants, builds for the requested target
  architecture, and runs as an unprivileged user.
- Compose no longer grants blanket privileged access and uses a BusyBox `nc`
  healthcheck.
- Removed tracked compiled/OS artifacts and unused fake-time code.
- Updated the README, contributor guide, examples, scenarios, and scripts for
  Go 1.25 and the new safety defaults.

## Breaking changes and migration

1. Replaying writes now requires `--allow-writes`.
2. Raw headers and bodies require `--capture-sensitive-data`.
3. Proxying to local/private dependencies requires
   `--allow-private-destinations`.
4. Applications must use the forward proxy printed by `infernosim capture` to
   record outbound dependencies.
5. Replay result and snapshot files now live inside the incident directory.
6. Go 1.25.12 or newer in the 1.25 line is required.

## Known limitations

- Native HTTPS CONNECT/TLS response stubbing is not implemented. Use an HTTP
  test endpoint or a TLS termination layer in front of the replay stub.
- Transparent replay remains Linux-specific and requires explicit root and
  `NET_ADMIN` permissions.
- State extraction focuses on common JSON token/resource fields and cookies;
  application-specific formats may require a state adapter.

## Validation performed

- `go test -race ./...`
- `go vet ./...`
- `govulncheck ./...` (zero reachable vulnerabilities)
- `go mod tidy -diff`
- `git diff --check`
- Shell syntax validation for every script
- Docker Compose configuration validation
- Cross-builds for Linux amd64/386/arm64, Windows amd64, and Darwin arm64
- Local CLI capture → inspect → verify → replay → diff workflow
- Strong two-run replay with a captured outbound dependency
- Deliberate auth/body/latency/status drift detection with non-zero exit status

The Dockerfile was configuration-checked, but an image build could not be run
in the validation environment because its Docker daemon was not running.
