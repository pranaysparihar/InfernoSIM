#!/usr/bin/env bash
set -euo pipefail
test -s events.log || { echo "events.log missing or empty. Run verify.sh first."; exit 1; }
echo "=== InfernoSIM Replay ==="

echo "[1/2] Building environment"
docker compose build

echo "[2/2] Running replay"
docker compose run --rm \
  -e TIME_SCALE="${TIME_SCALE:-0.1}" \
  -e INJECT_FLAGS="${INJECT_FLAGS:-}" \
  -e INCIDENT_DIR="/infernosim" \
  infernosim bash scripts/linux-replay.sh

cat replay_result.txt
echo "=== Done ==="