#!/usr/bin/env bash
set -euo pipefail

echo "=== InfernoSIM Verification ==="

if ! command -v docker >/dev/null 2>&1; then
  echo "Docker is required but not installed"
  exit 1
fi

echo "[1/3] Preparing isolated verification environment"
docker compose build

echo "[2/3] Running verification"
docker compose run --rm infernosim bash scripts/linux-verify.sh

echo "[3/3] Verification result"
cat verify_result.txt

echo "=== Done ==="