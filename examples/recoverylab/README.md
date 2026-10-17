# Destination recovery regressions

These fixtures cover issues #16, #19 and #20 with a separate Python provider
process and a real SQLite destination. They do not assert real-provider or
production exactly-once guarantees. `contract.json` defines the synthetic
receipt, visibility, deduplication, cancellation and observation contracts.

Build the CLI, then run:

```bash
go build -o /tmp/infernosim-421 ./cmd/agent
go run ./cmd/communitybenchmark -binary /tmp/infernosim-421 \
  -iterations 3 -schedule-budget 12 -out /tmp/community-421
```

The output directory must be new. The benchmark retains each result, destination
timeline, plan, checker fingerprint and minimized reproduction, and repeats
relocated failures against safe/unsafe applications with fresh runtime IDs.
The retry/cancel campaign samples bounded valid schedules and reports truncation;
it does not claim exhaustive application interleavings.

| Contract | Destination behavior and controls |
| --- | --- |
| Committed write, response loss, retry and cancellation | The provider commits in its own process before the caller's observable response checkpoint. HTTP response loss races recovery with MCP cancellation. Safe retry uses the existing operation or stops after cancellation; unsafe retry creates a distinct duplicate. Cancellation never rolls back the original commit. |
| Pending original across restart | A stable handle, provider/account/environment scope, request fingerprint and business identity survive the runner's process kill. Pending, failed/stale status reads, active same-key retry and expired-key cases preserve uncertainty until a bound receipt is available. The unsafe client queues a second execution. |
| Lost original handle | Matching content, delayed visibility, object deletion, failed reads, key expiry and another execution's receipt do not identify the original. Safe recovery preserves unknown and stops unprotected new mutations. Separate controls recover identity through an explicitly authoritative index or active deduplication. Revoked mutation authority has its own case. |

The observation harness drains all pending provider work **after the client
process exits**. The independent checker then opens the destination read-only.
It checks immutable historical effects as well as current state: deleting an
object cannot erase proof that the original mutation happened. It reports
destination duplicates, authorization violations and outcome knowledge as
separate named assertions. Application stdout and simulator counters are not
the oracle.

The application harness prints synthetic destination timelines for retained
evidence. The checker never trusts that output; it reads `destination.sqlite`
and the client's persisted decision. Pre-cancellation admissions may finish.
Cancellation revokes new mutation admission while read-only reconciliation
remains allowed. Active deduplication returns the same execution, including
pending executions. Expiry disables deduplication and does not fence pending
work. Receipts/history remain available for the fixture's entire lifetime.

Commands are always explicit argv. The checker manifest pins `checker.py`, its
provider helper and the contract; the destination database is deliberately not
a checker dependency. See [checker fingerprinting](../../docs/CHECKER_FINGERPRINTS.md)
for the declared dependency scope and runtime limitations.

The lost-response fault is necessary for the combined unsafe duplicate; baseline
cases without that fault should pass. The pending/lost-handle unsafe controls
are deliberately broken and should fail. Missing checkpoints, unexercised
restarts, process errors, bad checks and impossible schedules are invalid runs,
never successful application counterexamples.
