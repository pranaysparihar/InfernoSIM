#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SMOKE_DIR="$(mktemp -d)"
CONTAINER="infernosim-redpanda-smoke-$$"
BROKER="127.0.0.1:19092"
REDPANDA_IMAGE="${INFERNOSIM_REDPANDA_IMAGE:-docker.io/redpandadata/redpanda:v25.2.9@sha256:bd6c35647153a4dbc19507c8037dcca0802484c23e6e5fbb97591a62539e313a}"

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  rm -rf "$SMOKE_DIR"
}
trap cleanup EXIT

docker run --detach --name "$CONTAINER" -p 19092:19092 \
  "$REDPANDA_IMAGE" \
  redpanda start --overprovisioned --smp 1 --memory 512M \
  --reserve-memory 0M --node-id 0 --check=false \
  --kafka-addr internal://0.0.0.0:9092,external://0.0.0.0:19092 \
  --advertise-kafka-addr internal://127.0.0.1:9092,external://"$BROKER" >/dev/null

for attempt in $(seq 1 60); do
  if docker exec "$CONTAINER" rpk cluster info -X brokers=127.0.0.1:9092 >/dev/null 2>&1; then
    break
  fi
  if [[ "$attempt" == "60" ]]; then
    docker logs "$CONTAINER"
    exit 1
  fi
  sleep 1
done

docker exec "$CONTAINER" rpk topic create payment.authorized ci.payment.authorized \
  -X brokers=127.0.0.1:9092 >/dev/null

go build -trimpath -o "$SMOKE_DIR/infernosim" ./cmd/agent
mkdir -p "$SMOKE_DIR/incident"

"$SMOKE_DIR/infernosim" kafka capture \
  --brokers "$BROKER" --topics payment.authorized \
  --out "$SMOKE_DIR/incident/messages.log" --max-messages 1 --from-beginning \
  --capture-sensitive-data &
CAPTURE_PID=$!
sleep 2
printf '%s\n' '{"payment_id":"pay_42","status":"authorized"}' | \
  docker exec -i "$CONTAINER" rpk topic produce payment.authorized \
  -X brokers=127.0.0.1:9092 >/dev/null
wait "$CAPTURE_PID"

"$SMOKE_DIR/infernosim" kafka validate "$SMOKE_DIR/incident" \
  --asyncapi "$ROOT_DIR/examples/asyncapi.yaml" --report-dir "$SMOKE_DIR/reports"

"$SMOKE_DIR/infernosim" kafka replay "$SMOKE_DIR/incident" \
  --brokers "$BROKER" --asyncapi "$ROOT_DIR/examples/asyncapi.yaml" \
  --topic-prefix ci. --report-dir "$SMOKE_DIR/reports"

docker exec "$CONTAINER" rpk topic consume ci.payment.authorized -n 1 \
  --format '%v' -X brokers=127.0.0.1:9092 | grep -q '"payment_id":"pay_42"'

test -s "$SMOKE_DIR/reports/infernosim-kafka-proof.json"
test -s "$SMOKE_DIR/reports/infernosim-report.junit.xml"
test -s "$SMOKE_DIR/reports/infernosim-report.sarif"
test -s "$SMOKE_DIR/reports/infernosim-report.html"

echo "Kafka/AsyncAPI/Redpanda smoke test passed"
