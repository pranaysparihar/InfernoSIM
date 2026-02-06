#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT_DIR"

RUNTIME="${1:-node}"
case "$RUNTIME" in
  node)
    PROFILE="example-node"
    APP_SERVICE="nodeapp-deterministic"
    APP_PORT="8083"
    INCIDENT_DIR="./examples/nodeapp-deterministic"
    ;;
  go)
    PROFILE="example-go"
    APP_SERVICE="goapp-deterministic"
    APP_PORT="8084"
    INCIDENT_DIR="./examples/goapp-deterministic"
    ;;
  *)
    echo "Usage: $0 [node|go]" >&2
    exit 2
    ;;
esac

INCIDENT_DIR=$(cd "$INCIDENT_DIR" && pwd)
LOG_FILE="${INCIDENT_DIR}/outbound.log"
COMPOSE_FILES=(-f docker-compose.yml -f docker-compose.examples.yml)

cleanup() {
  if [ "${KEEP_ON_FAIL:-0}" != "1" ]; then
    docker compose "${COMPOSE_FILES[@]}" --profile "$PROFILE" down >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

echo "[1/5] Build infernosim image"
docker build -t infernosim:local .

echo "[2/5] Reset incident log"
mkdir -p "$INCIDENT_DIR"
rm -f "$LOG_FILE"

echo "[3/5] Start compose stack"
INCIDENT_DIR="$INCIDENT_DIR" docker compose "${COMPOSE_FILES[@]}" --profile "$PROFILE" up -d

wait_healthy() {
  local service="$1"
  local cid
  cid=$(INCIDENT_DIR="$INCIDENT_DIR" docker compose "${COMPOSE_FILES[@]}" --profile "$PROFILE" ps -q "$service")
  if [ -z "$cid" ]; then
    echo "Service $service has no container id" >&2
    return 1
  fi
  for _ in $(seq 1 30); do
    status=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$cid")
    if [ "$status" = "healthy" ]; then
      return 0
    fi
    sleep 1
  done
  echo "Service $service did not become healthy" >&2
  return 1
}

echo "[4/5] Wait for service health"
wait_healthy infernosim-capture
sleep 2

echo "[5/5] Run smoke request and verify outbound capture"
if ! INCIDENT_DIR="$INCIDENT_DIR" docker compose "${COMPOSE_FILES[@]}" --profile "$PROFILE" exec -T "$APP_SERVICE" sh -lc "for i in 1 2 3 4 5; do wget -qO- http://127.0.0.1:${APP_PORT}/api/demo >/dev/null 2>&1 && exit 0; sleep 1; done; exit 1"; then
  echo "Smoke failed: app request did not succeed" >&2
  INCIDENT_DIR="$INCIDENT_DIR" docker compose "${COMPOSE_FILES[@]}" --profile "$PROFILE" ps -a || true
  INCIDENT_DIR="$INCIDENT_DIR" docker compose "${COMPOSE_FILES[@]}" --profile "$PROFILE" logs --no-color --tail=120 infernosim-capture "$APP_SERVICE" || true
  exit 1
fi
sleep 1

if [ ! -s "$LOG_FILE" ]; then
  echo "Smoke failed: $LOG_FILE is missing or empty" >&2
  exit 1
fi

if ! grep -q '"type":"OutboundCall"' "$LOG_FILE"; then
  echo "Smoke failed: no OutboundCall events captured" >&2
  exit 1
fi

echo "SMOKE: PASS"
