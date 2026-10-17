# Reproduce the retry fixture

From a checkout of the release tag, with Go and Python 3 installed:

```bash
bash scripts/retry-repro.sh /tmp/infernosim-retry-421
```

The output directory must not exist. The script builds the checkout's binary,
creates a synthetic incident, discovers its reviewed causal graph, and runs
both application controls. It retains the graph, JSON/HTML/JUnit/SARIF results
and failing reproduction artifacts in that directory. No provider credentials
or live service are needed.

The safe control must pass every valid case. The unsafe control must fail
`state:database` after response loss, with zero invalid runs in both campaigns.
Exit 1 from the unsafe exploration is expected; the script checks the JSON
results so a setup error or invalid schedule cannot count as a counterexample.
The independent check opens a real SQLite outbox and a separate JSONL journal.
The remote refund is simulated. This does not establish any real provider's
deduplication or receipt guarantees.

The supported command sequence is:

1. `python3 examples/concurrencylab/app.py --scenario retry --init INCIDENT`
2. `infernosim agent causal discover INCIDENT --declarations INCIDENT/declarations.json --out GRAPH.json`
3. `infernosim agent explore INCIDENT --causal-graph GRAPH.json ... -- python3 examples/concurrencylab/app.py --scenario retry --mode safe`

The script supplies the remaining budget and setup/check argv flags explicitly.
Use it as the complete command reference, including correct shell quoting.
To inspect a retained failure, use `agent reproduce` with the same explicit
setup/check commands and check IDs and the unsafe application. Switch only the
application's `--mode` to `safe` to verify the fix; do not edit the checker.

## Why ordinary replay reported a correlation error

The fixture creates fresh JSON-RPC IDs on every run and checks that responses
echo those IDs. Ordinary HTTP replay retains the recorded response ID. Its
`response correlation changed` exception therefore indicates the wrong entry
point for this fixture. Causal replay binds each supported request to its
recorded operation and remaps the response to that request's runtime ID.
The exception now points here and to the script.

Request association stays strict. Distinct recorded operations with otherwise
identical calls require stable operation identity; arrival order cannot resolve
them. Changed payloads, unknown operations, missing participants and invalid
schedules remain invalid executions. See [the causal guide](CONCURRENCY_4_2.md).

The original community report required a command change after four ordinary
replay attempts. Its two successful campaigns were by the same operator and
host. This guide addresses that reproducibility friction; it does not relabel
the report as independent-operator validation or a newly discovered engine bug.
