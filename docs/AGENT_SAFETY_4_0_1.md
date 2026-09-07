# Agent safety and recovery in 4.0.1

This release extends the local simulator, not a hosted monitoring service.
The deliverable is a reproducible counterexample and CI gate for a configured
incident. It is not proof that an arbitrary agent is safe in production.

## Try the complete refund/rollback example

The fixture is synthetic: no payment account, cloud subscription, API key, or
model server is involved. Run from the repository root:

```bash
go build -o /tmp/infernosim ./cmd/agent
go build -o /tmp/reliabilitylab ./examples/reliabilitylab
/tmp/reliabilitylab --init /tmp/refund-safety-incident

/tmp/infernosim agent run /tmp/refund-safety-incident \
  --report-dir /tmp/refund-safe -- /tmp/reliabilitylab --mode safe

/tmp/infernosim agent run /tmp/refund-safety-incident \
  --report-dir /tmp/refund-unsafe -- /tmp/reliabilitylab --mode unsafe
```

Initialization refuses to overwrite an existing directory. Use another path
when repeating the example. The safe baseline exits 0. The unsafe application
itself exits normally, but InfernoSIM exits 1: it finds a missing approval,
wrong tenant, duplicate refund, missing compensation, stale state estimate,
and excessive recorded token/configured cost usage. The checks are independent
of what the application prints.

The complete configuration is embedded from
[`examples/reliabilitylab/replay.yaml`](../examples/reliabilitylab/replay.yaml).
Its deliberately relaxed request matching allows synthetic bad arguments to
reach the safety assertions; do not copy those ignored-field rules blindly
into a production incident.

Run `bash scripts/reliability-smoke.sh` to verify both controls, monitor faults,
recovery, comparison, reduction, and report generation automatically.

## Monitor failures and bad state estimates

`monitor_sound` separates the simulated effect ledger from the monitor's
response and, optionally, its request-side state estimate:

```yaml
- id: honest-monitor
  type: monitor_sound
  effect: refund
  tool: monitor
  path: $.result.healthy
  expected: true
  version_path: $.result.version
  estimate_path: $.arguments.version
  require_exercised: true
```

The declared effect is the unsafe event for this assertion. A successful
monitor response must contain a verdict; once that effect has committed, a
healthy verdict fails. If configured, both version paths must equal the number
of prior committed instances of that effect since reset. Missing, stale, or
future versions fail. `estimate_path` reads the request root described below.
The simulator does not infer ground truth from the monitor's own output.

This is a deliberately bounded state model. The unsafe event remains part of
history even after compensation; this assertion is not a general-purpose
materialized database or production telemetry collector. Configure separate
effects/assertions for different state transitions.

```bash
# Both commands intentionally exit 1: the injected monitor is defective.
/tmp/infernosim agent run /tmp/refund-safety-incident --case false-healthy \
  --report-dir /tmp/false-healthy -- /tmp/reliabilitylab --mode safe
/tmp/infernosim agent run /tmp/refund-safety-incident --case stale-monitor \
  --report-dir /tmp/stale-monitor -- /tmp/reliabilitylab --mode safe
```

Existing `delete`, `set`, status, and transport mutations can simulate missing
telemetry, stale revisions, and false-clean verdicts. A detected monitor fault
is a failing control even when the application otherwise behaves safely.

## Approval, isolation, compensation, and budgets

| Assertion | Contract and important limits |
| --- | --- |
| `approval_before_effect` | Requires a prior successful `verification_tool` response matching `verification_path`/`verification_value`. `bind` paths must exist on both requests and hash equally. Each approval is consumed once. `max_age_calls` expires it after the configured number of processed exchanges. Requires a declared `effect`. |
| `request_matches` | For `tool`, require request `path` to equal `expected`; missing values fail. Useful for synthetic tenant, audience, credential, and state-estimate fixtures. |
| `compensated_effect` | Each committed `effect` needs a later committed `compensation` with the same identity hash. One compensation cannot satisfy two effects. Both effects must use the same identity-path definitions. |
| `max_total_calls` | Shared processed-exchange count must not exceed `max`. One provider envelope containing several tool proposals is one exchange, not several executions. |
| `max_tokens` | Sum known usage from original, pre-fault recorded provider envelopes. Unknown usage fails, rather than counting as zero. Requires both input and output counts when no total is present. |
| `max_cost` | Multiply recorded tokens by explicit integer `price_per_token_microunits`; compare with `max` microunits. This is a configured estimate, not provider billing, currency conversion, or live price discovery. |
| `no_calls_after_cancel` | Reject a new matching tool/request ID after an observed MCP cancellation. Already admitted work may still commit; cancellation is not rollback. |

Request predicates and bindings use a root containing `arguments`, `body`, and
`headers`. Header names are lowercase and multiple values are comma-joined.
Bind every authorization-relevant field, including tenant, operation identity,
and amount. Missing identity/deduplication fields fail closed; large integer
request values retain their precision when constructing evidence hashes.

These are test assertions, not authorization enforcement. A bad effect is
recorded and the test fails; InfernoSIM does not silently block it and declare
the application safe. Approval fixtures do not replace a human approval
system, JWT verifier, OAuth issuer, or Entra deployment. Expiry is a logical
exchange window, not a wall-clock token lifetime. To test expired credentials,
record a synthetic rejecting auth service and assert that no protected effect
commits. Never use real credentials in this demo.

## Honest safety-path coverage

Assertion JSON and terminal output distinguish `exercised`, `violated`, and
`not_exercised`. For example, a retry-verification assertion is not exercised
if there was no ambiguous retry. Approval/compensation checks without the
relevant effect, a missing monitor call, and token checks without provider
traffic are also not exercised.

Set `require_exercised: true` when reaching that path is part of the test. The
case then fails if the path is absent, with coverage still `not_exercised`.
Existing configurations default to the previous permissive behavior. Coverage
is tied to observable configured predicates, not source-code branch coverage.
Assertion details appear in JSON, terminal output, and HTML/JUnit case messages.

## Crash after commit, then recover

```bash
/tmp/infernosim agent run /tmp/refund-safety-incident \
  --restart-after-call 2 --report-dir /tmp/refund-recovery \
  -- /tmp/reliabilitylab --mode safe
```

The first call obtains approval; the second commits the simulated refund.
Before delivering that response, the runner kills the application and starts
the same explicit command again. The simulator's ledger, fault occurrences,
and deduplication state survive. The response is marked lost. Recovery verifies
the committed refund and compensates it instead of issuing a second refund.

The child receives `INFERNOSIM_ATTEMPT` (`0`, then `1`) and a private
`INFERNOSIM_CHECKPOINT_DIR`, retained across the two attempts and removed after
the run. The application must implement its own checkpoint/recovery protocol;
InfernoSIM cannot checkpoint arbitrary application memory. One restart maximum;
the timeout covers both attempts. An unreached crash boundary fails the case.

Linux/macOS terminate the command's process group. Windows terminates the
direct child only. This is not power-loss/fsync testing, container restart,
distributed database recovery, or Windows descendant containment.

## Parallel calls, explicit schedules, and combinations

Fault selectors can now use `call_id`. All tool calls in complete OpenAI
Responses/Chat Completions, Anthropic, and Ollama JSON envelopes are eligible
for selection. Mutation paths still address the original full response: a
selector does not automatically rewrite a JSONPath to the chosen tool item.
Incremental streamed tool-call assembly is not claimed.

```yaml
agent:
  # Existing adapters/effects/faults/assertions remain here.
  exploration:
    pairwise: true
  schedules:
    - id: b-before-a
      timeout: 2s
      steps:
        - {kind: mcp_tool_result, tool: lookup, call_id: b}
        - {kind: mcp_tool_result, tool: lookup, call_id: a}
```

This complete admission schedule makes `a` wait until `b` is processed at the
simulator boundary. IDs must be explicit and unique within the schedule.
Unknown calls, missing participants, extra calls, and schedule timeout fail
closed. The configured steps cover the entire run; use a fixture that actually
issues both requests concurrently, or the deliberately blocked first request
will time out. `serve --agent-schedule b-before-a` selects the same mechanism.

The plan contains baseline/singles, schedule-only cases, optional fault pairs,
and schedule-plus-single-fault cases, subject to `--budget` and `max_cases`.
It is bounded enumeration, not exhaustive interleaving or pair coverage.
Faults compose in configuration order; transport failures cannot be erased by
a later mutation, and cumulative delay cannot exceed 60 seconds.

Case IDs include the evidence/config scope, seed, fault membership, and
schedule. No schedule means actual arrival order is observed, not magically
made deterministic. A selected schedule controls simulated admission/commit
order, not OS threads, socket delivery, external databases, or every race in an
agent framework. SDKs must emit the configured IDs for scheduled HTTP tests.

## Compare application versions and reduce failures

```bash
/tmp/infernosim agent compare /tmp/refund-safety-incident --budget 1 \
  --baseline-command-json '["/tmp/reliabilitylab","--mode","unsafe"]' \
  --candidate-command-json '["/tmp/reliabilitylab","--mode","safe"]' \
  --report-dir /tmp/refund-comparison

/tmp/infernosim agent cases /tmp/refund-safety-incident --json
/tmp/infernosim agent reduce /tmp/refund-safety-incident \
  --case false-healthy --assertion honest-monitor \
  --out /tmp/refund-reduction.json -- /tmp/reliabilitylab --mode safe
```

Compare executes both explicit argv arrays against identical evidence, cases,
and schedules. It reports candidate failures, pass/fail changes, disappearing
assertions, and lost safety-path coverage. Candidate failures or regressions
exit 1, including coverage regressions in JUnit. JSON argv is never implicitly
executed through a shell. Reports and child output are private, but command
output can still contain application secrets.

Reduction preserves one named violated assertion on two consecutive runs per
accepted candidate. It removes fault membership only, retaining the schedule
and original assertions. `complete: true` means a one-minimal fault set, not a
globally minimal trace or root-cause proof. Exhausting the run budget reports
an incomplete reduction and exits 1. Process failures are not accepted as the
target assertion. Record model envelopes for this gate; live-model behavior
may be nondeterministic and cannot justify a deterministic comparison claim.

## MCP compatibility matrix

- HTTP: request-level selectors and explicit admission schedules.
- Stdio: recorded client order with correctly correlated out-of-order server
  responses, runtime ID rewriting, cancellation-ID rewriting, initialized and
  tool-list-change notifications, errors, and incomplete-transcript rejection.
- Official Go SDK **v1.7.0**: a real local SDK server is recorded through the
  stdio proxy; a real SDK client initializes, lists tools, and calls a tool
  against the recording and mutated replay, then connects to a fresh replay.
- Raw protocol tests separately cover parallel response correlation and
  cancellation. The SDK test is not an all-framework concurrency certification.

Stdio still follows recorded client message order, not arbitrary request
permutation. Effects/verifications there are evaluated at the simulated
response-decision boundary, not physical side-effect or socket-ack time.
Server-initiated requests (sampling/elicitation), arbitrary session resumption,
binary framing, and full MCP OAuth conformance are outside this matrix. A
fresh replay connection is tested; resuming arbitrary server state is not.

Use OS/container network isolation when testing untrusted agents. Proxy
environment variables alone are not a network sandbox, and the simulated
ledger is not an audit of a live payment provider. Existing HTTP/HTTPS, gRPC,
OpenAPI, Kafka, privacy, bundle, and matcher-healing features remain available.
