#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT_DIR"

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/infernosim-agent-smoke.XXXXXX")
cleanup() {
  rm -rf "$WORK_DIR"
}
trap cleanup EXIT

go build -trimpath -o "$WORK_DIR/infernosim" ./cmd/agent
go build -trimpath -o "$WORK_DIR/agentlab" ./examples/agentlab

SAFE_OUTPUT="$WORK_DIR/safe.out"
UNSAFE_OUTPUT="$WORK_DIR/unsafe.out"

"$WORK_DIR/infernosim" agent stress examples/agent-reliability \
  --report-dir "$WORK_DIR/safe-report" \
  -- "$WORK_DIR/agentlab" --mode=safe | tee "$SAFE_OUTPUT"

if "$WORK_DIR/infernosim" agent stress examples/agent-reliability \
  --report-dir "$WORK_DIR/unsafe-report" \
  -- "$WORK_DIR/agentlab" --mode=unsafe >"$UNSAFE_OUTPUT" 2>&1; then
  echo "agent smoke failed: unsafe control loop unexpectedly passed" >&2
  exit 1
fi

safe_passes=$(grep -c '^PASS ' "$SAFE_OUTPUT" || true)
unsafe_passes=$(grep -c '^PASS ' "$UNSAFE_OUTPUT" || true)
unsafe_failures=$(grep -c '^FAIL ' "$UNSAFE_OUTPUT" || true)
if [ "$safe_passes" -ne 5 ] || [ "$unsafe_passes" -ne 1 ] || [ "$unsafe_failures" -ne 4 ]; then
  echo "agent smoke failed: safe=$safe_passes unsafe-pass=$unsafe_passes unsafe-fail=$unsafe_failures" >&2
  cat "$UNSAFE_OUTPUT" >&2
  exit 1
fi

for report in \
  "$WORK_DIR/safe-report/infernosim-agent-results.json" \
  "$WORK_DIR/safe-report/infernosim-agent-surface.json" \
  "$WORK_DIR/safe-report/infernosim-report.junit.xml" \
  "$WORK_DIR/safe-report/infernosim-report.sarif" \
  "$WORK_DIR/safe-report/infernosim-report.html"; do
  test -s "$report"
done

echo "AGENT_SMOKE: PASS (safe 5/5; unsafe controls rejected 4/4)"
