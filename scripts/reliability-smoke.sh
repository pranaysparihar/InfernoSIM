#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT_DIR"
RELIABILITY_DIR=$(mktemp -d "${TMPDIR:-/tmp}/infernosim-reliability.XXXXXX")
trap 'rm -rf "$RELIABILITY_DIR"' EXIT

go build -trimpath -o "$RELIABILITY_DIR/infernosim" ./cmd/agent
go build -trimpath -o "$RELIABILITY_DIR/reliabilitylab" ./examples/reliabilitylab
CLI="$RELIABILITY_DIR/infernosim"
LAB="$RELIABILITY_DIR/reliabilitylab"
INCIDENT="$RELIABILITY_DIR/incident"
"$LAB" --init "$INCIDENT"

expect_failure() {
  if "$@"; then
    echo "unsafe control unexpectedly passed" >&2
    exit 1
  else
    status=$?
    if [ "$status" -ne 1 ]; then
      echo "expected assertion exit 1, got $status" >&2
      exit 1
    fi
  fi
}

"$CLI" agent run "$INCIDENT" --report-dir "$RELIABILITY_DIR/safe" -- "$LAB" --mode safe
"$CLI" agent run "$INCIDENT" --restart-after-call 2 --report-dir "$RELIABILITY_DIR/recovery" -- "$LAB" --mode safe
expect_failure "$CLI" agent run "$INCIDENT" --report-dir "$RELIABILITY_DIR/unsafe" -- "$LAB" --mode unsafe
expect_failure "$CLI" agent run "$INCIDENT" --case false-healthy --report-dir "$RELIABILITY_DIR/false-healthy" -- "$LAB" --mode safe
expect_failure "$CLI" agent run "$INCIDENT" --case stale-monitor --report-dir "$RELIABILITY_DIR/stale" -- "$LAB" --mode safe

"$CLI" agent compare "$INCIDENT" --budget 1 \
  --baseline-command-json "[\"$LAB\",\"--mode\",\"unsafe\"]" \
  --candidate-command-json "[\"$LAB\",\"--mode\",\"safe\"]" \
  --report-dir "$RELIABILITY_DIR/improvement"
expect_failure "$CLI" agent compare "$INCIDENT" --budget 1 \
  --baseline-command-json "[\"$LAB\",\"--mode\",\"safe\"]" \
  --candidate-command-json "[\"$LAB\",\"--mode\",\"unsafe\"]" \
  --report-dir "$RELIABILITY_DIR/regression"
"$CLI" agent reduce "$INCIDENT" --case false-healthy --assertion honest-monitor \
  --out "$RELIABILITY_DIR/reduced.json" -- "$LAB" --mode safe

for report in infernosim-agent-results.json infernosim-report.junit.xml infernosim-report.sarif infernosim-report.html; do
  test -s "$RELIABILITY_DIR/safe/$report"
done
grep -q '"restarted": true' "$RELIABILITY_DIR/recovery/infernosim-agent-results.json"
grep -q '"complete": true' "$RELIABILITY_DIR/reduced.json"
grep -q '<failure' "$RELIABILITY_DIR/regression/infernosim-report.junit.xml"
echo "RELIABILITY_SMOKE: PASS (approval, tenant, compensation, monitor, budgets, recovery, compare, reduce)"
