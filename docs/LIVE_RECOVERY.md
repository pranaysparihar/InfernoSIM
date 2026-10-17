# Opt-in local Ollama recovery

Recorded replay stays offline by default. For a bounded recovery experiment,
`agent run` can send an unmatched model request to an explicitly configured
local Ollama endpoint:

```bash
infernosim agent run ./incident --case tool-fails \
  --live-ollama-endpoint http://127.0.0.1:11434 \
  --live-model-origin http://model.test \
  --live-ollama-model YOUR_LOCAL_MODEL \
  --live-max-requests 1 --live-max-tokens 256 --live-timeout 30s \
  --live-capture-dir ./recovery-branch \
  --live-privacy-policy ./privacy.yaml \
  --report-dir ./recovery-results -- ./test-agent
```

The application sends model traffic to the intercepted origin and uses the
runner's HTTP proxy. If omitted, `--live-model-origin` equals the live endpoint.
Only HTTP `POST /api/chat` at that exact origin is eligible. The configured
model must match the request. The provider endpoint must be a numeric loopback
HTTP origin; redirects, credentials in URLs and cloud-tagged models are refused.
Configure Ollama itself for local inference and pin the model's weights in your
test environment. The runner records the model name and generation-option
hashes; it does not independently identify the model weights.

The recorded prefix runs normally, including configured faults. The first
unmatched model request starts recovery, preserving the application's actual
fault context. Model request bodies are compared exactly for this decision.
Once recovery starts, later model calls stay live even if they resemble the
old recording. Tool calls still require a recorded response or an explicitly
configured test scenario. There is no live tool passthrough. As with ordinary
replay, the application must honor the proxy; this is not a network sandbox for
an arbitrary process.

## Budgets and outcomes

The recovery window allows 1–16 model requests, 1–8,192 measured output tokens,
and at most ten minutes. The default is one request, 256 output tokens and
30 seconds. Remaining output allowance is sent as Ollama `num_predict`; final
`eval_count` is required and checked. Input size is capped at 1 MiB, not a
provider-independent input-token estimate. Provider responses are capped at
1 MiB and buffered until completion before delivery. Both non-streaming JSON
and complete Ollama NDJSON chat responses are supported. This does not preserve
live token latency or support arbitrary provider formats.

Provider errors, redirects, malformed/incomplete responses, cancellation,
concurrent live requests and exhausted budgets fail explicitly. Output-token
limits rely on the local provider's usage accounting; the runner validates the
reported count and never claims an independent tokenizer or monetary budget.

Reports label the run `hybrid-live-recovery` and non-deterministic. Seeds control
the recorded experiment, not newly sampled model output. Live runs cannot be
used directly for deterministic comparison/minimization. Existing causal or
admission schedules, stdio sessions and runner-managed restarts cannot be mixed
with this first live recovery mode: a new branch needs its own participants and
dependencies.

## Capture and replay the branch

The new private directory retains the actual prefix and continuation responses,
request/release observation metadata, original scope/case/fault provenance and
budget outcomes. Faulted responses are already present in the new recording;
its generated replay config does not inject those faults a second time.

Supply `--live-privacy-policy` with `capture_bodies: true`, or explicitly use
`--live-capture-sensitive-data` for synthetic data. Credentials are never
forwarded from application headers to the live provider. Capture retains only
content type, stable operation identity and trace-parent request headers.
Privacy transformations apply before storage. Streaming bodies are transformed
one JSON frame at a time. A transformation that removes required correlation
can prevent replay and is reported; raw data is not silently substituted.

Review the new branch and convert it before offline reuse:

```bash
infernosim agent causal convert ./recovery-branch \
  --declarations ./branch-declarations.json --out ./branch-observed.json
infernosim agent explore ./recovery-branch \
  --observed-schedule ./branch-observed.json \
  --report-dir ./offline-results -- ./test-agent
```

`--live-capture-review` may supply a review statement at capture time. Missing
tool-result identity or other unresolved dependencies still require declarations;
a review statement alone cannot resolve them. The initial `observed.json` is
explicitly partial when evidence/review is insufficient. Incomplete transport
responses (including connection loss) are not exported as complete offline
responses. Inspect capture status before reuse.

The release acceptance test injects a tool error, obtains a new tool call with
changed arguments from a deterministic local Ollama fixture, validates the
captured graph, stops the provider and replays the branch offline. Provider
fixtures establish protocol/budget behavior, not the quality of a live model's
recovery choices. Real local Ollama checks remain optional compatibility tests.

Protocol references: [Ollama chat](https://docs.ollama.com/api/chat) and
[usage accounting](https://docs.ollama.com/api/usage).
