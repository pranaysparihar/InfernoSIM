#!/usr/bin/env bash
set -euo pipefail

INBOUND_PORT=18080
APP_PORT=18081
INCIDENT_DIR="${INCIDENT_DIR:-/infernosim}"

rm -f replay_result.txt

# Start inbound proxy (for replay target)
infernosim --mode=inbound \
  --listen=:${INBOUND_PORT} \
  --forward=localhost:${APP_PORT} \
  --log=/dev/null &
INBOUND_PID=$!

# Start app, force outbound traffic into stubproxy
HTTP_PROXY=http://localhost:19000 PORT=${APP_PORT} \
  go run examples/goapp/main.go &
APP_PID=$!

sleep 2

# 🔴 CRITICAL FIX: --incident IS A DIRECTORY
infernosim replay \
  --incident "${INCIDENT_DIR}" \
  --runs 10 \
  --time-scale "${TIME_SCALE:-0.1}" \
  ${INJECT_FLAGS:-}

echo "REPLAY: PASS" > replay_result.txt

kill $APP_PID $INBOUND_PID
wait || true