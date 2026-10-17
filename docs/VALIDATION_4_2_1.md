# v4.2.1 validation evidence

All seven community issues (#14–#20) are implemented. The frozen implementation
is `b2736e9f8a26db97263a41ce669b454130a58d6b`. It passed all nine jobs in
[GitHub CI](https://github.com/pranaysparihar/InfernoSIM/actions/runs/39071650478).
The release workflow repeats its quality, integration and application gates
before publication. The source and local verification binaries are hashed in
[provenance](../benchmarks/results/v4.2.1/source-provenance.json).

## Community acceptance

| Issue | Verified behavior |
| --- | --- |
| #14 | Offline default; explicit local Ollama recovery after a tool fault; changed tool arguments; no unknown-tool passthrough or rejoining the old tape; request/token/time limits, cancellation, malformed responses, privacy, hybrid reporting and captured offline replay |
| #15 | Real concurrent HTTP and MCP capture, mixed shared clock, reviewed observed schedules, fresh IDs, progress/cancellation terminals, streamed HTTP CLI round trip, relocation, and rejection of missing seals, gaps, duplicate/unrelated clocks and incompatible orders |
| #16 | Actual destination commit followed by response loss, retry and MCP cancellation; safe/unsafe controls, independent SQLite/history checks, named failure reduction, exact causal proof and relocated replay |
| #17 | Checker executable/script, declared helpers/lockfiles/configuration and contract fingerprints; Python options/module scope; changed/missing/tampered inputs; mutable destination exclusion; explicit new-case lineage and legacy visibility |
| #18 | Runnable fresh-ID retry reproduction through causal discovery/exploration; targeted ordinary-HTTP correlation diagnostic; safe success and named unsafe database failure |
| #19 | Pending original across restart, durable bound handle, failed/stale status reads, active versus expired deduplication, and provider work draining after the client exits |
| #20 | Lost handle with misleading positive/negative lookups, deletion, failed reads, wrong receipt and expiry; preserve unknown, read-only reconciliation after revocation, authoritative identity recovery and active-deduplication controls |

The new suite ran 15 variants at seeds 42100, 42101 and 42102. All **186 safe
runs passed**. Unsafe clients failed **114 runs** and passed **72 controls**
where the required response-loss combination was absent. There were **zero
invalid runs**. The combined retry/cancel case samples 12 schedules per seed
and is explicitly truncated; this is not exhaustive interleaving coverage.
Every variant/seed retained a reduced `state:destination` failure, then replayed
the relocated artifact twice for both safe and unsafe clients with exact causal
proof. Those repeats and minimization attempts are additional to the counts.

The synthetic provider maintains actual SQLite executions, objects, immutable
commit history and audit records in a separate process. The checker reads the
destination independently of simulator counters. Failed/stale/misleading reads
come from the provider's read interface; queued writes settle after client exit.
These checks establish the documented synthetic contract, not a production
provider's deduplication, receipt retention or cancellation guarantees.

The five historical causal scenarios also passed three seeds (7100–7102):
**378 safe passes, 288 unsafe failures, 90 unsafe controls passed, zero invalid
runs**, with all 15 failures reduced and replayed after relocation.

Evidence: [community campaigns](../benchmarks/results/v4.2.1/community-acceptance.json),
[community reproductions](../benchmarks/fixtures/community-v4.2.1/),
[independent destination replay results](../benchmarks/results/v4.2.1/community-replays/),
[historical campaigns](../benchmarks/results/v4.2.1/causal-acceptance.json), and
[historical reproductions](../benchmarks/fixtures/causal-v4.2.1/).
All 60 copied artifacts passed integrity, fingerprint and causal-proof loading.
Reusing an identical checker preserves identity; a different runtime/checker
requires an explicit new case, as described in the fingerprint guide.

## Race, integration, security and packaging gates

- Full root race suite, vet, module consistency, and both nested Go modules.
- Root, MCP SDK and Testcontainers `govulncheck` scans: no affected imported
  packages or called symbols. Testcontainers' required x/crypto module includes
  the unimported OpenPGP advisory GO-2026-5932; it is not used by this adapter.
- Pinned official MCP Go SDK integration and all historical agent,
  safety/recovery and discovery smoke tests.
- Independent official Python SDK consumers: OpenAI 3.29.0, Anthropic 1.13.0,
  Ollama 0.6.3. All 24 baseline/mutation/fragment-size cases passed over HTTP.
- Real local `llama3.1-local:latest`: tool-call envelope plus bounded live recovery
  JSON/NDJSON and measured output-token accounting. This is a compatibility
  check, not a claim that a live model always recovers correctly.
- 100-run category benchmark: stable configuration/harness hashes, behavior
  checks passed and zero fixture secret leaks. Agent benchmark: 100/100 safe
  runs passed and 80/80 unsafe controls were rejected.
- Unprivileged Linux amd64 image, real Testcontainers incident/lifecycle test,
  Redpanda Kafka/AsyncAPI smoke, and native arm64 Node/Go Compose profiles.
- Generated Actions/Compose harness validation, pinned actionlint, shell syntax,
  Homebrew formula generation and GoReleaser configuration checks.
- Final-source GoReleaser snapshot produced exactly eight platform archives;
  every archive matched the checksum manifest.

A Linux CI failure during development exposed a post-write observation race:
a reader can react before the writer's completion callback resumes. Capture
now records delivery initiation before the write and validates completeness
before sealing. Deterministic peer-reaction tests, 50 repeated host round trips,
20 repeated Linux round trips and final CI verify the correction. Captured
boundaries represent proxy observations, not packet arrival or database commits.

## Two-hour soak and fuzzing

The frozen source completed **4,239 cycles and 42,390
application executions over 7,200.3 seconds** on macOS arm64. Every cycle
ran safe and unsafe versions of the five retained causal regressions after
relocation. Named failures, exact schedules and safe outcomes remained stable.
No invalid executions, process timeouts or leaked fixture processes occurred.

| Measurement | Peak growth/result after warm-up | Allowed growth |
| --- | ---: | ---: |
| Go heap after GC | 286.1 KiB | 32 MiB |
| Resident memory | 1.25 MiB | 96 MiB |
| Goroutines | 0 (peak 1) | 8 |
| File descriptors | 0 (peak 6) | 8 |
| Remaining descendants | 0 | 0 |
| Identifiable orphaned fixture processes | 0 | 0 |

The soak started at `2026-10-17T21:37:05.724718Z`. Its two executables were built once
from the frozen source and were not replaced during the run. Earlier interrupted
pre-fix runs do not count. [All samples](../benchmarks/results/v4.2.1/causal-soak.json)
and [source/binary provenance](../benchmarks/results/v4.2.1/source-provenance.json)
are retained. Duration uses Go's monotonic clock, with idle sleep inhibited.
An earlier wall-clock run containing long host pauses is not counted toward
this gate.
This is a two-hour causal runtime soak; the new recovery variants,
live recovery and capture paths have their separate acceptance tests above.

Go 1.27.2 ten-minute fuzz runs passed **5,810,968 planner executions** and
**50,072,872 provider-stream executions**. All ten historical ten-second fuzz
smokes passed. Release builds and race gates use Go 1.26.9. See
[fuzz results](../benchmarks/results/v4.2.1/fuzz.json) and
[provider SDK results](../benchmarks/results/v4.2.1/provider-sdks.json).

## Platform scope

Native unit tests passed on Linux amd64, macOS arm64/amd64 and Windows amd64.
Linux arm64 ran native container/Compose checks on the maintainer host. Linux
arm64/386 CI build targets run their unit tests on the amd64 runner before
cross-building. Windows arm64/386 have release cross-build coverage only.
A built archive is not counted as native execution on that platform.

The supported live mode is local Ollama HTTP chat with buffered responses,
explicit budgets and a new reviewed branch; old causal schedules, stdio and
restart mode cannot be combined with it. Checker dependencies are declared,
not a universal discovered closure. Observed order does not prove independence.
See [live recovery](LIVE_RECOVERY.md), [observed schedules](OBSERVED_SCHEDULES.md),
[checker fingerprints](CHECKER_FINGERPRINTS.md) and the
[recovery contract](../examples/recoverylab/README.md).
