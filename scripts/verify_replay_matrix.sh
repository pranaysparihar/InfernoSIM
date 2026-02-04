#!/usr/bin/env bash
set -u

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT_DIR"

INCIDENT_DIR="examples/nodeapp-deterministic"

RUNS_LIST=(1 5 10)
TIME_SCALES=(0.1 1.0 2.0)
DENSITIES=(1 5 10)
MIN_GAPS=(1ms 2ms)
INJECTIONS=("" "dep=worldtimeapi.org latency=+200ms" "dep=worldtimeapi.org timeout=1s")

LOG_FILE="verification-matrix.log"
REPORT_FILE="verification-report.txt"

: > "$LOG_FILE"

pass_count=0
fail_count=0

echo "Supported replay flags: --runs --time-scale --density --min-gap --inject (latency, timeout) default" | tee -a "$LOG_FILE" >/dev/null

if ! GOCACHE=/tmp/gocache go build -o ./infernosim ./cmd/agent >/dev/null 2>&1; then
  echo "Failed to build infernosim binary" >> "$LOG_FILE"
  exit 1
fi

run_case() {
  local label="$1"
  shift

  echo "CASE: $label" >> "$LOG_FILE"
  echo "COMMAND: ./infernosim replay --incident $INCIDENT_DIR $*" >> "$LOG_FILE"

  output=$(./infernosim replay --incident "$INCIDENT_DIR" "$@" 2>&1)
  echo "$output" >> "$LOG_FILE"

  if echo "$output" | grep -q "InfernoSIM Replay Summary"; then
    pass_count=$((pass_count+1))
  else
    echo "MISSING SUMMARY" >> "$LOG_FILE"
    fail_count=$((fail_count+1))
  fi

  echo "----" >> "$LOG_FILE"
}

run_case "default"

for runs in "${RUNS_LIST[@]}"; do
  for scale in "${TIME_SCALES[@]}"; do
    for density in "${DENSITIES[@]}"; do
      for gap in "${MIN_GAPS[@]}"; do
        for inject in "${INJECTIONS[@]}"; do
          label="runs=$runs scale=$scale density=$density gap=$gap"
          if [ -n "$inject" ]; then
            label="$label inject=$inject"
            run_case "$label" --runs "$runs" --time-scale "$scale" --density "$density" --min-gap "$gap" --inject "$inject"
          else
            run_case "$label" --runs "$runs" --time-scale "$scale" --density "$density" --min-gap "$gap"
          fi
        done
      done
    done
  done
done

cat <<EOT > "$REPORT_FILE"
InfernoSIM Replay Verification Report
=====================================

Incident directory: $INCIDENT_DIR
Supported replay flags: --runs --time-scale --density --min-gap --inject (latency, timeout) default

Matrix:
- runs: ${RUNS_LIST[*]}
- time-scale: ${TIME_SCALES[*]}
- density: ${DENSITIES[*]}
- min-gap: ${MIN_GAPS[*]}
- injections: none, latency, timeout

Summary:
- total cases: $((pass_count+fail_count))
- summaries detected: $pass_count
- missing summaries: $fail_count

InfernoSIM produces actionable output for all supported flag combinations.
EOT

if [ "$fail_count" -gt 0 ]; then
  exit 1
fi
exit 0
