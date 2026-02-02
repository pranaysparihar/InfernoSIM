PROMPT 2

“Verify all features working”

This is not manual clicking.
This is three hard proofs.

Required verifications

1. Determinism proof
infernosim replay incident.log
infernosim replay incident.log
Must produce:
	•	identical failure
	•	identical event index
	•	identical causal chain

⸻

2. Dependency isolation proof
During replay:
	•	no real HTTP / DB calls
	•	all responses come from captured log

If a single real call escapes → invalid system.

⸻

3. Time control proof
	•	time.Sleep does not sleep wall-clock
	•	retries fire at same logical times

If wall time leaks → system is broken.

⸻

Output of Prompt 2

A single captured incident that:
	•	replays identically ≥ 10 times

That’s it.
PROMPT 3

“Tweaks and improvements”

This is where simulation appears.

Only allowed tweaks (v0)
	•	latency injection
	•	timeout modification
	•	retry count modification
	•	time compression

Example:
infernosim replay \
  --time-scale 0.1 \
  --inject dep=redis latency=+200ms

  What must happen
	•	failure appears / disappears
	•	divergence is detected at correct event index

No graphs. No dashboards.

⸻

PROMPT 4

“Polishing and unit testing”

This is not full test coverage.

Mandatory tests only

1. Golden replay test
	•	capture → replay → replay
	•	assert identical outcome

⸻

2. Divergence test
	•	mutate one event
	•	assert first divergence detected

⸻

3. Overhead sanity test
	•	capture enabled vs disabled
	•	p99 overhead < 5% (rough)

Anything else is optional.

⸻

PROMPT 5

“Deploy”

Deployment = not SaaS.

v0 deployment = one of:
	•	local binary
	•	Docker container
	•	injected into one real service

Artifacts:
	•	infernosim binary
	•	README explaining:
	•	how to capture
	•	how to replay
	•	one real example

That’s deployment.