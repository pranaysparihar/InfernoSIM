#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT_DIR"

OUTPUT_FILE="$ROOT_DIR/deterministic-replay-output.txt"
INCIDENT_DIR="$ROOT_DIR/examples/nodeapp-deterministic"

if [ ! -f "$INCIDENT_DIR/inbound.log" ] || [ ! -f "$INCIDENT_DIR/outbound.log" ]; then
  echo "Missing inbound/outbound logs in $INCIDENT_DIR" >&2
  exit 1
fi

docker compose build infernosim

docker compose up -d nodeapp-deterministic

set +e
OUTPUT=$(docker compose run --rm infernosim replay --incident /infernosim/examples/nodeapp-deterministic --runs 3 2>&1)
STATUS=$?
set -e

echo "$OUTPUT" > "$OUTPUT_FILE"

if ! echo "$OUTPUT" | grep -q "Outcome: PASS_STRONG"; then
  echo "Replay did not PASS_STRONG" >&2
  exit 1
fi
if ! echo "$OUTPUT" | grep -E "Outbound events observed: [1-9]" >/dev/null; then
  echo "No outbound events observed" >&2
  exit 1
fi
if ! echo "$OUTPUT" | grep -q "Dependencies exercised: true"; then
  echo "Dependencies not exercised" >&2
  exit 1
fi

exit $STATUS
