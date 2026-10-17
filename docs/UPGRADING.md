# Upgrading to v4.2.1

Existing replay stays offline unless you explicitly configure live recovery.
Generated harnesses now use the `4.2.1` container. The patched build toolchain
remains Go 1.26.9; fuzz verification uses Go 1.27.2.

New reproduction exports use format v3 with checker fingerprints and case
lineage. Use v4.2.1 to read them. Formats v1/v2 remain readable; artifacts that
did not retain a checker are labelled `legacy-unpinned`. They can be replayed,
but cannot establish an unchanged-checker comparison or reduction. Supply an
explicit checker and `--new-case-out` to create a new pinned case.

Checker fingerprints now include executable/direct-script contents, arguments,
required assertion IDs, timeout and setup presence. Declare imported helpers,
lockfiles and configuration with `--checker-manifest`. Changing those inputs
requires a new case. Do not declare the mutable destination being checked.
See [checker fingerprints](CHECKER_FINGERPRINTS.md) for scope and migration.

New capture metadata adds session/counter observations and a completion seal.
`agent causal record` creates a shared HTTP/MCP session. Standalone MCP captures
also produce a `.capture-session.json` sidecar; retain it with the transcript.
Old recordings still replay, but timestamps alone cannot establish an exact
observed order. Conversion reports missing evidence instead of inventing it.
See [observed schedules](OBSERVED_SCHEDULES.md).

Opt-in [live recovery](LIVE_RECOVERY.md) is limited to explicitly configured
local Ollama HTTP chat. Hybrid runs are labelled non-deterministic; convert and
validate their newly captured branch before offline comparison or minimization.
Existing causal schedules, stdio sessions and process-restart mode cannot be
combined with this initial live mode.

For fresh JSON-RPC request IDs use the documented [retry reproduction](RETRY_REPRODUCTION.md)
route. Ordinary HTTP replay still does not remap those IDs. New destination
recovery regressions live in [recoverylab](../examples/recoverylab/README.md).

---

# Upgrading to v4.2.0

Existing non-causal configurations and commands remain supported. Causal
discovery, graph scheduling and argument-relative provider mutations are opt-in.
Use Go 1.26.9 or newer to build; generated harnesses now default to the 4.2.0
container.

`agent causal discover` saves an unreviewed graph but returns exit code 1 until
the dependency review and blocking issues are resolved. `agent explore
--causal-graph` cannot combine a causal graph with legacy admission schedules.
Identical concurrent requests need distinct stable application identities.

Configured provider streams are now validated for the supported lifecycle and
completion events even in baseline runs. Replace synthetic or incomplete SSE
fixtures with valid recorded events; unsupported formats fail explicitly.
`mutations[].target: tool_arguments` uses paths relative to one selected tool's
arguments. The existing whole-response target remains the default.

New reproduction exports use manifest version 2, including a manifest checksum.
Non-causal v1 artifacts remain readable. Causal artifacts retain the graph and
execution evidence and require v4.2. Their total size, including the larger
manifest, remains bounded to 64 MiB. Commands still come from explicit CLI argv.

For stdio under causal replay, start the connector argv from
`INFERNOSIM_MCP_COMMAND_JSON` so stdio and HTTP share the runner's controller.
The bridge supports one client session; automatic MCP reconnect/resumption is
not introduced by the HTTP process-restart feature.

See the [concurrency guide](CONCURRENCY_4_2.md) and
[validation evidence](VALIDATION_4_2.md).

---

# Upgrading to v4.1.0

Existing v4.0.1 configuration and commands remain compatible. New `agent
explore`, `agent reproduce`, and `agent minimize` commands are opt-in. The
`partial` schedule field defaults to false. State-check hooks require explicit
CLI argv and required IDs; incident configuration cannot launch hooks.
`state:` assertion IDs are reserved when an independent check is supplied.
Comparison rejects changed hook contracts. Incomplete schedules are now marked
invalid so reducers cannot mistake missing participants for a product failure.

See [v4.1 discovery](DISCOVERY_4_1.md) for examples, artifact portability,
privacy, budget limits, and the distinction between simulated effects and
independently observed application state.

---

# Upgrading InfernoSIM

## v4.0.0 to v4.0.1

Existing `agent.version: 1` configurations still work. New assertions,
`exploration.pairwise`, schedules, and `require_exercised` are opt-in. New
configurations require the 4.0.1 binary; older strict decoders reject them.

Reports add coverage, scope, schedule, usage, binding-verdict, and restart
fields. Consume `Case.ActiveFaults()`/`fault_ids` for combinations, not just
the legacy single `fault_id`. Schedule-only cases are not the default baseline.
Unexercised conditional checks remain non-failing unless required, but they
are now labeled honestly and can cause a coverage regression in comparisons.

Missing declared identity/deduplication fields now fail closed. MCP stdio
rejects incomplete transcripts and duplicate in-flight IDs, and verification
does not accept JSON-RPC errors merely because the transport returned HTTP 200.
Review fixtures that depended on those previous permissive behaviors.

Regenerate agent harnesses to retain combined faults and schedule IDs. The
generated image default is `ghcr.io/pranaysparihar/infernosim:4.0.1`; until that
image is published, pass `--image infernosim:4.0.1-local` after a local Docker
build. The encrypted bundle format and ordinary replay configuration have
not changed. See the [full guide](AGENT_SAFETY_4_0_1.md).

## v3.4 to v4.0

Existing incident directories and `replay.yaml` files remain valid. Agent
reliability is disabled unless the optional `agent` section sets
`enabled: true`.

### Agent reliability configuration

v4 adds strict `agent.adapters`, `agent.effects`, `agent.faults`,
`agent.assertions`, and `agent.limits` fields. Use
`infernosim lint <replay.yaml>` before running cases. Unknown fields and invalid
selectors fail before a simulator or child process starts.

The default case plan is the recorded baseline followed by one case per fault.
Case IDs incorporate the incident and configuration hashes, so IDs change when
the test evidence or safety contract changes. Reference a fault ID in scripts
when a stable human-readable selector is more useful than a stable ID for one
exact fixture revision.

### Incident additions

`mcp.log` and `agent-spans.jsonl` are optional. Existing bundle-v2 commands
already encrypt every regular file within the incident directory, so there is
no bundle format migration. v4 proof and case-scope hashes include these files
when present.

`agent-spans.jsonl` stores only normalized correlation metadata. Re-import the
source OTLP JSON with `infernosim agent otel import`; do not copy raw prompt,
argument, or result attributes into the incident.

### Streaming capture

New captures can preserve bounded response frames and delays for SSE, NDJSON,
and JSON-sequence responses when transformed body capture is authorized. Old
captures without frame metadata continue to replay as a single response body.

### Test generation and image version

`infernosim testgen --profile agent` writes `agent-cases.json` and agent-aware
harness wiring. The generated GitHub Actions workflow extracts the pinned v4
CLI from the container and runs the assertion-aware `agent stress` command;
regenerate older agent workflow experiments before using them as a release
gate.

The default generated container reference is now
`ghcr.io/pranaysparihar/infernosim:4.0.0`. Pass `--image` to pin a different
digest or internal registry.

### Verification assertions

`require_verification_before_retry` may now add `verification_path` and
`verification_value`. With those fields, a verification call counts only when
its delivered 2xx response satisfies the semantic predicate. Without a path,
the backward-compatible rule requires a delivered successful response from the
named verification tool.

See [the complete agent reliability guide](AGENT_RELIABILITY.md) for the
configuration schema, commands, reports, security boundaries, and Ollama
compatibility smoke.

## v3.3 to v3.4

Existing incident directories and `replay.yaml` files remain valid. All v3.4
configuration is opt-in.

### Incident-to-test workflow

`infernosim serve` runs an incident as a standalone dependency simulator with
separate proxy and control ports. `infernosim testgen` can generate a Go
Testcontainers test, Docker Compose configuration, or GitHub Actions workflow.
Generated files are private by default and are never overwritten unless
`--force` is supplied.

### Guarded matcher healing

`infernosim heal` writes proposals to `replay.proposed.yaml`. It does not modify
`replay.yaml` unless `--apply` is explicit, and an applied change first creates
`replay.yaml.bak`. Authentication, tenant, identity, permission, money, status,
and personal-data fields remain protected from automatic relaxation.

### Kafka and cross-protocol workflows

Kafka capture requires an explicit topic list and either a privacy policy or
the explicit unsafe raw-capture override. Policy-based replayable capture must
set `capture_bodies: true`. Broker passwords are read from environment
variables rather than command-line flags.

AsyncAPI validation currently targets AsyncAPI 3.x JSON messages and local
references. Cross-protocol workflow verification can order HTTP, gRPC, and
Kafka observations with optional correlation and timing constraints.

### CI reports

Workflow, AsyncAPI, and Kafka commands can emit JUnit, SARIF, and HTML reports.
Treat those outputs as build artifacts; they are intentionally not attached to
the GitHub release.

## v3.2 to v3.3

Existing incident directories and `replay.yaml` files remain valid. The v3.3
features are opt-in.

### Dynamic responses

Static `body` and `body_base64` responses behave exactly as before. Use
`body_template` only when a scenario response must derive data from the runtime
request. A response may configure only one of `body`, `body_base64`,
`body_template`, `protobuf_json`, or `protobuf_stream`.

Templates use a sandboxed, deterministic function set. They cannot read files,
open sockets, execute commands, or access environment variables. Set
`templates.seed` to preserve generated IDs and timestamps across machines.

### Descriptor-aware gRPC

Captured wire-level gRPC replay still works without schema configuration. Add
`matching.grpc.proto_files` or `matching.grpc.descriptor_sets` only when using
Protobuf field predicates, semantic Protobuf comparison, or synthesized
responses.

Relative schema paths are resolved from the directory containing
`replay.yaml`. Descriptor sets must be binary `google.protobuf.FileDescriptorSet`
files containing their imports.

### CLI additions

- `infernosim generate` creates a reviewable `replay.yaml` from OpenAPI or
  Protobuf schemas.
- `infernosim lint` performs strict parsing plus scenario reachability and
  shadowing checks.
- `infernosim match explain` reports why each captured dependency call matched
  or failed.

Generation refuses to overwrite a file unless `--force` is supplied.

## v3.1 to v3.2

HTTPS dependency stubbing remains opt-in. Applications must trust the
InfernoSIM replay CA, and every MITM hostname must be explicitly allowlisted.
Existing HTTP-only replay configurations continue to work unchanged.

Incident bundle v2 is an encrypted wrapper around the existing directory
layout. Opening a v2 bundle restores an ordinary incident directory; no
permanent migration is required.
