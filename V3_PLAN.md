# InfernoSIM v3 Roadmap (Corrected)

## Phase 1 — Incident Format & Capture Stabilization

**Goal:** define stable incident recording format.

**Deliverables:**
- finalize incident.json schema
- request + response fidelity
- deterministic ordering
- metadata block (environment, host, capture info)
- incident bundle format

**CLI:**
```
infernosim record
```
**Output:**
```
incident.json
```

---

## Phase 2 — Dependency Understanding

**Goal:** InfernoSIM understands relationships in incidents.

**Deliverables:**
- token dependency extraction
- cookie/session chain detection
- resource ID propagation
- multi-dependency chains
- dependency graph builder

**CLI:**
```
infernosim inspect incident.json
```
**Optional:**
```
infernosim graph incident.json
```

---

## Phase 3 — State Snapshot Hooks (External State)

**Goal:** reconstruct surrounding system state.

**Deliverables:**
- StateAdapter interface
- Redis/session adapter
- DB row adapter
- feature flag adapter
- snapshot completeness report

**Replay becomes:**
```
restore snapshot → replay incident
```

**CLI:**
```
infernosim replay incident.json
```
*(No new command required.)*

---

## Phase 4 — Replay Engine Hardening

**Goal:** make replay reliable and deterministic.

**Deliverables:**
- improved runtime substitution
- multi-dependency substitution
- replay ordering guarantees
- large-incident stability
- environment mapping

**Replay stays simple:**
```
infernosim replay incident.json
```
**Profiles handled by config, not flags.**

---

## Phase 5 — Replay Diff & Root-Cause View

**Goal:** explain why replay diverged.

**Deliverables:**
- first divergence detection
- status diff
- header diff
- JSON body diff
- latency delta analysis
- dependency-aware divergence tracing

**CLI:**
```
infernosim diff incident.json
```
**Output:**
```
First divergence: request 12
Original: 200
Replay:   500

Body diff:
status confirmed -> failed
```

---

## Phase 6 — Incident Verification & Safety

**Goal:** make replay safe for real teams.

**Deliverables:**
- unresolved dependency detection
- expired token detection
- side-effect detection
- replay readiness scoring
- dry-run mode

**CLI:**
```
infernosim verify incident.json
```
**Output:**
```
replay readiness: 86%
unsafe requests: 2
missing dependencies: 1
```

---

## Phase 7 — Incident Exploration

**Goal:** help engineers understand incidents quickly.

**Deliverables:**
- incident summary
- request timeline
- dependency graph view
- divergence timeline

**CLI:**
```
infernosim inspect incident.json
```
**Output:**
```
requests: 42
dependency chains: 7
tokens: 2
sessions: 1
resource IDs: 5
```

---

## Phase 8 — Incident Simulation

**Goal:** explore alternate outcomes.

**Deliverables:**
- latency injection
- connection reset
- timeout simulation
- retry simulation

**CLI:**
```
infernosim replay incident.json
```
**Simulation rules come from config.**

---

## Phase 9 — Developer Experience (CLI Architecture)

**Goal:** simple, predictable CLI.

**Command structure (final)**
```
infernosim record
infernosim inspect
infernosim verify
infernosim replay
infernosim diff
```
**No command explosion.**

---

## Config-driven workflows

**Instead of flags:**
```
infernosim replay incident.json --inject-latency --profile staging --dry-run --state redis
```
**Use config:**
```
infernosim replay incident.json --config replay.yaml
```
**Example config:**
```yaml
target: http://staging-api
chaos:
  latency:
    request: 7
    delay: 500ms
state:
  redis: redis://localhost
safe_mode: true
```

---

## Sensible defaults

**If users run:**
```
infernosim replay incident.json
```
**InfernoSIM should automatically:**
- detect dependencies
- perform substitution
- warn about unsafe requests
- print divergence summary

**No flags required.**

---

## Human-friendly output

**Example:**
```
Incident Replay Summary

Requests: 42
Dependencies: 7
Replay Status: Diverged

First divergence: request 12
Original: 200
Replay:   500
Latency delta: +420ms
```

---

## Phase 10 — Runtime Compatibility Matrix

**Goal:** prove production reliability.

**Test across:**
- Go (Gin/Echo)
- Node (Express/Fastify)
- Python (FastAPI)
- Java (Spring Boot)

**Technical behavior packs:**
- token chains
- cookie session chains
- resource ID chains
- multi-dependency chains
- retry chains
- timeout chains
- chaos divergence chains

---

## Phase 11 — Open Core Platform Boundary

**Open source:**
- incident capture
- dependency graph
- state-aware replay
- diff analysis
- verify command
- basic simulation

**Paid layer:**
- advanced state adapters
- CI replay validation
- incident dashboards
- team collaboration
- replay orchestration

---

## Final CLI Philosophy

InfernoSIM v3 should feel like:
- git
- docker
- kubectl

**Meaning:**
- few commands
- clear workflows
- config driven
- minimal flags

---

## Final v3 Flow

```
record incident
      ↓
inspect dependencies
      ↓
verify safety
      ↓
restore state
      ↓
replay deterministically
      ↓
diff divergence
      ↓
simulate alternate outcomes
```

---

If done correctly, v3 becomes a serious developer tool, not a CLI science project.