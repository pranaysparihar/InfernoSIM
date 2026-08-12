# InfernoSIM v3.4.0

Status: generally available

v3.4 is a single GA release train. No alpha, beta, v3.5, or v3.6 aliases are
used. Publication is allowed only after the mandatory release workflow passes
on the exact commit being tagged.

v3.4 completes InfernoSIM's local incident-to-test path. A sanitized production
incident can now become an editable container test, an explainably stabilized
matcher configuration, a cross-protocol contract gate, and a deterministic
proof artifact without an InfernoSIM-hosted service.

## Highlights

- `infernosim serve` runs captured HTTP, HTTPS, HTTP/2, and gRPC dependency
  behavior as a standalone simulator with a separate health/reset/status/proof
  control API.
- `infernosim testgen` produces readable Testcontainers-Go, Docker Compose, or
  GitHub Actions harnesses. A maintained Testcontainers-Go adapter is shipped
  as an independent module under `integrations/testcontainers-go`.
- `infernosim heal` infers narrow semantic matchers from repeated observations,
  validates candidates on a held-out observation, protects security/business
  fields, hashes evidence, and refuses ambiguous proposals.
- Kafka-compatible capture and replay preserve topic, partition, offset, key,
  headers, payload, schema name, correlation ID, timestamp, and payload hash.
- Kafka connections support TLS, mTLS, SASL/PLAIN, SCRAM-SHA-256, and
  SCRAM-SHA-512 without accepting passwords on the command line.
- Deterministic Kafka delay, drop, duplicate, poison, and reorder plans make
  asynchronous failure tests repeatable.
- AsyncAPI 3.x validates JSON payload schemas, message/channel references,
  required fields, types, enums, patterns, and additional-property drift.
- Explicit workflows verify ordered HTTP, gRPC, and Kafka observations with
  optional correlation and per-step timing bounds.
- HTTP and Kafka captures share configurable deterministic tokenization,
  redaction, and drop rules.
- Simulator and Kafka proof JSON records semantic fingerprints; workflow,
  AsyncAPI, and Kafka commands emit JUnit, SARIF, and HTML reports.

## Safety and compatibility

- Existing incidents and replay configuration remain valid. The new
  `workflows` section is optional and strictly validated.
- Healing writes `replay.proposed.yaml`; it never silently replaces
  `replay.yaml`. Explicit `--apply` promotion creates `replay.yaml.bak` first.
- Authorization, credentials, tenant/account boundaries, permissions, money,
  status, and personal-data fields cannot be automatically relaxed.
- UUIDs and timestamps are relaxed only at allowlisted volatile locations such
  as request, trace, correlation, nonce, or timestamp fields; identity IDs stay
  exact. Regeneration removes stale InfernoSIM-managed healing rules.
- A proposal that makes distinct recorded responses match the same request
  fails without writing configuration.
- Control APIs do not expose captured bodies, keys, or headers.
- Generated incident files remain owner-only on the host and are copied
  read-only into the isolated, unprivileged container.
- Kafka capture subscribes only to topics explicitly selected by the user.
- Kafka capture requires a privacy policy unless raw sensitive-data storage is
  explicitly enabled. Replayable policy-based capture also requires
  `capture_bodies: true`.
- Invalid report formats, topics, authentication combinations, non-finite
  timing/confidence values, duplicate message IDs, duplicate privacy rules,
  unsupported AsyncAPI schema features, and unsafe generated paths fail before
  network side effects.
- Automated Homebrew publication requires a credential with write access to
  the separate tap repository. A maintainer may instead publish from an
  authenticated local session without storing that credential in InfernoSIM.
  Releases upload only eight platform archives plus `checksums.txt`;
  benchmark/report JSON is excluded.

## Validation evidence

- The checked-in category baseline completed 100 independent heal/testgen runs
  with the expected accept/reject behavior, one configuration hash, one harness
  hash, zero ambiguities, and zero seeded-secret leaks. On the recorded local
  run, healing p95 was 0.896 ms and test generation p95 was 0.607 ms. Raw
  results are in `benchmarks/results/infernosim.json`; timing is environment
  specific and is not a competitor claim.
- Root and nested-module race tests, module consistency, vet, and reachable
  vulnerability analysis pass. The vulnerability scan reports zero reachable
  vulnerabilities.
- The Kafka CLI wire test passes capture → AsyncAPI validation → prefixed replay
  → consume and verifies JUnit, SARIF, HTML, and proof files.
- The production Dockerfile builds successfully, runs unprivileged, exposes the
  proxy/control ports, and passes the real Testcontainers lifecycle/proxy/reset
  test. Node and Go Compose smoke profiles pass against that image.
- Targeted matcher, gRPC, template, OpenAPI, bundle, healer, and message fuzz
  smoke tests pass. GoReleaser builds all eight platform archives, a checksum
  manifest, and the Homebrew cask; the upload-asset assertion reports nine
  project assets, which GitHub displays as 11 after its two automatic source
  downloads.
- The Docker smoke passes capture → validation → replay → consume against
  Redpanda v25.2.9 using a pinned multi-architecture image digest and separate
  internal/external listeners. This remains a mandatory release-workflow gate.

## Deliberate boundaries

- v3.4 supports Kafka-compatible brokers, not RabbitMQ, NATS, MQTT, SQS, or SNS.
- AsyncAPI validation covers JSON payloads. Avro, Schema Registry, and remote
  schema resolution are not implemented.
- Kafka capture uses a consumer subscription rather than a transparent Kafka
  protocol proxy.
- Healing is deterministic rule inference rather than an LLM and declines
  values it cannot classify safely.
- Existing gRPC compression, reflection/remote descriptor, large-body, and
  bidirectional-stream branching limitations remain.

---

# InfernoSIM v3.3.0

Status: GA-ready (publish the signed `v3.3.0` tag to make this public)

v3.3 turns recorded exchanges and API schemas into programmable, typed
simulations while preserving InfernoSIM's deterministic and fail-closed safety
model.

## Highlights

- Sandboxed response templates can derive bodies, headers, and trailers from
  JSON, Protobuf, query, and header values in the runtime request.
- Seeded `uuid`, `token`, `now`, and `nowUnix` functions produce stable output
  for the same request without filesystem, environment, command, or network
  access.
- `.proto` files and binary `FileDescriptorSet` documents can be compiled at
  runtime for descriptor-aware gRPC request matching and typed response
  synthesis.
- Protobuf field regexes, ignored fields, and semantic message comparison work
  across unary messages and bounded client streams.
- Scenarios can synthesize unary or server-streamed gRPC responses with named
  status codes, trailers, and configurable inter-message delays.
- `infernosim generate` creates reviewable simulation configurations from
  OpenAPI 3.x or Protobuf contracts.
- `infernosim lint` checks strict schema validity, template syntax, duplicate
  rules, unreachable states, and shadowed scenario steps.
- `infernosim match explain` reports the match or rejection reason for every
  captured dependency candidate.

## Compatibility and safety

- Existing incidents, static scenarios, and wire-level gRPC captures remain
  valid without configuration changes.
- Schema-aware Protobuf behavior is opt-in under `matching.grpc`.
- Relative Protobuf paths are resolved from the directory containing
  `replay.yaml`.
- Template source, output, stream frame count, and Protobuf message sizes are
  bounded.
- Compressed gRPC frames remain replayable as captured bytes but are not
  accepted for descriptor-aware decoding.
- Generated files are never overwritten unless `--force` is explicit.

## GA release engineering

- CI now covers formatting, module consistency, vet, reachable-vulnerability
  analysis, race tests, targeted fuzzing, platform builds, container
  architecture checks, and Node/Go Compose smoke profiles.
- Tagged releases generate platform archives and a SHA-256 checksum manifest.
- Generated JSON, reports, SBOMs, signature bundles, and internal fixtures are
  deliberately excluded from GitHub release assets.
- GoReleaser configuration was migrated away from deprecated archive,
  snapshot, and Homebrew formula properties.
- Upgrade and maintainer release guides are included under `docs/`.

## Known limitations

- Protobuf schema loading is local-file based; gRPC server reflection and
  remote Buf Schema Registry resolution are not yet implemented.
- Protobuf semantic decoding rejects compressed messages.
- Captured bodies larger than 256 KiB remain fingerprint-only.
- Streaming simulation models ordered messages and delay but does not yet
  provide per-message state transitions or client-driven bidirectional
  branching.

---

# InfernoSIM v3.2.0

Status: generally available

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
