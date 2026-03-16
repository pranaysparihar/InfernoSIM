#!/usr/bin/env bash
# run-scenarios.sh — Deterministic replay validation for InfernoSIM
#
# Runs every scenario multiple times to detect nondeterministic divergence.
#
# Default coverage:
#   20 scenarios × 20 runs each
#
# Usage:
#   ./scripts/run-scenarios.sh
#   ./scripts/run-scenarios.sh --runs 50
#   ./scripts/run-scenarios.sh --safe-mode
#
# Environment:
#   INFERNOSIM_TARGET   Override replay target
#   INFERNOSIM_BIN      Path to infernosim binary

set -euo pipefail

TARGET="${INFERNOSIM_TARGET:-http://localhost:8081}"
BIN="${INFERNOSIM_BIN:-go run ./cmd/agent}"
CORPUS_DIR="$(dirname "$0")/../pkg/replaydriver/testdata/incidents"

RUNS=20
SAFE_MODE=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --runs)
      RUNS="$2"
      shift 2
      ;;
    --safe-mode)
      SAFE_MODE=true
      shift
      ;;
    *)
      shift
      ;;
  esac
done

pass=0
fail=0
skip=0

echo ""
echo "InfernoSIM Determinism Runner"
echo "============================="
echo "Target:   $TARGET"
echo "Runs:     $RUNS"
echo "Corpus:   $CORPUS_DIR"
echo ""

for dir in "$CORPUS_DIR"/*/; do
  scenario=$(basename "$dir")

  if [ ! -f "$dir/inbound.log" ]; then
    echo "SKIP  $scenario (no inbound.log)"
    skip=$((skip + 1))
    continue
  fi

  echo "▶ $scenario"

  scenario_pass=0
  scenario_fail=0

  extra_flags=""
  if [ "$SAFE_MODE" = true ]; then
    extra_flags="--safe-mode"
  fi

  for ((i=1;i<=RUNS;i++)); do
    if $BIN replay "$dir" \
        --target-base "$TARGET" \
        --runs 1 \
        --max-wall-time 30s \
        $extra_flags >/dev/null 2>&1; then
      scenario_pass=$((scenario_pass + 1))
    else
      scenario_fail=$((scenario_fail + 1))
    fi
  done

  echo "   PASS $scenario_pass / $RUNS"

  if [ "$scenario_fail" -gt 0 ]; then
    echo "   FAIL $scenario_fail / $RUNS"
    fail=$((fail + 1))
  else
    pass=$((pass + 1))
  fi
done

echo ""
echo "============================="
echo "Scenarios passed : $pass"
echo "Scenarios failed : $fail"
echo "Scenarios skipped: $skip"
echo "============================="

if [ "$fail" -gt 0 ]; then
  echo "Replay determinism FAILED"
  exit 1
fi

echo "Replay determinism PASSED"
exit 0