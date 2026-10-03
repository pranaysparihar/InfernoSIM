# InfernoSIM v4.1.0

Release target: v4.1.0. See [GitHub Releases](https://github.com/pranaysparihar/InfernoSIM/releases/tag/v4.1.0) for publication status.

## Discover failures and verify application state

- `agent explore` generates a deterministic, budgeted campaign from the
  existing incident: individual faults, subsets of up to four faults,
  occurrence variations, and explicitly permitted schedule permutations.
  Plans retain seed and case IDs and report truncation. Invalid replay,
  unexercised faults, and incomplete schedules are distinguished from valid
  assertion failures.
- Independent application-state checks are explicit user-supplied commands.
  Setup resets the test system before each run; a separate check process
  returns exactly the declared assertion IDs and boolean outcomes. Each case
  receives a private state directory shared across setup, application,
  process restart, and check. Malformed, missing, duplicate, or timed-out
  checks fail closed. Probe output is withheld from reports.
- State checks work with existing run, stress, compare, and reduce commands.
  Comparison binds the check contract into its execution scope. Hooks use the
  caller's ordinary network environment for test-database or broker access.

## Reduce and retain a regression

- `agent minimize` removes faults and admission-order constraints and lowers
  selected occurrence positions while preserving one named violation on two
  consecutive valid executions. The execution budget is hard; incomplete
  reduction returns its best result with a failing exit code.
- `agent reproduce` runs an explicitly supplied application against an exported
  incident/trial. A buggy application fails the gate; a fixed one passes.
  Portable artifacts contain the required check contract, timeouts, and
  restart boundary. Commands are never executed from incident or artifact
  data. Artifacts verify file checksums, reject links and external schema
  dependencies, and never overwrite an existing destination.
- Optional partial schedules constrain only selected participants while still
  requiring all of those participants to appear. Complete schedules remain
  the default.
- JSON, JUnit, SARIF, and existing standalone HTML reports cover the new cases.
  No hosted service, account, frontend runtime, or live model is required.

## Demonstration and compatibility

The runnable outbox example models an idempotent payment service and an
application that accidentally writes a duplicate local outbox entry after a
lost response. The simulated payment ledger passes; the independent state
check catches the application bug. The smoke explores eight cases per
application version, minimizes the noisy three-fault failure, relocates the
artifact, verifies the fix, and rejects tampered evidence.

Existing v4.0.1 configurations remain compatible. Generated harnesses now use
the v4.1.0 container. The incident guide's obsolete single-fault/parallel-call
limitations are corrected. gRPC is updated to v1.83.2 with required x/net,
x/sys, x/sync, and x/text updates, addressing GO-2026-6348 and GO-2026-6443.

Start with the [runnable discovery guide](docs/DISCOVERY_4_1.md) and
[upgrade notes](docs/UPGRADING.md).

## Validation

The full Go race suite passes with the release toolchain Go 1.26.6. Module
checks, vet, and the main-module vulnerability scan pass. The pinned official
MCP SDK and Testcontainers modules pass, including the real Docker lifecycle
test. The container builds as the existing unprivileged user; Kafka/AsyncAPI
against Redpanda and both Node/Go Compose smokes pass. Existing agent and
recovery controls and the new discovery CLI smoke pass. Generated Actions and
Compose harnesses validate. The deterministic incident benchmark passes 100
runs with stable configuration/harness hashes and no detected fixture leaks;
the agent benchmark passes 100/100 safe runs and rejects 80/80 unsafe controls.
The release workflows also execute the new discovery smoke.

## Boundaries

Exploration is bounded deterministic enumeration, not adaptive coverage-guided
fuzzing or exhaustive race search. Admission schedules do not control OS
threads or response delivery. Recorded-model divergence remains invalid; it
never silently falls back to live inference. Reduction is a fixed point under
its documented moves, not a globally shortest trace or root-cause proof.
State probes are user-authored checks against isolated test systems, not a
universal database integration or a security sandbox. Application output and
incident artifacts can contain secrets; existing privacy and encryption
controls still apply. Artifact checksums detect changed files, not publisher
authenticity. Existing streaming/MCP/platform limitations remain documented.

---

# InfernoSIM v4.0.1

Release target: v4.0.1. See [GitHub Releases](https://github.com/pranaysparihar/InfernoSIM/releases/tag/v4.0.1) for publication status.

## Safety and recovery

- Independent monitor assertions detect absent verdicts, false healthy status,
  stale response revisions, and bad request-side state estimates against a
  declared simulated effect ledger.
- Bound, single-use approval assertions check matching arguments/tenant and a
  logical expiry window. Request predicates support synthetic identity and
  authorization fixtures. Compensation assertions require later,
  identity-matched rollback effects.
- Shared call, recorded-token, and explicitly priced cost budgets fail on
  unknown usage. Costs are configured estimates, not provider invoices.
- `agent run/stress --restart-after-call N` kills and restarts the explicit
  application at a simulated response boundary. Ledger/deduplication state and
  a private application checkpoint directory survive the restart.
- Assertion coverage distinguishes exercised, violated, and unexercised paths;
  `require_exercised` makes absent safety behavior a release failure.

## Deterministic exploration and regression gates

- Explicit call-ID admission schedules and bounded pairwise fault enumeration.
  Every tool entry in a complete supported provider JSON envelope can be
  selected. Combined faults no longer erase earlier transport failures.
- `agent compare` runs two explicit application argv arrays against the same
  corpus/cases/schedules and detects failed cases, missing assertions, and
  safety-coverage regressions. Reports include JSON, JUnit, SARIF, and HTML.
- `agent reduce` finds a one-minimal fault set preserving a named violated
  assertion on repeated executions, with a hard execution budget.
- MCP stdio correlates out-of-order responses to the correct runtime request
  IDs, preserves notifications, remaps cancellation IDs, and rejects duplicate
  in-flight IDs and incomplete transcripts. Already admitted work is distinct
  from a new call after cancellation.

## Fixes, compatibility, and packaging

- Missing effect identity/deduplication fields fail closed instead of collapsing
  unrelated effects into a shared key. Request identity hashing preserves large
  integer precision. JSON-RPC/tool errors cannot satisfy retry verification.
- Updated `golang.org/x/net` to v0.56.0 (and `x/sys` to v0.46.0), removing
  GO-2026-5942 from the dependency graph. Injected HTTP waits honor cancellation.
- Agent harness generation retains combined fault and schedule selections.
- The release build targets the complete CLI package. Packaging is explicitly
  eight platform archives plus checksums; GitHub's two automatic source
  archives retain the established total of 11. Reports and internal JSON
  metadata are not release assets.
- Existing agent configuration defaults, backend replay features, and encrypted
  bundle-v2 format remain compatible. New configuration fields are opt-in.

## Validation and scope

Local validation includes the full Go race suite, vet, the existing safe/unsafe
agent smoke, the new recovery/safety/compare/reduce CLI smoke, official MCP Go
SDK v1.7.0 record/replay/reconnect integration, a Docker image build, a real
Testcontainers run, Kafka/AsyncAPI against Redpanda, and the installed Ollama
model smoke. The full suite was also run with the release toolchain Go 1.26.6.
After the dependency update, `govulncheck` reported no vulnerabilities in the
main-module and pinned SDK integration scans. The existing agent benchmark
passed 100/100 safe executions and rejected 80/80 unsafe controls across 20
iterations; the incident-to-test benchmark passed 100 runs with one stable
configuration hash, one stable harness hash, and zero detected fixture leaks.
The new crash/recovery example also passed inside a Linux/arm64 container
with external networking disabled. A local GoReleaser snapshot produced all eight platform
archives and their checksums verified. Cross-builds are not native Windows or
Intel macOS runtime tests.

These checks establish the behavior of the tested fixtures, not universal
agent safety, full MCP conformance, or superiority over competitors. Admission
schedules do not control arbitrary OS races. Approvals/credentials are local
test predicates, not real identity-provider enforcement. Process restart is
not power-loss recovery. Live model execution is separate from deterministic
recorded-envelope gates. Windows child termination does not contain descendants.

Start with the [runnable safety/recovery guide](docs/AGENT_SAFETY_4_0_1.md) and
the [upgrade notes](docs/UPGRADING.md). Existing v4.0.0 release history follows.

---

# InfernoSIM v4.0.0

Status: generally available

v4 adds a local, incident-derived reliability gate for tool-using agents. It
replays recorded LLM and tool protocols, injects deterministic semantic and
transport faults, accounts for real side-effect consequences, and emits
reviewable CI evidence without requiring a hosted InfernoSIM service or live
model during the test.

## Highlights

- `infernosim agent cases`, `agent run`, and `agent stress` plan stable
  baseline/single-fault matrices and execute only the explicit child command
  supplied after `--`.
- MCP JSON-RPC over HTTP is recognized inside ordinary HTTP/HTTPS replay. The
  new `agent mcp record` and `agent mcp replay` commands support bounded
  newline-delimited stdio transcripts with semantic request matching and
  runtime request-ID rewriting. Lost responses keep the replay server alive for
  retries, and EOF evaluates assertion/fault coverage with optional reports.
- Protocol adapters recognize OpenAI Responses, OpenAI Chat Completions,
  Anthropic Messages, and Ollama chat tool-call envelopes. Generic HTTP
  selectors remain available for custom gateways.
- Faults can target response kind, provider, tool, HTTP method, host/path RE2,
  and occurrence. Actions include semantic JSON/SSE/NDJSON mutation, status and
  header changes, bounded delay/timeout, reset, truncation, empty success, and
  “side effect committed but response lost.”
- An explicit side-effect ledger tracks hashed identities, idempotency
  deduplication, commit state, response delivery, and ambiguous completion
  without retaining raw tool arguments or results.
- Consequence assertions cover exactly-once, at-most-once, forbidden effects,
  call budgets, verified retry, recorded-universe-only execution, and
  deadlines. Verification can require a delivered 2xx response matching a
  JSONPath/value predicate.
- Streaming HTTP capture can preserve bounded SSE, NDJSON, and JSON-sequence
  response frames and inter-frame delays; replay can mutate structured frames
  without flattening their framing.
- `agent otel import` normalizes OTLP JSON or JSONL into an owner-only
  correlation log. Raw prompt, argument, and result attributes are excluded;
  optional one-way content hashes support correlation.
- Agent results produce private JSON plus JUnit, SARIF, and standalone HTML.
  JUnit contains one testcase per planned case. A separate JSON reliability
  surface reports unweighted observed pass rates by validated fault category;
  it is evidence for that matrix, not a general model score.
- `testgen --profile agent` generates a stable case manifest and agent-aware
  Testcontainers, Compose, or GitHub Actions wiring. Generated Actions run the
  assertion engine and fail if a selected fault is never exercised.
- Bundle-v2 archives automatically include and encrypt optional `mcp.log` and
  `agent-spans.jsonl` files because they remain regular incident files; the
  encrypted format itself is unchanged.

## Public validation fixture

`examples/agent-reliability` contains a sanitized refund incident and recorded
MCP/OpenAI-style exchanges. `examples/agentlab` contains a defensive loop and
an intentionally unsafe control. The matrix covers:

- a refund commit whose response is lost;
- an HTTP-200 policy response missing its decision field;
- an LLM refund tool call missing its arguments; and
- an MCP tools list missing the required refund tool.

The checked-in raw benchmark completed 20 identical matrix iterations. The
defensive loop passed 100/100 executions, the unsafe baseline passed 20/20,
and the unsafe loop was rejected in 80/80 fault executions. Case-plan hashes
were stable and raw content was not retained. Timing is environment-specific.
See `benchmarks/results/agent-reliability.json`.

The optional local Ollama smoke passed with `llama3.1-local:latest` by producing
a native `policy_check` tool call that the adapter recognized. Ollama sampling
is compatibility evidence, not the deterministic release oracle.

## Validation gates

- Root and Testcontainers-module tests pass with the Go 1.26.6 race detector;
  module consistency and `go vet` are clean. The Go vulnerability scan reports
  zero reachable vulnerabilities.
- Ten bounded fuzz targets pass, including the agent fault engine, semantic
  JSONPath implementation, MCP request matcher, OpenAPI matcher, bundle-v2,
  gRPC, streaming-template, healer, and message surfaces.
- The safe/unsafe agent smoke confirms five defensive passes, the unsafe
  baseline control, and rejection of all four unsafe fault cases. The MCP stdio
  CLI fixture evaluates fault coverage and consequence assertions at EOF.
- The final production image builds, runs as the unprivileged `infernosim`
  user, enumerates the five checked-in cases, and passes the real
  Testcontainers lifecycle test. Existing Node, Go Compose, and
  Kafka/AsyncAPI/Redpanda smokes remain green.
- Generated agent GitHub Actions and Compose harnesses pass `actionlint` and
  `docker compose config`. All repository workflows pass `actionlint`.
- A local GoReleaser snapshot builds eight platform archives plus
  `checksums.txt`; every archive checksum verifies. The tagged release workflow
  repeats the mandatory release gates before publishing that same artifact set.

## Safety and compatibility

- Existing incidents and replay configurations remain valid; the `agent`
  section is opt-in.
- The minimum Go toolchain is 1.26.6. This is the first 1.26 patch that fixes
  the reachable standard-library advisories found by the release vulnerability
  scan; CI is pinned to it and the container builder downloads that exact
  toolchain through Go's automatic toolchain selection.
- Both simulator listeners bind to loopback in the runner. Calls outside the
  recorded universe fail closed and can be asserted explicitly.
- Agent bodies are capped at 16 MiB, MCP messages at 16 MiB, MCP transcripts at
  100,000 records, OTel imports at 64 MiB, call/case counts at 10,000, and fault
  delays at 60 seconds. JSONPath length/depth and regex length are bounded.
- Configuration is strictly decoded. Duplicate IDs, unknown fields, invalid
  selectors, unsupported mutations, conflicting terminal faults, and an
  ambiguous-response fault without a matching declared effect are rejected.
- Proofs expose hashes, operation names, counts, assertion results, and
  divergence reasons—not captured bodies, prompt text, or raw tool values.
  Bounded child stdout/stderr is stored only in owner-only JSON and should
  still be treated as sensitive application output.
- Case execution has a bounded timeout and process-output limit. Incident data
  never supplies executable commands.
- The release artifact contract is unchanged: eight platform archives plus
  `checksums.txt` are uploaded by GoReleaser. Benchmark JSON, reports, fixtures,
  and internal GoReleaser metadata are not release assets.

## Deliberate boundaries

- v4 explores the baseline plus one configured fault at a time. It does not
  claim combinatorial search or formal verification.
- The first recognized tool call in a provider response is independently
  selectable. Parallel multi-tool envelopes are replayable but not yet
  individually fault-addressable within one response.
- MCP stdio support targets newline-delimited JSON-RPC. Other stdio framing and
  binary MCP transports are not claimed.
- OpenTelemetry support is an offline OTLP JSON/JSONL importer, not a live OTLP
  collector.
- Provider adapters recognize envelopes; they do not replace an LLM server or
  make live model sampling deterministic.
- Existing gRPC compression, reflection/remote descriptor, large-body, and
  bidirectional-stream branching limitations remain.

## Upgrade and documentation

- New guide: `docs/AGENT_RELIABILITY.md`
- Upgrade notes: `docs/UPGRADING.md`
- Maintainer gates: `docs/RELEASING.md`
- Example fixture: `examples/agent-reliability`
- Safe/unsafe reference loop: `examples/agentlab`
- Local gates: `scripts/agent-smoke.sh` and `scripts/ollama-smoke.sh`

---

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
- Homebrew publication does not require a cross-repository personal access
  token. The tap's own scheduled/manual workflow reads the latest public
  release and publishes the source-built formula with its repository-scoped
  `GITHUB_TOKEN`; a maintainer can also update it from an authenticated local
  session.
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
  smoke tests pass. GoReleaser builds all eight platform archives and a
  checksum manifest; the source-built Homebrew formula passes `brew test`.
  The upload-asset assertion reports nine project assets, which GitHub displays
  as 11 after its two automatic source downloads.
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
