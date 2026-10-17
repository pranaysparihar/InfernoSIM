#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT_DIR"
COMMUNITY_DIR=$(mktemp -d "${TMPDIR:-/tmp}/infernosim-community.XXXXXX")
trap 'rm -rf "$COMMUNITY_DIR"' EXIT
PYTHON=$(command -v "${PYTHON:-python3}")
CLI="$COMMUNITY_DIR/infernosim"
go build -trimpath -o "$CLI" ./cmd/agent
bash scripts/retry-repro.sh "$COMMUNITY_DIR/retry"
go run ./cmd/communitybenchmark -binary "$CLI" -python "$PYTHON" \
  -iterations "${INFERNOSIM_COMMUNITY_ITERATIONS:-1}" -schedule-budget 12 \
  -out "${INFERNOSIM_COMMUNITY_OUTPUT:-$COMMUNITY_DIR/acceptance}"
echo 'COMMUNITY_SMOKE: PASS (retry usability; committed-loss/cancel, pending original, lost handle; destination checks; pinned reduced reproductions)'
