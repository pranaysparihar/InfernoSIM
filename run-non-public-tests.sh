#!/usr/bin/env bash

OUT_FILE="non-public-test-results.txt"
BIN="./infernosim"
TIMEOUT_SECS=90

echo "==================================================" > "$OUT_FILE"
echo "InfernoSIM Internal Replay Test Suite" >> "$OUT_FILE"
echo "Timestamp: $(date)" >> "$OUT_FILE"
echo "Timeout per test: ${TIMEOUT_SECS}s" >> "$OUT_FILE"
echo "==================================================" >> "$OUT_FILE"
echo "" >> "$OUT_FILE"

run_test () {
  local desc="$1"
  local cmd="$2"
  local status

  echo "--------------------------------------------------" >> "$OUT_FILE"
  echo "TEST: $desc" >> "$OUT_FILE"
  echo "COMMAND:" >> "$OUT_FILE"
  echo "  $cmd" >> "$OUT_FILE"
  echo "--------------------------------------------------" >> "$OUT_FILE"
  echo "" >> "$OUT_FILE"

  if command -v gtimeout >/dev/null 2>&1; then
    gtimeout "${TIMEOUT_SECS}s" bash -c "$cmd" >> "$OUT_FILE" 2>&1
    status=$?
  elif command -v timeout >/dev/null 2>&1; then
    timeout "${TIMEOUT_SECS}s" bash -c "$cmd" >> "$OUT_FILE" 2>&1
    status=$?
  else
    echo "WARNING: no timeout command available, running without timeout" >> "$OUT_FILE"
    bash -c "$cmd" >> "$OUT_FILE" 2>&1
    status=$?
  fi

  echo "" >> "$OUT_FILE"

  if [ "$status" -eq 124 ]; then
    echo "RESULT: TIMEOUT after ${TIMEOUT_SECS}s" >> "$OUT_FILE"
  else
    echo "EXIT CODE: $status" >> "$OUT_FILE"
  fi

  echo "" >> "$OUT_FILE"
}

# ---------- TIME SCALE TESTS ----------
run_test "Replay at 10x faster time (0.1x)" \
"$BIN replay --time-scale 0.1 --incident ."

run_test "Replay at 2x slower time (2.0x)" \
"$BIN replay --time-scale 2.0 --incident ."

# ---------- SINGLE FAULT INJECTION ----------
run_test "Redis latency +200ms" \
"$BIN replay --inject \"dep=redis latency=+200ms\""

run_test "Redis error rate 10%" \
"$BIN replay --inject \"dep=redis error=10%\""

run_test "Redis timeout 1s" \
"$BIN replay --inject \"dep=redis timeout=1s\""

run_test "Redis latency +200ms and error 5%" \
"$BIN replay --inject \"dep=redis latency=+200ms error=5%\""

run_test "Postgres latency +300ms" \
"$BIN replay --inject \"dep=postgres latency=+300ms\""

run_test "worldtimeapi.org latency +500ms" \
"$BIN replay --inject \"dep=worldtimeapi.org latency=+500ms\""

# ---------- COMBINED TIME + FAULT ----------
run_test "Time-scale 0.1 + Redis latency +200ms" \
"$BIN replay --time-scale 0.1 --inject \"dep=redis latency=+200ms\""

run_test "Time-scale 2.0 + Redis error 10%" \
"$BIN replay --time-scale 2.0 --inject \"dep=redis error=10%\""

# ---------- MULTI DEPENDENCY ----------
run_test "Redis latency +200ms + Auth-service error 5%" \
"$BIN replay --inject \"dep=redis latency=+200ms\" --inject \"dep=auth-service error=5%\""

# ---------- DETERMINISM ----------
run_test "Determinism check (10 runs)" \
"$BIN replay --incident . --runs 10"

# ---------- BASELINE WITH FAULT ----------
run_test "Baseline replay with Redis latency +200ms" \
"$BIN replay --incident . --inject \"dep=redis latency=+200ms\""

echo "==================================================" >> "$OUT_FILE"
echo "END OF INTERNAL TEST SUITE" >> "$OUT_FILE"
echo "==================================================" >> "$OUT_FILE"

echo "Done. Results written to $OUT_FILE"