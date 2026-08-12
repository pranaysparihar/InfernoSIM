#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <vMAJOR.MINOR.PATCH> <output-formula>" >&2
  exit 2
fi

release_tag="$1"
output_formula="$2"
if [[ ! "$release_tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$ ]]; then
  echo "invalid release tag: $release_tag" >&2
  exit 2
fi

release_version="${release_tag#v}"
source_url="https://github.com/pranaysparihar/InfernoSIM/archive/refs/tags/${release_tag}.tar.gz"
repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
formula_template="$repository_root/packaging/homebrew/infernosim.rb.tmpl"

source_sha256="${INFERNOSIM_SOURCE_SHA256:-}"
if [[ -z "$source_sha256" ]]; then
  temporary_directory="$(mktemp -d)"
  trap 'rm -rf "$temporary_directory"' EXIT
  curl --fail --location --silent --show-error \
    --output "$temporary_directory/source.tar.gz" "$source_url"
  if command -v sha256sum >/dev/null 2>&1; then
    source_sha256="$(sha256sum "$temporary_directory/source.tar.gz" | awk '{print $1}')"
  else
    source_sha256="$(shasum -a 256 "$temporary_directory/source.tar.gz" | awk '{print $1}')"
  fi
fi

if [[ ! "$source_sha256" =~ ^[0-9a-f]{64}$ ]]; then
  echo "invalid source SHA-256: $source_sha256" >&2
  exit 2
fi

mkdir -p "$(dirname "$output_formula")"
sed \
  -e "s/@VERSION@/$release_version/g" \
  -e "s/@SOURCE_SHA256@/$source_sha256/g" \
  "$formula_template" >"$output_formula"

echo "Generated $output_formula for $release_tag"
