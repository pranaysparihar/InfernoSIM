#!/usr/bin/env bash
# Preserve a complete, inspectable safe/unsafe retry reproduction.
set -euo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT_DIR"
OUT=${1:?Usage: bash scripts/retry-repro.sh NEW_OUTPUT_DIRECTORY}
mkdir -m 700 "$OUT"
OUT=$(cd "$OUT" && pwd)
PYTHON=$(command -v "${PYTHON:-python3}")
APP="$ROOT_DIR/examples/concurrencylab/app.py"
CLI="$OUT/infernosim"
go build -trimpath -o "$CLI" ./cmd/agent
"$PYTHON" "$APP" --scenario retry --init "$OUT/incident"
"$CLI" agent causal discover "$OUT/incident" \
  --declarations "$OUT/incident/declarations.json" --out "$OUT/graph.json"
SETUP=$("$PYTHON" -c 'import json,sys; print(json.dumps(sys.argv[1:]))' "$PYTHON" "$APP" --scenario retry --setup)
CHECK=$("$PYTHON" -c 'import json,sys; print(json.dumps(sys.argv[1:]))' "$PYTHON" "$APP" --scenario retry --check)
for MODE in safe unsafe; do
  STATUS=0
  "$CLI" agent explore "$OUT/incident" --causal-graph "$OUT/graph.json" \
    --seed 7100 --schedule-budget 100 --budget 200 --max-faults 2 \
    --setup-command-json "$SETUP" --check-command-json "$CHECK" --check-ids database,filesystem \
    --report-dir "$OUT/$MODE" -- "$PYTHON" "$APP" --scenario retry --mode "$MODE" || STATUS=$?
  if [ "$MODE" = safe ]; then test "$STATUS" -eq 0; else test "$STATUS" -eq 1; fi
done
"$PYTHON" - "$OUT" <<'PY'
import json, pathlib, sys
root = pathlib.Path(sys.argv[1])
safe = json.loads((root / 'safe/exploration.json').read_text())
unsafe = json.loads((root / 'unsafe/exploration.json').read_text())
assert safe['passed'] > 0 and safe['failed'] == safe['invalid'] == 0
assert unsafe['failed'] > 0 and unsafe['invalid'] == 0
assert any(a['id'] == 'state:database' and a['coverage'] == 'violated'
           for r in unsafe['results'] for a in r['assertions'])
assert unsafe['reproductions']
print('RETRY_REPRO: PASS; fresh-ID causal replay, safe control, named unsafe database failure')
PY
printf 'Evidence retained in %s\n' "$OUT"
