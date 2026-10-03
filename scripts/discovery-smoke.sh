#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT_DIR"
DISCOVERY_DIR=$(mktemp -d "${TMPDIR:-/tmp}/infernosim-discovery.XXXXXX")
trap 'rm -rf "$DISCOVERY_DIR"' EXIT
go build -trimpath -o "$DISCOVERY_DIR/infernosim" ./cmd/agent
go build -trimpath -o "$DISCOVERY_DIR/discoverylab" ./examples/discoverylab
CLI="$DISCOVERY_DIR/infernosim"
LAB="$DISCOVERY_DIR/discoverylab"
"$LAB" --init "$DISCOVERY_DIR/incident"
SETUP="[\"$LAB\",\"--setup\"]"
CHECK="[\"$LAB\",\"--check\"]"
expect_failure() {
 if "$@"; then echo "expected an assertion failure" >&2; exit 1
 else local status=$?; test "$status" -eq 1; fi
}
"$CLI" agent explore "$DISCOVERY_DIR/incident" --budget 100 --max-faults 3 \
 --setup-command-json "$SETUP" --check-command-json "$CHECK" --check-ids outbox-once \
 --report-dir "$DISCOVERY_DIR/safe" -- "$LAB" --mode safe
expect_failure "$CLI" agent explore "$DISCOVERY_DIR/incident" --budget 100 --max-faults 3 \
 --setup-command-json "$SETUP" --check-command-json "$CHECK" --check-ids outbox-once \
 --report-dir "$DISCOVERY_DIR/unsafe" -- "$LAB" --mode unsafe
# Validate actual state findings and select the deliberately noisy triple case.
REPRO=$(python3 - "$DISCOVERY_DIR/unsafe" <<'PY'
import json, pathlib, sys
root=pathlib.Path(sys.argv[1]); r=json.loads((root/'exploration.json').read_text())
assert r['failed'] > 0 and r['invalid'] == 0 and not r['plan']['truncated']
assert len(r['results']) == 8
for result in r['results']:
    assert next(a for a in result['assertions'] if a['id']=='remote-refund-once')['passed']
    if not result['passed']:
        assert not next(a for a in result['assertions'] if a['id']=='state:outbox-once')['passed']
for path in r['reproductions']:
    manifest=json.loads((pathlib.Path(path)/'reproduction.json').read_text())
    if len(manifest['trial']['case'].get('fault_ids',[]))==3:
        print(path); break
else: raise AssertionError('missing triple reproduction')
PY
)
"$CLI" agent minimize "$REPRO" --out "$DISCOVERY_DIR/minimal" --budget 100 \
 --setup-command-json "$SETUP" --check-command-json "$CHECK" --check-ids outbox-once \
 -- "$LAB" --mode unsafe
python3 - "$DISCOVERY_DIR/minimal" <<'PY'
import json,sys
p=sys.argv[1]; r=json.load(open(p+'.minimization.json'))
assert r['complete']
assert r['trial']['case']['fault_ids']==['response-lost']
PY
mv "$DISCOVERY_DIR/minimal" "$DISCOVERY_DIR/relocated"
expect_failure "$CLI" agent reproduce "$DISCOVERY_DIR/relocated" \
 --setup-command-json "$SETUP" --check-command-json "$CHECK" --check-ids outbox-once \
 -- "$LAB" --mode unsafe
"$CLI" agent reproduce "$DISCOVERY_DIR/relocated" \
 --setup-command-json "$SETUP" --check-command-json "$CHECK" --check-ids outbox-once \
 -- "$LAB" --mode safe
printf 'tampered' >> "$DISCOVERY_DIR/relocated/incident/outbound.log"
expect_failure "$CLI" agent reproduce "$DISCOVERY_DIR/relocated" \
 --setup-command-json "$SETUP" --check-command-json "$CHECK" --check-ids outbox-once \
 -- "$LAB" --mode safe
for report in infernosim-report.junit.xml infernosim-report.sarif infernosim-report.html; do
 test -s "$DISCOVERY_DIR/unsafe/$report"
done
echo 'DISCOVERY_SMOKE: PASS (exploration, independent outbox checks, triple reduction, relocation, fix, tamper rejection)'
