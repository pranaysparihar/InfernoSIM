# Causal concurrency testing in v4.2

InfernoSIM can discover evidenced dependencies in a recorded agent incident,
generate seeded topological schedules, and enforce separate request-admission
and response-release boundaries across HTTP and MCP stdio. A retained regression
includes the graph and observed execution proof. The application still runs as
an ordinary process; independent checks can inspect its database and files.

This is bounded exploration of recorded protocol interactions. It does not
schedule OS threads, prove that hidden application dependencies are absent, or
generate new model responses when the application diverges from the recording.

## Discover and review the graph

```bash
infernosim agent causal discover ./incident --out ./graph-unreviewed.json
```

Discovery writes the graph and prints issues. An unreviewed graph returns exit
code 1 so CI cannot accidentally treat missing evidence as proven independence.
Review the application's dependencies and supply declarations, then discover
again into a new file:

```json
{
  "review": "The two lookups run concurrently; the new-value checkpoint is issued after its transaction commits.",
  "edges": [{
    "from": "read_new:release",
    "to": "checkpoint_new:admit",
    "reason": "Application continuation awaits the new lookup before committing and issuing its checkpoint."
  }],
  "identities": {},
  "span_operations": {},
  "resolutions": {}
}
```

```bash
infernosim agent causal discover ./incident \
  --declarations ./declarations.json --out ./graph.json
infernosim agent causal plan ./graph.json \
  --seed 7100 --budget 100 --out ./plan.json
```

Operation IDs come from captured HTTP event IDs or MCP transcript sequence IDs
such as `mcp_5`. Every operation has `:admit` and `:release` nodes. Edges retain
the inference rule, source reference, and evidence digest. Discovery uses these
relationships:

| Evidence | Dependency |
| --- | --- |
| Captured exchange | Its admission precedes its release |
| Unique model tool proposal matched by name and canonical arguments | Model release precedes tool admission |
| Consumed provider tool-result identity | Tool release precedes consuming model admission |
| OpenAI `previous_response_id`, including supported Responses streams | Prior response release precedes continuation admission |
| OTel parent chain, mapped by W3C `traceparent`, tool-call identity or declaration | Parent admission precedes child admission |
| Legacy MCP initialize/initialized exchange | Initialization precedes subsequent protocol operations |
| MCP cancellation target | Target admission precedes cancellation; a captured cancellation without a response precedes the target's terminal boundary |
| MCP progress token | Request admission precedes correlated progress; progress release precedes response release |
| Reviewed application declaration | The explicitly named boundary relationship |

Generic OTel ancestors, timestamps, parent IDs and links survive normalization.
Parenthood does **not** imply that the parent finished before the child began.
Timestamps do not become ordering edges. Links need explicit review because
they do not identify a start/finish relationship. Missing parents, conflicting
or ambiguous correlations, unsupported streams and cycles are diagnosed.
`span_operations` maps `trace_id/span_id` to an operation. `resolutions` maps an
issue ID to the explanation of the reviewed evidence; it does not add edges.
Add any required edges separately. Cycles and malformed graphs cannot be waived.

Identical requests need distinct stable application identities. Set
`X-InfernoSIM-Operation-ID` on HTTP requests, or
`params._meta["infernosim/operation"]` on MCP requests. A declarations file can
associate those values with recording operations. The application must send the
corresponding identity during replay. Arrival order is never used to distinguish
otherwise identical concurrent calls. JSON-RPC runtime IDs can change; recorded
IDs remain the fault-selector identities and responses are remapped to runtime
IDs without converting large integers through floating point.

## Explore and verify application state

Use the same explicit setup/check argv contract introduced in v4.1:

```bash
infernosim agent explore ./incident \
  --causal-graph ./graph.json --seed 7100 \
  --schedule-budget 100 --budget 200 --max-faults 2 \
  --boundary-timeout 5s \
  --setup-command-json '["./test-app","--setup"]' \
  --check-command-json '["./test-app","--check"]' \
  --check-ids database,filesystem --report-dir ./results \
  -- ./test-app
```

The runner owns one controller, fault engine, effect ledger and execution proof
for HTTP and stdio. HTTP proxy environment variables point at the local replay
server. For stdio, the application starts the argv in
`INFERNOSIM_MCP_COMMAND_JSON` as its MCP server; the connector uses an authenticated
loopback bridge to the same runner. Library users can supply `Options.MCPConnector`.
The stdio receive loop continues reading while other calls wait for their turns.
Cancellation IDs and progress notifications remain correlated.

The controller records a boundary only after its transport action succeeds.
Response release means bytes were written/flushed, or the configured response
loss occurred. It does not mean the application has finished processing those
bytes. Use an observable application checkpoint and a declared dependency when
you need to order an internal transaction against another response. An assertion
requiring approval or verification cannot use a concurrent response that has
not yet been released.

Unexpected/duplicate calls, missing participants, impossible orders, failed
transport writes and boundary timeouts invalidate the run. Invalid runs cannot
be retained by the minimizer as successful reproductions of a product bug.

Graphs are bounded to 256 operations and 8,192 edges. The schedule budget is
1–1,000; boundary timeout is greater than zero and at most 60 seconds. Existing
agent call/body limits still apply. Up to 12 boundary nodes, the planner traverses
valid orders until exhausted or budgeted. Larger graphs use bounded seeded
sampling, suppress duplicates and report attempts and truncation; they do not
claim exhaustive coverage unless the order is provably unique. The case budget
also bounds the cross-product of schedules and faults. Plans report both limits.
The same graph, configuration and seed produce the same plan and IDs.

## Target one streamed tool call

Provider adapters now assemble complete captured streams before replay and allow
argument-relative mutations of one selected call:

```yaml
agent:
  enabled: true
  adapters:
    providers:
      - name: model
        type: openai
        host_regex: '^api\.openai\.com$'
  faults:
    - id: corrupt-second-amount
      select:
        kind: llm_response
        provider: model
        tool: lookup
        call_id: call_b
      mutations:
        - target: tool_arguments
          operation: set
          path: $.amount
          value: 999
```

`target: response` remains the default for existing full-envelope JSONPath
mutations. `tool_arguments` also works with complete supported JSON envelopes.
A selector that matches multiple calls is rejected. A tool name alone is usable
only when it identifies one call in that response.

| Provider format | Supported assembly and targeting | Boundary |
| --- | --- | --- |
| OpenAI Responses SSE | Output item IDs, call IDs, argument deltas/done, item completion and final snapshot; interleaved function calls | Function tools; documented text/reasoning/refusal events pass through; unsupported event families fail |
| OpenAI Chat Completions SSE | Choice/tool indices, stable name/ID, argument fragments, finish reason, usage and `[DONE]` | Fragmented tool names, legacy `function_call` and incomplete/error termination are not accepted |
| Anthropic SSE | Message/block lifecycle, `tool_use`, `input_json_delta`, complete or empty input, block/message completion | Client tool calls; server-tool and new block families require explicit support |
| Ollama NDJSON | Complete tool argument objects in streamed message chunks, tool index and terminal `done` | Missing IDs use `ollama:<index>`; arbitrary argument-string fragment formats are not accepted |

Limits are 16 MiB, 10,000 frames and 256 calls per stream. SSE framing supports
LF and CRLF separators; other separators are unsupported. Malformed JSON,
truncated arguments, conflicting identities, missing/duplicate completion and
unsupported event shapes fail explicitly. A selected fault may intentionally
produce malformed argument strings where the provider represents arguments as
strings; a later mutation that needs to parse that malformed stream fails.

Unrelated protocol frames remain byte-for-byte intact. Changed frames retain
their SSE/NDJSON framing; a replacement argument is placed in its first argument
fragment and subsequent fragments are emptied, with completion snapshots updated.
Captured chunk count, flushes and delays are preserved; changed payload lengths
may change chunk contents. Exact TCP packet segmentation is not promised.
Cancellation interrupts delayed stream delivery. This is recorded-stream replay,
not an online parser that rewrites an unbounded live upstream stream.

## Minimize and retain the failure

```bash
infernosim agent minimize ./results/reproduction-directory \
  --out ./minimal --budget 100 \
  --setup-command-json '["./test-app","--setup"]' \
  --check-command-json '["./test-app","--check"]' \
  --check-ids database,filesystem -- ./test-app --mode unsafe

infernosim agent reproduce ./minimal \
  --setup-command-json '["./test-app","--setup"]' \
  --check-command-json '["./test-app","--check"]' \
  --check-ids database,filesystem -- ./test-app --mode safe
```

The v2 reproduction manifest contains the graph, edge evidence, schedule,
observed order, operation/runtime-ID digest bindings, faults, named assertion,
check IDs, setup requirement, timeouts and restart boundary. Manifest and file
checksums detect alteration; they are not an authenticity signature. Replay
validates recording metadata as well as request/response bodies. Moving the
artifact to another directory and changing runtime request IDs is supported.
Application/setup/check commands remain explicit caller input.

Causal minimization keeps the graph and all participants. It removes fault noise,
reduces occurrences and swaps adjacent independent boundaries toward canonical
order only when causal validity survives. Every retained candidate must fail the
same named assertion on two valid executions with the exact observed schedule.
Completion means a fixed point under those moves, not a globally shortest trace.

## Reproduce the acceptance checks

```bash
bash scripts/causal-smoke.sh
go build -o /tmp/infernosim-candidate ./cmd/agent
go run ./cmd/causalbenchmark -binary /tmp/infernosim-candidate \
  -iterations 3 -out /tmp/causal-acceptance
go run ./cmd/causalbenchmark -binary /tmp/infernosim-candidate \
  -reproductions /tmp/causal-acceptance -out /tmp/causal-soak -soak 2h
```

The Python standard-library application uses a real SQLite database, a separate
JSONL file and independent check processes. Five scenarios exercise stale writes,
lost committed responses/retries, cancellation, compensation and process restart.
Every schedule runs against fixed and deliberately broken implementations; one
failure per scenario is minimized, repeated and exported. Some unconstrained
internal application races can produce different broken-client outcomes under
the same network schedule. The retained checkpoint-constrained regressions must
reproduce consistently, and fixed clients must always pass.

The soak relocates those five regressions, runs both clients repeatedly, checks
the exact named failure and observed schedule, and samples resources after GC.
After three warm-up cycles, permitted growth is 32 MiB Go heap, 96 MiB RSS,
eight goroutines and eight descriptors; remaining descendants and identifiable
orphaned fixture processes must be zero. The release gate requires two hours.
The instrumented soak supports Linux/macOS; it is not a Windows containment test.

The pinned official MCP Go SDK also exercises all six admission/release orders
for concurrent calls across three seeds, with targeted faults and runtime ID
remapping. Its 2026-07-28 `server/discover` path and legacy initialization fixtures
are tested. Official OpenAI, Anthropic and Ollama Python SDKs independently
consume baseline and mutated recorded streams over fragmented loopback HTTP
writes in 24 checks. This does not claim complete MCP 2026 conformance or testing
against live provider services. See [release evidence](VALIDATION_4_2.md).

## Remaining support boundaries

Causal replay requires complete recorded HTTP responses and newline-delimited
MCP JSON-RPC. MCP server-initiated requests, multi-session restart/resumption,
OAuth and other framing are outside this contract. HTTP process restart is
tested; the stdio bridge accepts one client session. Generic SSE/MCP HTTP session
streams are not provider-tool streams. Causal schedules cannot be combined with
legacy admission-only schedules. Existing ordinary replay/scenario features
remain available outside causal agent replay.

Interactive bidirectional gRPC, virtual application clocks, application-memory
snapshots, Windows Job Object containment, Avro/Schema Registry and additional
brokers remain separate work. Azure packaging belongs to the paid repository.

Protocol references: [OpenAI function calls](https://developers.openai.com/api/docs/guides/function-calling),
[Chat streaming](https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events),
[Anthropic tool streaming](https://platform.claude.com/docs/en/agents-and-tools/tool-use/fine-grained-tool-streaming),
[Ollama tool calls](https://docs.ollama.com/capabilities/tool-calling),
[MCP Go SDK](https://go.sdk.modelcontextprotocol.io/protocol/),
[MCP discovery specification](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2026-07-28/server/discover.mdx).
