# Reproducible category benchmark

This directory defines the benchmark InfernoSIM uses for evidence-backed
product claims. It is not a throughput-only microbenchmark and must not be
edited after results are known.

The category under test is **local incident-derived service virtualization**.
The benchmark measures:

- time from a supplied sanitized incident to the first green CI test;
- manual edits and handwritten matcher lines;
- successful replay of held-out incident observations;
- false-positive and false-negative matcher decisions;
- semantic determinism over 100 identical runs;
- seeded-secret leakage across generated source, logs, and reports;
- startup time, peak RSS, CPU, and replay latency.

The category contract reserves these reference workloads:

1. REST with changing UUIDs, timestamps, and protected amount/status fields;
2. webhook retry and idempotency state;
3. unary and server-streaming gRPC with trailers;
4. OpenAPI response drift;
5. HTTP → gRPC → Kafka causal payment workflow;
6. AsyncAPI schema drift and deterministic message faults.

The checked-in v3.4 baseline currently automates workload 1 over 100 clean
process-level runs and records both matcher configuration and generated harness
hashes. Workloads 2–6 define the next adapter fixtures; they are not presented
as completed comparison results. Kafka and cross-protocol behavior are covered
by the repository's wire-protocol and CLI integration suites meanwhile.

## Rules for fair comparisons

- Pin tool, container, operating-system, and runtime versions.
- Use the same sanitized input and expected assertions.
- Include installation and required configuration in time-to-green.
- Never count generated code as handwritten lines.
- Publish failures, unsupported cases, warm and cold runs, and raw JSON data.
- Do not extrapolate a win on one workload into an overall superiority claim.
- Commercial tools are listed as “not run” unless a licensed, reproducible run
  was actually performed.

The public benchmark runner and competitor adapters must be reviewed separately
from feature implementation. Until raw competitor results exist, InfernoSIM may
claim the capabilities it demonstrates but not that it is categorically faster
or better than a named competitor.
