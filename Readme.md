Another terminal:
 HTTP_PROXY=http://localhost:9000 PORT=8081 go run examples/goapp/main.go
Anohter terminal:
 go build -o infernosim ./cmd/agent
./infernosim --mode=proxy --listen=:9000 --log=outputs.log
Another terminal
go build -o infernosim ./cmd/agent
./infernosim --mode=inbound --listen=:8080 --forward=localhost:8081 --log=inputs.log


InfernoSIM

InfernoSIM is a deterministic traffic capture, replay, and simulation tool for debugging distributed systems.

It lets you:
	•	Capture real inbound and outbound HTTP traffic
	•	Replay requests deterministically
	•	Simulate failures (latency, time, retries)
	•	Detect behavioral divergence at the exact event index

No dashboards.
No tracing UI.
Just truth via replay.

⸻

Core Concepts

1. Capture

InfernoSIM runs as lightweight HTTP proxies:
	•	Inbound proxy → captures requests entering your service
	•	Outbound proxy → captures calls your service makes to dependencies

All events are written as append-only JSON logs.

2. Replay

Captured inbound traffic is replayed:
	•	With original timing (or time-compressed)
	•	Against a live instance of your service
	•	While outbound calls are stubbed and controlled

3. Simulation

During replay you may inject controlled changes:
	•	Latency
	•	Time scaling
	•	Retry amplification
	•	Timeout behavior

Any behavioral change is detected deterministically.
Build
go build -o infernosim ./cmd/agent
Phase 1: Capture Traffic

Start outbound proxy (dependencies)
./infernosim --mode=proxy \
  --listen=:19000 \
  --log=outbound.log

Start inbound proxy (service entrypoint):
./infernosim --mode=inbound \
  --listen=:18080 \
  --forward=localhost:18081 \
  --log=inbound.log

Run your app through InfernoSIM:
HTTP_PROXY=http://localhost:19000 \
PORT=18081 \
go run examples/goapp/main.go
Generate traffic (curl, browser, tests, etc).

Phase 2: Verify Determinism
./verify.sh

What this checks:
	•	Capture works
	•	Replay produces identical fingerprints
	•	No external dependencies leak in
	•	Time control is stable

Output:
VERIFY: PASS (deterministic, isolated, time-controlled)


Phase 3: Replay + Simulation

Basic replay
./infernosim replay \
  --incident . \
  --runs 10

Creates:
  replay_result.txt

Example content:
REPLAY: PASS
FINGERPRINT: 01f2b489d9b6e507850942f0340b95288c1451c334241e0b208fbe62eb08dab7
RUNS: 10
TIME_SCALE: 1.000

Time compression:
./infernosim replay \
  --incident . \
  --time-scale 0.1
Replays traffic 10× faster, preserving ordering.

Latency injection:
./infernosim replay \
  --incident . \
  --inject "dep=worldtimeapi.org latency=+200ms"

Expected behavior:
	•	Either replay still passes (system resilient)
	•	Or divergence is detected with exact event index

Forced divergence example:
./infernosim replay \
  --incident . \
  --inject "dep=worldtimeapi.org latency=+10s" \
  --time-scale 0.01

Example output:
DIVERGENCE at outbound event index=1
reason=unexpected_outbound_call