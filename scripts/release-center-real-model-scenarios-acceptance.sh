#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"
export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-ai-etl-release-real-model}"
export COMPOSE_FILE="docker-compose.yml:docker-compose.eval.yml:docker-compose.smoke.yml:docker-compose.governance.yml"
export API_PORT="${API_PORT:-8087}"
export QUERY_API_HOST_PORT="${API_PORT}"
export WEB_PORT="${WEB_PORT:-3107}"
export ENVIRONMENT=staging
export JWT_SECRET="${JWT_SECRET:-release-real-model-jwt-secret-2026}"
export BOOTSTRAP_ADMIN_TENANT="${BOOTSTRAP_ADMIN_TENANT:-release-real-model-tenant}"
export BOOTSTRAP_ADMIN_USERNAME="${BOOTSTRAP_ADMIN_USERNAME:-release-real-model-admin}"
export BOOTSTRAP_ADMIN_PASSWORD="${BOOTSTRAP_ADMIN_PASSWORD:-release-real-model-password-2026}"
export EMBED_DIMENSION="${EMBED_DIMENSION:-768}"
export STORE_COLLECTION="${STORE_COLLECTION:-release-real-model-documents}"
export AGENT_PLANNER_TYPE="auto"
export AGENT_PLANNER_TIMEOUT="${AGENT_PLANNER_TIMEOUT:-180s}"
export AGENT_PLANNER_MAX_TOKENS="${AGENT_PLANNER_MAX_TOKENS:-1200}"
export AGENT_REVIEW_MAX_TOKEN_BUDGET="${AGENT_REVIEW_MAX_TOKEN_BUDGET:-32000}"
export LLM_ENDPOINT="${LLM_ENDPOINT:-http://host.docker.internal:11434/v1/chat/completions}"
export LLM_MODEL="${LLM_MODEL:-qwen2.5:1.5b}"
export EMBED_ENDPOINT="${EMBED_ENDPOINT:-http://host.docker.internal:11434/api/embeddings}"
export EMBED_MODEL="${EMBED_MODEL:-nomic-embed-text}"
export GOVERNANCE_REPORT_DIR="${GOVERNANCE_REPORT_DIR:-${ROOT_DIR}/artifacts/release-center-real-model-scenarios-acceptance}"
REPORT_PATH="${GOVERNANCE_REPORT_DIR}/release-center-real-model-scenarios-$(date -u +%Y%m%dT%H%M%SZ).json"

case "${LLM_ENDPOINT,,} ${LLM_MODEL,,}" in
  *mock*|*localhost:18080*|*127.0.0.1:18080*)
    echo "LLM_ENDPOINT/LLM_MODEL must point to a real model" >&2
    exit 2
    ;;
esac

cleanup() {
  local code=$?
  if [[ "${RELEASE_REAL_MODEL_KEEP_SERVICES:-0}" != "1" ]]; then
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
python3 scripts/release-center-real-model-scenarios-acceptance.py \
  --api-url "http://127.0.0.1:${API_PORT}" \
  --report "${REPORT_PATH}" \
  --timeout "${MAX_WAIT_SECONDS:-600}"
echo "[release-center-real-model-scenarios] retained report: ${REPORT_PATH}"
