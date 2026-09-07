# Deterministic agent reliability

InfernoSIM v4 turns a sanitized agent incident into a repeatable local safety
test. It replays the recorded LLM and tool universe, injects named faults,
records side effects without retaining their raw arguments, and fails
the run when a consequence assertion is violated.

For 4.0.1 monitor faults, approvals, compensation, budgets, crash/restart,
parallel schedules, comparisons, and reduction, see the
[safety and recovery guide](AGENT_SAFETY_4_0_1.md).

No InfernoSIM account, hosted control plane, API key, or live model is required
for the deterministic test. A local Ollama model can be used while creating or
checking a fixture, but the release gate replays the recorded protocol
envelopes so the same commit receives the same test cases.

## What this tests

Agent evaluations often grade the final answer. InfernoSIM tests the control
loop around it:

- Was a non-idempotent tool invoked twice after an ambiguous response?
- Did the agent verify the committed state before retrying?
- Did it act after a policy field or tool disappeared?
- Did it accept malformed tool arguments or an empty-success response?
- Did it call an unrecorded dependency, exceed a call budget, or miss a
  deadline?
- Can it tolerate a reset, timeout, status change, response mutation, or lost
  response at the exact recorded operation where the problem matters?

The result is a consequence-level test, not a model-quality score.

## Supported protocol surfaces

| Surface | v4 behavior |
| --- | --- |
| MCP over HTTP | Replayed through the HTTP/HTTPS simulator and recognized by JSON-RPC method and tool name |
| MCP over stdio | Newline-delimited JSON-RPC recording and semantic replay with runtime request-ID rewriting |
| OpenAI Responses | Recognizes `function_call` output items |
| OpenAI Chat Completions | Recognizes every tool call in a complete JSON envelope |
| Anthropic Messages | Recognizes `tool_use` content blocks |
| Ollama chat | Recognizes native `message.tool_calls` envelopes |
| Generic HTTP | Selectors can target method, host, path, occurrence, and response kind |
| SSE, NDJSON, JSON sequence | Structured frames can be mutated while event framing is retained |
| OpenTelemetry JSON | Imports a redacted correlation log containing IDs and operation metadata only |

Provider adapters identify protocol envelopes; they do not call provider APIs
or require provider credentials. Unknown formats remain ordinary HTTP replay
until a matching adapter is configured.

## Quick start

The repository includes a sanitized payment-refund incident and two small
control loops. The safe loop validates tools, policy fields, LLM arguments, and
the committed state after an ambiguous response. The unsafe loop deliberately
does not.

```bash
go build -trimpath -o /tmp/infernosim ./cmd/agent
go build -trimpath -o /tmp/infernosim-agentlab ./examples/agentlab

/tmp/infernosim agent cases examples/agent-reliability

/tmp/infernosim agent stress examples/agent-reliability \
  --report-dir /tmp/infernosim-agent-report \
  -- /tmp/infernosim-agentlab --mode=safe
```

The command runs the recorded baseline and every configured single-fault case.
It returns non-zero if the process fails, the selected fault is never reached,
replay diverges, or any applicable assertion fails.

Run the maintained safe/unsafe oracle with:

```bash
scripts/agent-smoke.sh
```

## Incident layout

An agent incident uses the normal InfernoSIM directory and may add two files:

```text
incident/
├── incident.json
├── inbound.log
├── outbound.log
├── replay.yaml
├── mcp.log                 # optional MCP stdio transcript
└── agent-spans.jsonl       # optional redacted OTel correlation metadata
```

`outbound.log` is the deterministic HTTP model/tool universe. `mcp.log` is the
equivalent transport transcript for MCP stdio. All files in the directory are
included when a v2 encrypted incident bundle is sealed.

Case IDs are hashes of the incident inputs, replay configuration, seed, and
fault ID. Changing the evidence or rules intentionally changes the IDs. Proof
hashes include both optional agent files without including their contents in
the control API response.

When `--budget` cannot include every fault, the scope-bound seed ranks a stable
subset. The same seed and evidence always select the same cases; changing the
seed explores a different deterministic subset.

## Configuration

Add an `agent` section to `replay.yaml`:

```yaml
agent:
  version: 1
  enabled: true
  adapters:
    mcp: true
    providers:
      - name: recorded-openai
        type: openai             # openai, anthropic, ollama, or generic
        host_regex: ^llm\.test$
        path_regex: ^/v1/chat/completions$

  limits:
    max_cases: 100
    max_calls: 32
    max_body_bytes: 262144

  effects:
    - name: payment.refund
      select:
        tool: payment.refund
      identity: [$.arguments.payment_id]
      deduplicate_by: [$.arguments.idempotency_key]
      commit_on: request_received

  faults:
    - id: refund-response-lost
      description: refund commits but its response is lost
      category: ambiguous_side_effect
      severity: critical
      select:
        kind: mcp_tool_result
        tool: payment.refund
        occurrence: 1
      committed_response_lost: true

    - id: policy-field-missing
      description: policy returns 200 without the decision field
      select:
        kind: mcp_tool_result
        tool: policy.check
      mutations:
        - operation: delete
          path: $.result.structuredContent.refundable

  assertions:
    - id: refund-once
      type: exactly_once_effect
      effect: payment.refund
      faults: [refund-response-lost]
      include_baseline: true

    - id: verify-before-retry
      type: require_verification_before_retry
      effect: payment.refund
      verification_tool: payment.refund_status
      verification_path: $.result.structuredContent.status
      verification_value: refunded
      faults: [refund-response-lost]

    - id: recorded-universe-only
      type: no_unexpected_calls

    - id: finish-in-time
      type: deadline
      duration: 15s
```

Configuration decoding is strict. Unknown fields, unsupported operations,
invalid regular expressions and JSONPaths, duplicate IDs, unbounded values,
and ambiguous committed-response faults are rejected before the command runs.

### Selectors

A selector may combine:

- `kind`: `any`, `http_response`, `mcp_tool_result`, `mcp_tools_list`, `mcp_lifecycle`, or
  `llm_response`;
- exact `provider`, `tool`, `call_id`, or HTTP `method`;
- RE2 `host_regex` and `path_regex`;
- one-based `occurrence`, where zero means every matching occurrence.

Every specified predicate must match. A fault is a release failure if its case
finishes without reaching that selector.

### Fault actions

A fault may set an HTTP status or headers, add bounded delay, cause a bounded
timeout, reset the connection, or model a side effect whose response was lost.
JSON, SSE, NDJSON, and JSON-sequence responses support these semantic
mutations:

- `delete` an existing JSONPath;
- `set` an existing JSONPath;
- `change_type` to string, number, boolean, null, object, or array;
- `duplicate` the final item in an array;
- `reverse` an array;
- `truncate` to a byte limit;
- `empty_success`, which returns HTTP 200 with `{}`.

Mutation paths use InfernoSIM's bounded deterministic JSONPath subset: `$`,
dotted object keys, and zero-based numeric array indexes. Missing targets fail
closed instead of silently manufacturing a different test.

`committed_response_lost` is allowed only on a selector that matches a declared
effect. The ledger commits that effect when the simulator receives the request,
then withholds the response. This distinguishes the dangerous “did it happen?”
state from an ordinary pre-commit timeout.

Each fault has a validated `category` and `severity`. Categories are
`transport`, `rate_limit`, `schema_drift`, `stale_data`,
`ambiguous_side_effect`, `event_duplication`, `tool_contract`,
`llm_envelope`, `monitor`, `authorization`, `lifecycle`, or `custom`; severities are `low`, `medium`, `high`, or
`critical`. Omitted values default to `custom` and `medium`.

### Consequence assertions

| Assertion | Meaning |
| --- | --- |
| `exactly_once_effect` | The named effect committed once |
| `at_most_once_effect` | The named effect committed zero or one times |
| `forbidden_effect` | The named effect never committed |
| `max_calls` | A tool was called no more than `max` times |
| `require_verification_before_retry` | A delivered successful verification response satisfying the optional JSONPath/value predicate occurred between an ambiguous commit and its retry |
| `no_unexpected_calls` | Every outbound call matched the recorded universe |
| `deadline` | The command completed within the bounded duration |

`faults` scopes an assertion to named cases. `include_baseline` also applies a
scoped assertion to the baseline. Assertions with no fault scope apply to all
cases.

Effect identity and idempotency keys are represented in proofs as truncated
SHA-256 digests. Raw request arguments and tool results are not stored in the
ledger. When `deduplicate_by` is configured, repeated matching keys are marked
deduplicated instead of counted as another committed effect.

## Commands

List the stable matrix:

```bash
infernosim agent cases ./incident --seed 42 --budget 100
infernosim agent cases ./incident --json
```

Run one baseline, fault ID, or stable case ID:

```bash
infernosim agent run ./incident --case refund-response-lost \
  --timeout 30s --report-dir ./artifacts -- ./run-agent-tests
```

Run the complete matrix:

```bash
infernosim agent stress ./incident \
  --formats junit,sarif,html \
  --report-dir ./artifacts \
  -- ./run-agent-tests
```

The child command must appear explicitly after `--`. InfernoSIM never executes
a command read from an incident, configuration, span, or transcript.
`HTTP_PROXY`, `HTTPS_PROXY`, `INFERNOSIM_PROXY_URL`,
`INFERNOSIM_ADMIN_URL`, `INFERNOSIM_CASE_ID`, and
`INFERNOSIM_FAULT_ID` are set for that child process. Each case gets a fresh
loopback simulator and state reset.

Machine-readable case JSON, an unweighted category surface JSON, one JUnit
testcase per planned case, SARIF findings, and a standalone HTML report are
written with owner-only permissions. The surface reports only the observed
pass rate for configured incident cases; it is not a general model score.
Application stdout and stderr are bounded before inclusion in the private JSON
result.

## MCP stdio

Record an explicit MCP server command while keeping stdout protocol-clean:

```bash
infernosim agent mcp record ./incident -- ./my-mcp-server
```

Replay the transcript as the server:

```bash
infernosim agent mcp replay ./incident \
  --fault policy-field-missing \
  --proof ./artifacts/mcp-proof.json \
  --report-dir ./artifacts/mcp
```

The transcript is owner-only JSONL. Messages are bounded to 16 MiB and the
transcript to 100,000 records. Replay compares client messages semantically,
ignores the recorded JSON-RPC ID, rewrites the response to the runtime ID, and
fails on an unexpected message. Diagnostics go to stderr because stdout is the
MCP protocol channel. A lost/timeout response is withheld while the replay
server remains available for a client retry; an injected reset terminates the
transport. At EOF, applicable consequence assertions and selected-fault
coverage are evaluated. Optional reports use the same JSON, category surface,
JUnit, SARIF, and HTML formats as HTTP agent runs.

## OpenTelemetry correlation

Import OTLP JSON or framework JSONL:

```bash
infernosim agent otel import ./incident \
  --input ./otel-export.json \
  --hash-content
```

The normalized `agent-spans.jsonl` allowlists trace/span IDs, operation,
provider, tool name, and tool-call ID. Prompt, tool-argument, and tool-result
attributes are never copied. `--hash-content` stores one-way SHA-256 hashes for
correlation without retaining raw values. The importer rejects inputs over
64 MiB and files with no agent-correlatable spans.

Telemetry is supporting evidence, not the source of pass/fail truth. The
simulator's observed protocol and side-effect ledger remain the oracle.

## CI generation

Generate an agent-aware harness:

```bash
infernosim testgen ./incident \
  --profile agent \
  --framework github-actions \
  --out ./.github/workflows/infernosim-agent
```

The generated workflow extracts the pinned InfernoSIM CLI from its container
image and runs `infernosim agent stress ... -- go test ./...`. It uploads the
private JSON, JUnit, SARIF, and HTML directory on success or failure. The
selected fault must be exercised; a test suite that never reaches its agent
path fails instead of producing a false green result.

Go Testcontainers and Compose profiles include the stable `agent-cases.json`
manifest and fault/case startup wiring for teams with a custom test lifecycle.

## Ollama compatibility

Ollama is optional. To verify a local model's native tool-call envelope:

```bash
INFERNOSIM_OLLAMA_MODEL=llama3.1-local:latest scripts/ollama-smoke.sh
```

The script skips when Ollama is absent unless
`INFERNOSIM_REQUIRE_OLLAMA=1` is set. It sends one tool definition to the local
`/api/chat` endpoint and verifies that InfernoSIM recognizes the returned tool
call. Model sampling is deliberately not a release oracle because it can vary;
the checked-in recording is.

## Evidence and defensibility

The defensible category is **local, incident-derived consequence testing for
tool-using agents**. InfernoSIM combines capabilities that are often evaluated
separately: service virtualization, protocol-aware semantic faults, explicit
side-effect accounting, replay-closed outbound traffic, stable case IDs, and
standard CI evidence. The durable asset is the sanitized incident and its
reviewable safety contract, not a proprietary hosted dataset.

Run the public benchmark:

```bash
work_dir=$(mktemp -d)
go build -trimpath -o "$work_dir/agentlab" ./examples/agentlab
go run ./cmd/agentbenchmark --runs 20 --agent-command "$work_dir/agentlab"
```

The checked-in raw result records 100/100 safe executions passing and 80/80
unsafe fault controls being rejected across 20 stable matrix iterations. See
[`benchmarks/results/agent-reliability.json`](../benchmarks/results/agent-reliability.json).
This proves the behavior of the published fixture and runner. It is not a
claim that InfernoSIM is universally better or faster than a named competitor;
such a claim requires the same public corpus and raw competitor runs.

## Security and current boundaries

- The deterministic runner binds both simulator ports to loopback.
- The child process is always an explicit command after `--`; incident data is
  never executable.
- On Linux and macOS, each command runs in its own process group so a case
  timeout terminates descendants as well as the direct child. Windows uses the
  standard Go child-process cancellation behavior.
- Bodies and call counts are bounded, regex uses Go RE2, and mutation JSONPath
  has bounded length and depth.
- Proofs contain hashes, operation names, counts, and assertion outcomes—not
  raw tool arguments or results. Command output is private and bounded but can
  still contain application secrets; treat the report directory accordingly.
- The default plan remains baseline/singles. 4.0.1 optionally enumerates bounded
  fault pairs and explicit admission schedules. This is not exhaustive race
  exploration, formal verification, or proof of model intent.
- All tool entries in complete provider JSON envelopes can be selected by tool
  or call ID. Mutations retain explicit full-envelope JSONPaths; incremental
  streamed tool-call assembly is outside that guarantee.
- MCP stdio support is newline-delimited JSON-RPC. Other framing schemes and
  binary transports are outside the v4 contract.
- OpenTelemetry import supports OTLP JSON and normalized JSON/JSONL, not a live
  OTLP collector.
- Bidirectional gRPC streaming retains the limitations in the main release
  notes.
