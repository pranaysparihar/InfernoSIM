#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT_DIR"
CAUSAL_DIR=$(mktemp -d "${TMPDIR:-/tmp}/infernosim-causal.XXXXXX")
trap 'rm -rf "$CAUSAL_DIR"' EXIT
PYTHON=$(command -v "${PYTHON:-python3}")
APP="$ROOT_DIR/examples/concurrencylab/app.py"
CLI="$CAUSAL_DIR/infernosim"
go build -trimpath -o "$CLI" ./cmd/agent
"$PYTHON" "$APP" --scenario stale --init "$CAUSAL_DIR/incident"
expect_failure() {
  if "$@"; then echo "expected a failing gate" >&2; exit 1
  else local status=$?; test "$status" -eq 1; fi
}
# Discovery writes inspectable evidence even when review is still missing.
expect_failure "$CLI" agent causal discover "$CAUSAL_DIR/incident" --out "$CAUSAL_DIR/unreviewed.json"
expect_failure "$CLI" agent causal plan "$CAUSAL_DIR/unreviewed.json" --out "$CAUSAL_DIR/invalid-plan.json"
"$CLI" agent causal discover "$CAUSAL_DIR/incident" --declarations "$CAUSAL_DIR/incident/declarations.json" --out "$CAUSAL_DIR/graph.json"
"$CLI" agent causal plan "$CAUSAL_DIR/graph.json" --seed 7100 --budget 100 --out "$CAUSAL_DIR/plan-a.json"
"$CLI" agent causal plan "$CAUSAL_DIR/graph.json" --seed 7100 --budget 100 --out "$CAUSAL_DIR/plan-b.json"
cmp "$CAUSAL_DIR/plan-a.json" "$CAUSAL_DIR/plan-b.json"
SETUP=$("$PYTHON" -c 'import json,sys; print(json.dumps(sys.argv[1:]))' "$PYTHON" "$APP" --scenario stale --setup)
CHECK=$("$PYTHON" -c 'import json,sys; print(json.dumps(sys.argv[1:]))' "$PYTHON" "$APP" --scenario stale --check)
"$CLI" agent explore "$CAUSAL_DIR/incident" --causal-graph "$CAUSAL_DIR/graph.json" --schedule-budget 100 --budget 100 --seed 7100 \
  --setup-command-json "$SETUP" --check-command-json "$CHECK" --check-ids database,filesystem \
  --report-dir "$CAUSAL_DIR/cli-safe" -- "$PYTHON" "$APP" --scenario stale --mode safe
ACCEPTANCE="${INFERNOSIM_CAUSAL_OUTPUT:-$CAUSAL_DIR/acceptance}"
go run ./cmd/causalbenchmark -binary "$CLI" -python "$PYTHON" -app "$APP" -out "$ACCEPTANCE" -iterations "${INFERNOSIM_CAUSAL_ITERATIONS:-1}"
REPRO="$ACCEPTANCE/stale-7100-reproduction"
expect_failure "$CLI" agent reproduce "$REPRO" --setup-command-json "$SETUP" --check-command-json "$CHECK" --check-ids database,filesystem -- "$PYTHON" "$APP" --scenario stale --mode unsafe
"$CLI" agent reproduce "$REPRO" --setup-command-json "$SETUP" --check-command-json "$CHECK" --check-ids database,filesystem -- "$PYTHON" "$APP" --scenario stale --mode safe
echo 'CAUSAL_SMOKE: PASS (review, seeded plans, concurrent SQL/file campaigns, minimization, CLI reproduction and fix)'
