# v4.2 validation evidence

The pre-publication application, SDK, fuzz and two-hour soak gates passed on
October 12, 2026. The implementation passed every job in
[GitHub CI](https://github.com/pranaysparihar/InfernoSIM/actions/runs/38218728040).
The release workflow repeats the race, integration, three-seed application and
security gates before publishing binaries and the container.

## Application consequences

The corrected SQLite/filesystem acceptance campaign uses seeds 7100, 7101 and
7102. Each scenario runs every valid fixture schedule against fixed and broken
clients, with the configured fault combinations. All 378 fixed-client runs
passed. Broken clients failed 288 runs and passed 90 runs where the required
response-loss fault was absent. No runs were invalid. These counts exclude the
additional minimization and retained-regression repeat executions.

| Scenario | Schedules per seed | Cases per client per seed | Fixed passed, three seeds | Broken failures, three seeds |
| --- | ---: | ---: | ---: | ---: |
| Stale write after observed new-value checkpoint | 9 | 18 | 54 | 54 |
| Retry after committed response loss | 15 | 60 | 180 | 90 |
| MCP cancellation and local cleanup | 3 | 6 | 18 | 18 |
| Failed commit and compensation | 15 | 30 | 90 | 90 |
| Crash/restart and duplicate local work | 6 | 12 | 36 | 36 |

Each of the 15 scene/seed combinations produced a completed reduction, repeated
failure on the named `state:database` assertion, and a passing fixed client.
The filesystem oracle is independent of the SQL query; it need not fail for
every defect caught by the database oracle.

The stale-write fixture declares an explicit scenario precondition: release the
old lookup only after the new transaction's checkpoint has been admitted. An
earlier fixture omitted that precondition and allowed an unconstrained internal
application race; repeated validation caught its unstable reduced outcome.
Network release alone is not treated as evidence of application commit.

## Unit, integration and security checks

- Full Go race suite, `go vet`, module consistency and shell syntax checks.
- Independent exhaustive permutation oracle for small dependency graphs; seeded
  plan identity, budget, ambiguity, cycle and missing-participant checks.
- Actual concurrent HTTP and MCP subprocess applications, including one shared
  mixed-transport controller, changed JSON-RPC IDs, notifications and cancellation.
- Pinned official MCP Go SDK v1.7.0: six concurrent boundary orders × three seeds
  × baseline/targeted fault = 36 replays, plus existing SDK tests.
- Four provider stream fixture families, interleaved calls, malformed/truncated
  input, completion validation, unrelated-frame preservation and real HTTP
  delivery with captured chunk fragmentation, trailers and cancellation.
- Independent official Python SDK consumers: OpenAI 3.29.0 (Responses and Chat),
  Anthropic 1.13.0 and Ollama 0.6.3. Four formats × baseline/mutation × three
  server-write sizes = 24 passing checks over loopback HTTP. No live APIs used.
- Full recording metadata and OTel ancestor integrity checks; relocation,
  changed runtime IDs, tampered manifests/files and large graph manifests.
- Historical agent, safety/recovery and discovery smokes; local Ollama
  `llama3.1-local:latest` compatibility smoke.
- 100-run incident benchmark and 20-iteration agent benchmark: 100/100 safe
  executions pass and 80/80 unsafe controls are rejected.
- Unprivileged Linux arm64 container, real Testcontainers lifecycle, Node and
  Go Compose smokes, Kafka/AsyncAPI against Redpanda, generated harness validation.

The release uses Go **1.26.9**, `golang.org/x/net` **v0.60.0** and required x/sys,
x/sync and x/text updates. The Testcontainers adapter also updates
`github.com/moby/go-archive` to v0.3.0 and `golang.org/x/crypto` to v0.56.0,
fixing its reachable archive/SSH advisories. The main module, pinned MCP SDK
integration and Testcontainers adapter all pass `govulncheck`; both nested
scans now run in CI and the release workflow. This replaces the earlier pins after the
October 8 security advisories, including the HTTP/2 HPACK encoder race.
Sources: [Go release history](https://go.dev/doc/devel/release#go1.26.9),
[GO-2026-6617](https://pkg.go.dev/vuln/GO-2026-6617).

## Soak and fuzz gates

The relocated five-scenario suite passed **2,507 complete cycles and
25,070 application executions over 7,201.6 seconds** on macOS arm64.
Every cycle ran both fixed and broken clients. Every broken run retained the
same named failure and exact observed schedule; every fixed run passed. There
were no invalid runs, timeouts or changed outcomes.

Resource samples after the three-cycle warm-up stayed within the thresholds
declared in `cmd/causalbenchmark/soak.go` before the run:

| Measurement | Observed peak growth/result | Allowed growth |
| --- | ---: | ---: |
| Go heap after GC | 248.0 KiB | 32 MiB |
| Resident memory | 0.77 MiB | 96 MiB |
| Goroutines | 0 (peak 1) | 8 |
| File descriptors | 0 (peak 6) | 8 |
| Remaining descendants | 0 | 0 |
| Identifiable orphaned fixture processes | 0 | 0 |

The source provenance records the 35 unchanged causal/runtime/scenario files
used by the soak. The runner remained unchanged. The MCP connector was rebuilt
eight seconds into warm-up, before the cycle-three resource baseline; both
initial and measured connector hashes are retained. Its MCP forwarding source
did not change. A provider-parser repair made after soak launch is outside
these five scenarios and is covered separately by the final fuzz, race and
independent SDK checks. The soak is evidence for the causal application runtime,
not a two-hour test of every provider adapter.

Both new fuzz targets passed ten minutes on Go 1.26.9: 3,496,670 planner
executions and 51,860,172 provider-stream executions. The latter found an
empty-argument/empty-delta mutation bug in an earlier run; the exact minimized
input is checked in and the passing count comes from the run after its fix.
The ten historical fuzz targets also passed their ten-second smoke runs.

Raw evidence: [application campaigns](../benchmarks/results/v4.2/causal-acceptance.json),
[retained regressions](../benchmarks/fixtures/causal-v4.2/),
[soak samples](../benchmarks/results/v4.2/causal-soak.json),
[source and binary provenance](../benchmarks/results/v4.2/causal-soak-provenance.json),
[fuzz results](../benchmarks/results/v4.2/fuzz.json), and
[independent provider SDK checks](../benchmarks/results/v4.2/provider-sdks.json).

## Platform interpretation

| Target | Completed release-candidate validation |
| --- | --- |
| macOS arm64 | Native full/race suite, SDK, SQLite campaign and two-hour soak on the maintainer host; native CI |
| macOS amd64 | Native CI unit suite and CLI build |
| Linux amd64 | Native CI unit/race suites, SDK, SQLite campaign, container and historical integration gates |
| Linux arm64 | Native Docker runtime smokes on the arm64 maintainer host; CLI cross-build in CI |
| Linux 386 | CLI cross-build; CI host tests run as amd64, not native 386 |
| Windows amd64 | Native CI unit suite and CLI build; no Job Object descendant-containment claim |
| Windows arm64/386 | Release cross-build only |

A cross-built archive is not counted as native execution. Protocol fixtures do
not imply live provider-SDK or full MCP conformance. See the exact
[support matrix and limits](CONCURRENCY_4_2.md).
