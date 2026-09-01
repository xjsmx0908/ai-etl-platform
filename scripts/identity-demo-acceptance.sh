#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
asset_dir="${IDENTITY_DEMO_OUTPUT_DIR:-${repo_root}/secrets/dev/identity-demo}"
compose=(docker compose -p ai-etl-identity-demo -f docker-compose.yml -f docker-compose.eval.yml -f docker-compose.identity-demo.yml)
export IDENTITY_DEMO_RUNTIME_UID="$(id -u)"
export IDENTITY_DEMO_RUNTIME_GID="$(id -g)"

cd "${repo_root}"
bash scripts/identity-demo-clean.sh >/dev/null
bash scripts/identity-demo-setup.sh

cleanup() {
  if [[ "${IDENTITY_DEMO_KEEP_RUNTIME:-0}" != "1" ]]; then
    bash scripts/identity-demo-clean.sh >/dev/null || true
  fi
}
trap cleanup EXIT

echo "[identity-demo] starting isolated runtime"
"${compose[@]}" up -d --build query-api web identity-demo-gateway

echo "[identity-demo] waiting for public HTTPS endpoints"
ready=0
for _ in $(seq 1 90); do
  if curl --fail --silent --show-error --cacert "${asset_dir}/ca.pem" \
    https://keycloak.localhost:8443/realms/ai-etl-demo/.well-known/openid-configuration >/dev/null && \
    curl --fail --silent --show-error --cacert "${asset_dir}/ca.pem" \
    https://ai-etl.localhost:3443/login >/dev/null; then
    ready=1
    break
  fi
  sleep 2
done
if [[ "${ready}" != "1" ]]; then
  "${compose[@]}" ps >&2 || true
  "${compose[@]}" logs --tail=160 keycloak query-api web identity-demo-gateway >&2 || true
  exit 1
fi

python3 scripts/identity-demo-acceptance.py --asset-dir "${asset_dir}"
