# Convert a real capture into a replay schedule

Record an explicitly supplied test application, optionally with a test MCP
server. The application must use the proxy environment and the MCP connector
argv in `INFERNOSIM_MCP_COMMAND_JSON`:

```bash
infernosim agent causal record ./incident \
  --capture-sensitive-data --allow-private-destinations \
  --mcp-command-json '["./test-mcp-server"]' -- ./test-app
infernosim agent causal convert ./incident \
  --declarations ./reviewed-declarations.json --out ./observed.json
infernosim agent explore ./incident --observed-schedule ./observed.json \
  --report-dir ./observed-results -- ./test-app
```

Use synthetic data with `--capture-sensitive-data`, or supply a privacy policy
with `capture_bodies: true` through `--privacy-policy`. Header redaction remains
the existing capture policy. MCP JSON is transformed before storage, while the
original message is forwarded. A transformation that removes correlation or
required payload information may make the recording unreplayable; conversion
fails rather than reconstructing missing values. No command is read from data.

This recording entry point supports HTTP and one newline-delimited MCP stdio
session in one runner process. HTTPS CONNECT tunneling is not an HTTP body
capture. Existing standalone HTTP/MITM recording remains available, but old
exchange timestamps/durations cannot prove exact mixed boundary order.

HTTP admissions are observed before forwarding; releases are observed after
the complete response write and flush succeeds. MCP observations bracket each
complete message write. These callbacks share one monotonic observation counter
and session ID. A sealed session identifies missing observations. Standalone MCP recordings
retain this seal in `mcp.log.capture-session.json`; keep that sidecar with the
transcript. Unsealed captures remain partial. They describe
the proxy's observation points, not OS scheduling, socket packet arrival, client
processing or internal database commits. A client may begin processing bytes
before the writer observes completion. Conflicting reviewed dependencies are
diagnosed instead of rewriting the observation order to fit them.

Conversion retains source references/digests, all participants and the reviewed
graph. It validates correlation, complete responses, stable identities and the
observed order against the graph. For a cancelled MCP request without a response,
its terminal boundary is explicitly derived from the forwarded cancellation;
it is not represented as a response that arrived. Progress/notification
participants retain their own observed admission and release boundaries.

An exact result is labelled `exact-proxy-observation` and includes an explicit
schedule. Missing stamps, incomplete sessions, duplicate counters or independent
capture clocks produce an inspectable `partial` result and exit 1. Per-session
boundaries are retained; no guessed total order is exported. Clock skew and
timestamp ties cannot be resolved by sorting wall-clock time. Malformed
responses, conflicting dependencies and cycles fail explicitly.

`--observed-schedule` preserves this baseline while exploring configured faults.
Use `--causal-graph` separately to generate alternate orders. Chronology does
not become causal edges, and a missing edge is not proof of independence. A
seed alone is never the captured replay artifact. Retained failures use the
existing portable reproduction format and integrity checks; callers still
supply application/setup/check commands when replaying elsewhere.

Acceptance tests record real concurrent HTTP and subprocess MCP exchanges,
convert and replay them, then relocate and replay the retained artifact with
fresh runtime request IDs. Tests also cover missing/independent clocks, capture
gaps, duplicate counters and progress/cancellation terminals.
