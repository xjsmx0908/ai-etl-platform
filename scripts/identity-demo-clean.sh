#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
configured_output_dir="${IDENTITY_DEMO_OUTPUT_DIR:-${repo_root}/secrets/dev/identity-demo}"
output_dir="$(realpath -m -- "${configured_output_dir}")"

if [[ "${output_dir}" != "${repo_root}/secrets/dev/identity-demo" &&
      ! "${output_dir}" =~ ^/tmp/[^/]+/[^/]+(/.*)?$ ]]; then
  echo "refusing unsafe identity demo output directory: ${output_dir}" >&2
  exit 1
fi

docker compose \
  -p ai-etl-identity-demo \
  -f docker-compose.yml \
  -f docker-compose.eval.yml \
  -f docker-compose.identity-demo.yml \
  down -v --remove-orphans

if [[ -d "${output_dir}" ]]; then
  rm -rf -- "${output_dir}"
fi

echo "Identity demo containers, volumes, and generated assets are removed"
