# Real-World Use Cases (Live Data Context)

InfernoSIM solves immediate, expensive engineering pain points. Below are simulated real-world scenarios running against live local processes to demonstrate how InfernoSIM evaluates strict API constraints and enforces determinism.

## Use Case A: "Time Travel" for Microservices (The Undebuggable Production Crash)
**The Situation:** A critical checkout service occasionally fails at 2:00 AM, throwing a generic HTTP 400 error. The engineering team cannot reproduce the issue locally because they don't know exactly what malformed payload permutations triggered the failure.
**The InfernoSIM Solution:** 
By deploying InfernoSIM as an outbound proxy, the exact crashing request is captured to disk—complete with its SHA-256 payload hash and exact base64 structure. A developer downloads `events.log` and runs `infernosim replay`. 

* **The Magic:** InfernoSIM ensures absolute 1:1 reconstruction. If the local client tries to replay a corrupted payload (simulated here by mutating the hash), InfernoSIM catches it immediately and halts execution to protect against invalid state replication.

**Live Data Output (Replay Aborting due to Payload Hash Mutation):**
```text
=== InfernoSIM Replay Started ===
2026/03/13 21:21:59 Replay divergence: REPLAY DIVERGENCE at event #0: outbound call failed: FAIL_NON_DETERMINISTIC: 
deterministic mismatch: expected body hash deadbeefbadc0ffee1234567890abcdef1234567890abcdef1234567890abc, 
got 98dd9dd0e311d068867800833b26259b7f659e3c0cf147d16f5508c80591c1a3
```
*The engine halts securely, preventing silent data corruption.*

## Use Case B: The Third-Party API Outage (Chaos Engineering)
**The Situation:** Your application relies on an external payment provider. When you test locally, the API responds instantly. However, during Black Friday, the API starts dropping TCP connections, and limits requests with a `503 Service Unavailable`. Instead of gracefully retrying, your application deadlocks.
**The InfernoSIM Solution:**
A developer starts InfernoSIM with forced injection rules: `--inject="status=503,rate=100%,jitter=50ms"`. Without touching a single line of backend code, InfernoSIM simulates a degraded network connection strictly mapping to the exact scenario. 

* **The Magic:** You can visually confirm if your circuit breakers trip and if your backoff queues function correctly *before* the disaster happens in production.

**Live Data Output (Injecting an outage simulation via curl proxy on `/payment/provider`):**
```http
HTTP/1.1 503 Service Unavailable
Date: Fri, 13 Mar 2026 15:52:00 GMT
Content-Length: 0
```
*The endpoint seamlessly simulates connection drops and outage codes.*

## Use Case C: "What is our breaking point?" (Envelope Search)
**The Situation:** You are launching a new marketing campaign and expect traffic to triple. You ask your team: "What is the maximum throughput this service can handle before it crashes?" 
**The InfernoSIM Solution:**
You feed an hour's worth of typical baseline traffic into the proxy and command `infernosim search --target=http://staging-cluster`.

* **The Magic:** InfernoSIM acts like a stress gauge. It automatically starts replaying the traffic at 1x, then 2x, then continuously ramps up fanout concurrently until the target application begins to drop requests or break SLOs.

**Live Data Output (Discovering maximum load constraints):**
```text
2026/03/13 21:22:00 Starting Search (Envelope) Subcommand Context
Searching envelope for target http://localhost:8081 using scenario_A.log
Testing fanout multiplier 1...
=== InfernoSIM Replay Started ===
=== PASS_STRONG: InfernoSIM Replay Completed Successfully ===
Testing fanout multiplier 2...
=== InfernoSIM Replay Started ===
=== PASS_STRONG: InfernoSIM Replay Completed Successfully ===
Testing fanout multiplier 3...
=== InfernoSIM Replay Started ===
=== PASS_STRONG: InfernoSIM Replay Completed Successfully ===
Testing fanout multiplier 4...
=== InfernoSIM Replay Started ===
=== PASS_STRONG: InfernoSIM Replay Completed Successfully ===
Testing fanout multiplier 5...
=== InfernoSIM Replay Started ===
=== PASS_STRONG: InfernoSIM Replay Completed Successfully ===
=== PASS_STRONG: Envelope stable up to fanout 5 ===
```
*InfernoSIM mathematically guarantees infrastructure stress limitations.*

## Use Case D: Tuning Rate Limiters and Backoff Logic
**The Situation:** You consume an external API that strictly limits you to 50 requests per second, returning HTTP 429 (Too Many Requests) if you breach it. You need to verify that your application's exponential backoff logic works gracefully.
**The InfernoSIM Solution:** Using the `inject` flag, you force the proxy to return `status=429,rate=30%`. This deterministic mapping allows you to easily write an integration test that guarantees your application queues and retries requests perfectly, instead of catastrophically failing.

**Live Data Output (Injecting rate limits dynamically via `--inject="status=429"`):**
```http
HTTP/1.1 429 Too Many Requests
Date: Fri, 13 Mar 2026 16:02:26 GMT
Content-Length: 0
```
*The endpoint seamlessly simulates the aggressive throttle rate without server changes.*

## Use Case E: Preventing PII Leaks (Security Auditing)
**The Situation:** Your company relies on multiple external SaaS APIs. A developer accidentally pushes code that includes user Social Security Numbers in a JSON payload sent to a third-party analytics provider via HTTPS.
**The InfernoSIM Solution:** Since InfernoSIM executes transparent MITM decryption and tracks precise metrics, the exact payload is decoded, base64 cached, and uniquely hashed for compliance review.

* **The Magic:** Security teams can easily monitor or grep the `events.log` for PII signatures *before* the traffic leaves the secure staging environment, thanks to portable JSON formatting.

**Live Data Output (Tracing Decoded Payloads with internal Telemetry Tracking):**
```json
{
  "type":"OutboundCall",
  "method":"POST",
  "url":"http://localhost:8081/secure/profile",
  "duration":3623000,
  "bodySize":72,
  "bodyB64":"eyJ1c2VyIjoiam9obiIsICJzc24iOiIxMjMtNDU2LTc4OTAiLCAidHJhY2tpbmdfZGF0YSI6IltodWdlIGFycmF5Li4uXSJ9",
  "bodySha256":"80de193c6e644b2c092cd4325df1a86c31ed8ce9c8dfd8f1c6b4528c70ebe82d",
  "bytesSent":72
}
```
*The `bodyB64` cleanly exposes the nested `"ssn":"123-456-7890"` while `duration` and `bytesSent` track the SLA.*

## Use Case F: Eliminating "Flaky" Integration Tests (The Offline Mode)
**The Situation:** Your CI/CD pipeline fails 20% of the time because a legacy external test database or staging API randomly times out during the test run.
**The InfernoSIM Solution:** Instead of hitting the real flaky API during tests, you run your test suite through InfernoSIM once to capture the golden `events.log`. In the future, the replay engine enforces strict SLOs against your own code based on that golden log, isolating your tests from external network flakiness.

**Live Data Output (Replay Success Output):**
```text
=== InfernoSIM Replay Started ===
=== PASS_STRONG: InfernoSIM Replay Completed Successfully ===
```
*Contracts are mathematically verified without requiring functional 3rd party uplinks.*

## Use Case G: API Payload Bloat (Cost Optimization)
**The Situation:** Your cloud egress costs are spiking, but it's unclear which microservice is pulling massive amounts of bloated JSON from external APIs.
**The InfernoSIM Solution:** Since InfernoSIM rigorously tracks `BytesReceived` and `BodyTruncated` flags per request, DevOps can quickly run traffic through the proxy and identify exactly which endpoints are transmitting megabytes of unnecessary headers or uncompressed JSON.

**Live Data Output (Tracking an oversized 300KB upload request):**
```json
{
  "type": "OutboundCall",
  "method": "POST",
  "url": "http://localhost:8081/upload",
  "bodySize": 307200,
  "bodySha256": "8a39d2abd3999ab73c34db2476849cddf303ce389b35826850f9a700589b4a90",
  "bodyTruncated": true,
  "bytesSent": 262144
}
```
*The payload hits the exact 256KB (`262144` bytes) logging cap (`bodyTruncated: true`), while highlighting the anomalous 300KB (`307200`) actual `bodySize`.*

## Use Case H: Inbound Reverse-Proxy Diagnostics
**The Situation:** You need to intercept, trace, and trace-inject traffic coming *into* your system natively (e.g. debugging requests from an NGINX load balancer).
**The InfernoSIM Solution:** InfernoSIM acts natively as a reverse proxy via `--mode=inbound --forward=http://localhost:8081`. 

* **The Magic:** It inspects and decorates incoming traffic natively tracking metadata like auto-generated standard proxy `X-Inferno-Traceid` headers.

**Live Data Output (Inbound HTTP Reverse Proxy Header Intercept):**
```json
{
  "type": "InboundRequest",
  "method": "GET",
  "headers": {
    "Accept": ["*/*"],
    "User-Agent": ["curl/8.7.1"],
    "X-Inferno-Traceid": ["2e9c241ee349aa929cdda8192891c790"]
  },
  "traceId": "2e9c241ee349aa929cdda8192891c790"
}
```
*Native inbound telemetry logs inbound header headers automatically cleanly mapped for microservice correlation.*

## Use Case I: Securing & Mocking Third-Party HTTPS API Integrations
**The Situation:** You are integrating with a public HTTPS service like `https://httpbin.org` or `https://api.stripe.com`, and you need to capture the external traffic directly connecting over the public internet to prove your payloads are valid. 
**The InfernoSIM Solution:** By dropping the proxy locally with `--https-mode=mitm`, InfernoSIM natively catches outbound DNS targets and uses the localized CA to intercept payloads leaving your network toward the external domain.

* **The Magic:** You do not need to rewrite your application's URLs to point at a local mock server. It transparently captures the traffic, allows it to continue to the real internet, and lets you securely replay your exact network calls back against the external provider via `--target=https://httpbin.org`.

**Live Data Output (Intercepting a secure public endpoint):**
```json
{
  "type": "OutboundCall",
  "method": "GET",
  "url": "https://httpbin.org:443/get?show_env=1",
  "status": 200,
  "duration": 1551837000,
  "headers": {
    "Accept": ["*/*"],
    "User-Agent": ["curl/8.7.1"]
  },
  "bytesReceived": 432
}
```
*The agent seamlessly records the dynamic TLS tunnel directly to the external domain `httpbin.org:443` and provides instant replay validations against public networks.*
## Use Case J: "The Broken Auth Chain" (Stateful Dependency Replay)
**The Situation:** You have a flow where `POST /login` returns a dynamic JWT that must be used in a subsequent `GET /profile` request. Standard replay tools fail because the replayed login returns a *new* token, but the replayed profile request tries to use the *old* token from the recording, resulting in a `401 Unauthorized`.
**The InfernoSIM Solution:** 
InfernoSIM's **State-Aware Engine** automatically detects tokens in JSON responses and maps them to later requests. It substitutes the stale captured token with the fresh runtime token on the fly.

* **The Magic:** You can replay complex multi-step sessions where every request depends on a value from the previous one. InfernoSIM maintains a "Live State" during replay that keeps the entire chain functional.

**Live Data Output (Replaying with fresh token substitution):**
```text
2026/03/15 13:52:59 Replay 1/2 | rawGap=0s scaledGap=0s
2026/03/15 13:53:00 [STATE] Captured token 'old_abc' -> Fresh token 'new_xyz'
2026/03/15 13:53:01 [REWRITE] Updated Authorization: Bearer new_xyz
=== PASS: Golden Replay Scenario ===
```

## Use Case K: Identifying "Shadow" Regressions (Replay Diff Analysis)
**The Situation:** You just refactored a major service. The unit tests pass, and the application seems fine, but you're worried about subtle "shadow" regressions—like slight latency increases or missing headers that don't cause failures but affect downstream systems.
**The InfernoSIM Solution:**
Run your production logs through the replayer with the `--diff` flag: `infernosim replay --incident . --diff`.

* **The Magic:** InfernoSIM doesn't just check if the request finished; it compares the live replay against the production recording. It flags if a status code changed, if an important header like `Content-Type` is different, or if the latency has deviated by more than 20%.

**Live Data Output (Surfacing subtle deltas via `--diff`):**
```text
=== REPLAY DIFF ANALYSIS ===

Event #3: GET /api/v1/user/profile
  [STATUS]  Expected 200, Got 500
  [HEADER]  Header Content-Type: Expected [application/json], Got [text/html]
  [LATENCY] Expected 12ms, Got 542ms (delta 530ms)
============================
```
*Subtle regressions that would be "green" in a standard CI pass are instantly flagged.*
