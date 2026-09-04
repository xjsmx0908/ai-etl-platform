#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"
export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-ai-etl-release-functional}"
export API_PORT="${API_PORT:-8084}"
export QUERY_API_HOST_PORT="${API_PORT}"
export WEB_PORT="${WEB_PORT:-3104}"
export ENVIRONMENT=staging
export JWT_SECRET="${JWT_SECRET:-release-center-functional-jwt-secret-2026}"
export BOOTSTRAP_ADMIN_TENANT="${BOOTSTRAP_ADMIN_TENANT:-release-functional-tenant}"
export BOOTSTRAP_ADMIN_USERNAME="${BOOTSTRAP_ADMIN_USERNAME:-release-admin}"
export BOOTSTRAP_ADMIN_PASSWORD="${BOOTSTRAP_ADMIN_PASSWORD:-release-admin-password-2026}"
export EMBED_DIMENSION="${EMBED_DIMENSION:-1024}"
export STORE_COLLECTION="${STORE_COLLECTION:-release-center-functional-documents}"
export AGENT_PLANNER_TYPE="${AGENT_PLANNER_TYPE:-rule}"
export GOVERNANCE_REPORT_DIR="${GOVERNANCE_REPORT_DIR:-${ROOT_DIR}/artifacts/release-center-functional-acceptance}"
REPORT_PATH="${GOVERNANCE_REPORT_DIR}/release-center-functional-$(date -u +%Y%m%dT%H%M%SZ).json"
export COMPOSE_FILE="docker-compose.yml:docker-compose.eval.yml:docker-compose.smoke.yml:docker-compose.governance.yml"

cleanup() {
  local code=$?
  if [[ "${RELEASE_CENTER_KEEP_SERVICES:-0}" != "1" ]]; then
    docker compose down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  exit "${code}"
}
trap cleanup EXIT

docker compose up -d --build --quiet-pull
for _ in $(seq 1 90); do
  curl -fsS "http://127.0.0.1:${API_PORT}/healthz" >/dev/null && break
  sleep 2
done
curl -fsS "http://127.0.0.1:${API_PORT}/healthz" >/dev/null
python3 scripts/release-center-functional-acceptance.py --api-url "http://127.0.0.1:${API_PORT}" --report "${REPORT_PATH}" --timeout "${MAX_WAIT_SECONDS:-300}"
echo "[release-center] retained report: ${REPORT_PATH}"
