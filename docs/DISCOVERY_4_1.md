# InfernoSIM v4.1: discover, check, minimize, reproduce

v4.1 adds bounded exploration around an existing recorded dependency universe,
independent application-state checks, and portable regression reproductions.
The same runner accepts any explicit application command; a live model or
agent framework is not required. Existing v4.0.1 commands/configurations remain
compatible. `agent` is the CLI namespace for this consequence-testing runner.

## Runnable demonstration

```bash
bash scripts/discovery-smoke.sh
```

The synthetic application in `examples/discoverylab` uses a simulated payment
service with idempotent refunds, and maintains a real local JSON outbox. A lost
response causes its unsafe implementation to write a duplicate outbox entry.
The simulated refund ledger passes; an independent process reads the outbox
and fails `state:outbox-once`. The fixed application passes the same case.
The smoke explores eight cases (baseline plus all subsets of three faults),
reduces the noisy three-fault failure to response loss alone, relocates the
artifact, verifies both application versions, and rejects altered evidence.

To inspect the individual commands:

```bash
go build -o /tmp/infernosim-v41 ./cmd/agent
go build -o /tmp/discoverylab ./examples/discoverylab
/tmp/discoverylab --init /tmp/discovery-incident

/tmp/infernosim-v41 agent explore /tmp/discovery-incident \
  --max-faults 3 --budget 100 --seed 42 \
  --setup-command-json '["/tmp/discoverylab","--setup"]' \
  --check-command-json '["/tmp/discoverylab","--check"]' \
  --check-ids outbox-once --report-dir /tmp/discovery-report \
  -- /tmp/discoverylab --mode unsafe
```

The exit code is 1 because the deliberately unsafe application fails. Inspect
`exploration.json` for valid failures, invalid runs, per-assertion exercise
counts, and reproduction paths. `plan.json` contains the exact bounded plan.
JUnit, SARIF, and standalone HTML are also emitted. Report destinations must
be new directories. Reports are updated after each completed case.

## What exploration changes

`agent explore` generates baseline, individual fault, schedule, fault subset,
and selected occurrence variations. The seed determines the stable fault
traversal order; equivalent inputs and seed produce identical case IDs.

| Option | Bound and behavior |
| --- | --- |
| `--budget` | 1–1000 application executions, including baseline |
| `--max-faults` | Subsets of up to 1–4 declared faults; default 3 |
| `--occurrences N` | Also target positions 1 through N; 0–16, default 0 retains configured selectors |
| `--permute-schedule ID` | Opt in to permutations of one existing schedule containing at most six independent calls |
| `--plan-only` | Write the plan without running the application or hooks |
| `--timeout` | Per-case application timeout, up to 10 minutes |
| `--restart-after-call` | Retain an explicit process-restart boundary across trials/reproductions |

At most 32 faults can be declared for exploration. Individual faults are
planned before spending the budget on schedule permutations and combinations.
If candidates remain after the case budget, `truncated: true` is reported.
Passing a truncated plan means only its executed cases passed. This release
uses deterministic enumeration, not adaptive coverage-guided fuzzing.

Permutation is explicit because only the application author knows which calls
can be reordered. Admission order is controlled at the simulator boundary;
response delivery, application threads, and arbitrary OS races are not.
Missing participants, untriggered selected faults, and recorded-universe
divergence are invalid runs, not valid counterexamples. No live-network
fallback invents a continuation after recorded model responses diverge.

The `partial: true` schedule option constrains just the listed calls in order.
Other calls can proceed, but every listed participant must still be exercised.
It is used by minimization to remove unnecessary ordering constraints. The
default remains a complete schedule, preserving v4.0.1 behavior.

## Independent state checks

`run`, `stress`, `compare`, `reduce`, `explore`, `reproduce`, and `minimize`
accept explicit setup/check argv JSON arrays. These commands are never read
from incident YAML and never implicitly evaluated through a shell.

- `--setup-command-json`: initialize/reset the external test state before every
  application execution, including each minimization attempt.
- `--check-command-json`: inspect actual test state after the application exits.
- `--check-ids`: the exact required assertion IDs, separated by commas.
- `--check-timeout`: 1ms–1m per hook; default 10s.

A check must exit 0 and write one JSON document to stdout:

```json
{"assertions":[{"id":"outbox-once","passed":false}]}
```

False is a valid observed violation. Missing, duplicate, unknown, or null
assertions, nonzero exit, malformed output, output over 64 KiB, and timeout
make the check invalid. Reports retain IDs and booleans, not hook stdout or
stderr. `state:` is reserved for these independent assertions.

Each case gets a private `INFERNOSIM_STATE_DIR`, shared by setup, application,
restarted application, and check, and removed when that case finishes.
`INFERNOSIM_CASE_ID` is also provided. Hooks use the caller's ordinary network
environment, so they can query a user-managed test database or broker; they do
not receive the simulator proxy variables automatically. Hooks are explicit
local programs, not a security sandbox. The application still receives its
normal replay proxy and checkpoint environment.

Use setup to reset external state; InfernoSIM cannot infer database cleanup.
Run campaigns against isolated test systems. Do not depend on temporary port
numbers or case IDs as business inputs. Application stdout/stderr retains the
existing private-output behavior and can contain application secrets.

Comparisons bind setup/check argv, required IDs, and hook timeout into the
execution scope. A command path alone does not fingerprint the executable or
its database contents; pin these inputs in your own CI.

## Minimize and reproduce

Exploration exports valid assertion failures under
`<report-dir>/reproductions/<case-id>/`. Supply explicit commands again:

```bash
/tmp/infernosim-v41 agent minimize /tmp/discovery-report/reproductions/CASE_ID \
  --out /tmp/minimal-outbox --assertion state:outbox-once --budget 100 \
  --setup-command-json '["/tmp/discoverylab","--setup"]' \
  --check-command-json '["/tmp/discoverylab","--check"]' \
  --check-ids outbox-once -- /tmp/discoverylab --mode unsafe

/tmp/infernosim-v41 agent reproduce /tmp/minimal-outbox \
  --setup-command-json '["/tmp/discoverylab","--setup"]' \
  --check-command-json '["/tmp/discoverylab","--check"]' \
  --check-ids outbox-once -- /tmp/discoverylab --mode safe
```

`minimize` removes faults, removes the whole schedule or individual ordering
participants, and lowers overridden occurrence positions. It preserves the
named assertion on two consecutive valid executions of every accepted
candidate. Process failures and invalid replays cannot stand in for that
assertion. `complete: true` means no offered simplification preserved the
failure; it does not mean globally shortest trace. Payloads, application
inputs, and fault durations are not minimized in this release. Budgets are
2–1000 executions, and incomplete reduction exits 1 with its best artifact.
Reduction metadata is in `<out>.minimization.json` beside the artifact.

`reproduce` is a regression gate: the buggy application exits 1, a fixed
application exits 0. It never executes a command embedded in the artifact.
The original expected state-check IDs and setup requirement must be supplied.
The caller is responsible for supplying the same check implementation.

Artifacts copy the incident plus normalized replay configuration, exact trial,
check contract, timeouts, and restart boundary. They are limited to 64 MiB and
1000 files, reject symlinks/nonregular files, never overwrite an existing
artifact, and verify SHA-256 file checksums before reproduction. Checksums
detect changed evidence, not the authenticity of an untrusted publisher.
Internal Protobuf paths are relocated; external schema dependencies and
explicit CA/state-file paths are rejected rather than silently producing a
machine-dependent reproduction. Bring those dependencies inside the incident
or use explicit setup/check hooks. Reproductions contain incident data and
are private artifacts, not automatically redacted or encrypted. Use existing
privacy policies before capture and bundle encryption when sharing.

## Scope

This release expands the existing HTTP/MCP simulator-boundary runner. It does
not add full MCP conformance, incremental streamed tool-call assembly,
interactive bidirectional gRPC, virtual application clocks, checkpoint-based
time travel, or exhaustive race exploration. Those remain separate work.
The public demonstration uses local outbox files; it does not claim a tested
integration with every database or broker.
