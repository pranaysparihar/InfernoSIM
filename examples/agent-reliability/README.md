# Agent reliability fixture

This is a synthetic, sanitized incident for InfernoSIM v4. It contains six
recorded HTTP exchanges: MCP tool discovery, two OpenAI-style tool decisions,
a policy lookup, a refund, and a refund-status verification. It contains no
production data or credentials.

Build and run the defensive reference loop:

```bash
go build -trimpath -o /tmp/infernosim ./cmd/agent
go build -trimpath -o /tmp/infernosim-agentlab ./examples/agentlab

/tmp/infernosim agent stress examples/agent-reliability \
  --report-dir /tmp/infernosim-agent-report \
  -- /tmp/infernosim-agentlab --mode=safe
```

The expected result is five passes: the baseline plus four fault cases. Change
the mode to `unsafe`; the baseline should pass and all four fault cases should
fail. `scripts/agent-smoke.sh` asserts both outcomes and verifies JSON, JUnit,
SARIF, and HTML generation.

The fixture demonstrates behavior, not general model quality. Extend it with
additional sanitized trajectories and explicit consequences for your own agent.
See [`docs/AGENT_RELIABILITY.md`](../../docs/AGENT_RELIABILITY.md).
