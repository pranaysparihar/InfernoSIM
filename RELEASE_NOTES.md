# InfernoSIM v3.2.0

Status: release candidate

This release turns InfernoSIM's replay engine into a protocol-safe release
gate. It adds native HTTPS dependency responses, semantic and stateful
virtualization, OpenAPI contract checks, standard CI reports, authenticated
encrypted bundles, and policy-driven privacy transformations.

## Highlights

- Native HTTPS CONNECT/TLS dependency stubbing returns captured or scenario
  HTTP/1.1, HTTP/2, and gRPC responses without contacting the original service.
- HTTPS gRPC capture now performs a verified TLS handshake to the upstream
  HTTP/2 service instead of returning a raw socket from the HTTP/2 dial hook.
- gRPC response trailers are captured and replayed, with compatibility fallback
  from the existing `grpcStatus` event field.
- Semantic request matching supports RE2 host/path/header/query predicates,
  deterministic JSONPath predicates, exact JSON comparison, and ignored
  volatile query/header/JSON fields.
- Explicit scenarios define named states, request matchers, responses, and
  atomic transitions that reset for every replay run.
- OpenAPI 3.0/3.1 validates captured and replayed requests/responses and reports
  undocumented operations/statuses, schema violations, missing required
  fields, and response drift.
- JUnit XML, SARIF 2.1.0, and escaped standalone HTML reports share the same
  finding model and can be emitted from `contract` or `replay`.
- Encrypted incident bundle v2 uses AES-256-GCM authenticated encryption with a
  random salt/nonce and PBKDF2-HMAC-SHA256 key derivation.
- Privacy policies can redact, drop, or deterministically tokenize headers,
  query parameters, and JSON fields before incident data is stored.

## CLI and configuration

- Added `infernosim contract <incident> --spec <openapi>` for baseline contract
  validation.
- Added `infernosim bundle seal` and `infernosim bundle open`.
- Added replay flags `--https-stub`, `--stub-ca-dir`,
  `--stub-mitm-allow-hosts`, `--openapi`, `--report-formats`, and
  `--report-dir`.
- Added capture/agent flag `--privacy-policy`.
- Extended strict `replay.yaml` parsing with `matching`, `scenarios`, and
  `stub.https`.
- Added complete examples in `examples/replay-v2.yaml`,
  `examples/privacy-policy.yaml`, and `examples/openapi.yaml`.

## Security and safety

- TLS leaf certificates are generated only for replay hosts on the explicit
  allowlist.
- TLS stubbing requires opt-in and uses TLS 1.2 or newer.
- Tokenization uses HMAC-SHA256 with a key supplied outside the incident;
  tokens do not contain recoverable plaintext.
- Bundle passphrases are read from an environment variable rather than command
  arguments or configuration files.
- Bundle opening authenticates ciphertext before extraction and rejects
  traversal paths, links, oversized entries, existing destination contents,
  and overwrite attempts.
- Existing secure capture behavior remains the default. Transformed bodies are
  stored only when `capture_bodies: true` is set in an explicit privacy policy.
- Regex, JSONPath, OpenAPI, scenario, bundle, and policy schemas fail closed on
  invalid configuration.

## Compatibility

- Existing JSONL incident logs remain readable without conversion.
- Existing `replay.yaml` files remain valid.
- Exact method/host/path/query matching remains the default when no semantic
  matcher rules are configured.
- HTTPS stubbing and transformed body storage are disabled unless explicitly
  configured.
- Bundle v2 is a portable encrypted wrapper around the existing incident
  directory, so opening it restores the standard layout.

## Known limitations

- gRPC virtualization is currently wire-level. Protobuf descriptor-aware field
  matching and dynamic message synthesis are not yet implemented.
- Captured gRPC streams larger than the 256 KiB payload bound are
  fingerprint-only and cannot be replayed as response bodies.
- The JSONPath implementation intentionally supports a deterministic subset:
  `$`, dotted object keys, and numeric array indexes.
- OpenAPI validation supports local component schema references. External
  `$ref` resolution is rejected and reported.
- Transparent replay remains Linux-specific and requires explicit root and
  `NET_ADMIN` permissions.

## Validation performed

- `go test -race ./...` passed across every package.
- `go vet ./...`, `go mod tidy -diff`, formatting, and `git diff --check`
  passed.
- `govulncheck ./...` found zero reachable vulnerabilities. One required module
  contains an advisory in code InfernoSIM does not call.
- Focused tests passed for authenticated HTTPS response stubbing, matcher
  regexes and ignored fields, state transitions, OpenAPI schema/drift checks,
  all three report formats, privacy transformations, and encrypted bundle
  round trips.
- Coverage for the new packages is 61.6%–83.3%; the HTTPS/stub package is
  41.5% and includes a real client/proxy/TLS integration test.
- Cross-builds passed for Linux amd64/arm64/386, Windows amd64, and Darwin
  amd64/arm64 with `CGO_ENABLED=0`.
- A built CLI produced the expected non-zero OpenAPI gate result plus valid
  JUnit, SARIF, and HTML artifacts.
- A built CLI sealed, authenticated, opened, byte-compared, and inspected an
  encrypted v2 incident.
- A live local auth-service replay completed `PASS_STRONG` while OpenAPI
  validation and all report exporters were enabled.
- Real generated gRPC clients passed HTTPS MITM capture and native HTTPS
  response virtualization over HTTP/2, including protobuf frames and
  `grpc-status` trailers.
- Shell syntax and the merged Docker Compose configuration validated.
- The Docker image built and ran as the unprivileged `infernosim` user with a
  native arm64 binary, and both Node and Go Compose smoke profiles passed.
- The Docker builder pin now uses the available Go 1.25.11 Alpine image with
  automatic selection of the Go 1.25.12 toolchain required by `go.mod`; target
  architecture defaults no longer force amd64 binaries into arm64 images.

---

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

## Known limitations at v3.1.0

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
