#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT_DIR"

OLLAMA_ENDPOINT="${INFERNOSIM_OLLAMA_ENDPOINT:-http://127.0.0.1:11434}"
OLLAMA_MODEL="${INFERNOSIM_OLLAMA_MODEL:-llama3.1-local:latest}"

if ! curl --fail --silent --max-time 2 "$OLLAMA_ENDPOINT/api/tags" >/dev/null 2>&1; then
  if [ "${INFERNOSIM_REQUIRE_OLLAMA:-0}" = "1" ]; then
    echo "Ollama is required but unavailable at $OLLAMA_ENDPOINT" >&2
    exit 1
  fi
  echo "OLLAMA_SMOKE: SKIP (no server at $OLLAMA_ENDPOINT)"
  exit 0
fi

go run ./cmd/ollamasmoke --endpoint "$OLLAMA_ENDPOINT" --model "$OLLAMA_MODEL"
