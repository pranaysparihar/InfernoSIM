# v4.2.1 validation record

Release preparation is in progress. Publication and the final validation record
are gated on the two-hour soak, packaging checks and GitHub CI. This document
will be finalized before the v4.2.1 tag is pushed.

Implemented scope: issues #14–#20. Tests cover bounded local Ollama recovery and
offline branch replay, real HTTP/MCP observed schedule conversion, checker
fingerprints and new-case lineage, fresh-ID retry reproduction, and 15 synthetic
destination recovery variants with safe/unsafe controls.

Completed local checks so far: pinned Go 1.26.9 root race suite, vet and module
consistency; root vulnerability scan; official provider SDK stream consumers;
real local Ollama tool envelope plus bounded JSON/NDJSON live recovery; three
seeded legacy causal campaigns; historical reliability/discovery smokes;
100-run category and 20-iteration agent benchmarks; unprivileged Linux amd64
container and real Testcontainers integration; Kafka/AsyncAPI with Redpanda.

The live model check establishes compatibility, not recovery quality. Causal
campaigns explore bounded schedules; they do not establish exhaustive coverage.
The new destination provider is synthetic, with actual SQLite state/history
checks and explicit deduplication, expiry, cancellation and receipt contracts.
