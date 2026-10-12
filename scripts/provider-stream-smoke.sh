#!/usr/bin/env bash
set -euo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT_DIR"
SDK_WORK=$(mktemp -d "${TMPDIR:-/tmp}/infernosim-provider-sdks.XXXXXX")
trap 'rm -rf "$SDK_WORK"' EXIT
SDK_PYTHON="${INFERNOSIM_PROVIDER_PYTHON:-$SDK_WORK/venv/bin/python}"
if [ -z "${INFERNOSIM_PROVIDER_PYTHON:-}" ]; then
  python3 -m venv "$SDK_WORK/venv"
  "$SDK_PYTHON" -m pip install --disable-pip-version-check -r integrations/provider-streams/requirements.txt
fi
go run ./integrations/provider-streams -out "$SDK_WORK/fixtures"
"$SDK_PYTHON" integrations/provider-streams/check.py "$SDK_WORK/fixtures" --out "${INFERNOSIM_PROVIDER_SDK_REPORT:-$SDK_WORK/report.json}"
echo 'PROVIDER_SDK_SMOKE: PASS (OpenAI Responses/Chat, Anthropic, Ollama; baseline and per-call mutation; three HTTP write fragment sizes)'
