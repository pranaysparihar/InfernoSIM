# InfernoSIM Scenario Corpus

Real captured incidents used for replay regression testing.

Each scenario lives in `pkg/replaydriver/testdata/incidents/<name>/` and contains:
- `incident.json` — capture metadata (env, host, counts)
- `inbound.log` — JSONL of captured InboundRequest events
- `outbound.log` — JSONL of outbound dependency events (if present)
- `replay.yaml` — scenario-specific replay config (if present)

---

## Scenarios

| Scenario | Events | What it covers |
|----------|--------|----------------|
| `auth-token-chain` | 2 | Bearer token produced by login, consumed by subsequent request — tests substitution correctness |
| `cookie-session` | 2 | Session cookie set on POST, sent on GET — tests cookie jar propagation |
| `jwt-expiry` | 1 | Expired JWT in Authorization header — tests `verify` expiry detection |
| `resource-id-chain` | 2 | Resource ID returned in POST body, used in GET URL — tests ID mapping |
| `partial-failure` | 3 | Mix of 200 and 500 responses — tests diff engine correctness |
| `retry-chain` | 3 | Two 503s followed by a 200 — tests retry detection and safe-mode enforcement |
| `timeout-chain` | 2 | 504 timeout followed by successful recovery — tests timing divergence detection |

---

## Adding a New Scenario

1. Run `infernosim record` against your app:
   ```bash
   infernosim record --listen :8080 --forward :8081 --out ./incident --env test
   # exercise your app: curl http://localhost:8080/login ...
   # Ctrl-C to stop
   ```

2. Move the incident bundle into the corpus:
   ```bash
   mv ./incident pkg/replaydriver/testdata/incidents/<scenario-name>
   ```

3. Add a test in `scenario_test.go`:
   ```go
   func TestScenario_Inspect_MyScenario(t *testing.T) {
       bundle, _ := OpenBundle(incidentDir("my-scenario"))
       result, err := InspectIncident(bundle.InboundLog)
       if err != nil { t.Fatal(err) }
       // assert what you expect
   }
   ```

4. Run the corpus:
   ```bash
   go test ./pkg/replaydriver/... -run TestScenario
   ./scripts/run-scenarios.sh --target http://localhost:8081
   ```

---

## CI Replay Matrix

Run every scenario with different configs:

```bash
# Normal replay
./scripts/run-scenarios.sh

# Safe mode (skips POST/PUT/PATCH/DELETE)
./scripts/run-scenarios.sh --safe-mode
```

---

## Runtime Compatibility

The same scenarios can be run against multiple language runtimes to verify protocol compatibility:

| Runtime | Example app |
|---------|-------------|
| Go | `examples/goapp/` |
| Go (deterministic) | `examples/goapp-deterministic/` |
| Node.js | `examples/nodeapp-deterministic/` |
| Python | `examples/python-fastapi/` |
| Java | `examples/java-springboot/` |
| gRPC | `examples/grpcapp/` |

```bash
# Start Go app, record and replay
go run examples/goapp/main.go &
infernosim record --listen :8080 --forward :8081 --out ./incident
# exercise endpoints...
infernosim replay ./incident --target-base http://localhost:8081
```
