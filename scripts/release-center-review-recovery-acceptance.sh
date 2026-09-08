#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"
export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-ai-etl-release-review-recovery}"
export COMPOSE_FILE="docker-compose.yml:docker-compose.eval.yml:docker-compose.smoke.yml:docker-compose.governance.yml"
export API_PORT="${API_PORT:-8086}"
export QUERY_API_HOST_PORT="${API_PORT}"
export WEB_PORT="${WEB_PORT:-3106}"
export ENVIRONMENT=staging
export JWT_SECRET="${JWT_SECRET:-release-review-recovery-jwt-secret-2026}"
export BOOTSTRAP_ADMIN_TENANT="${BOOTSTRAP_ADMIN_TENANT:-release-review-recovery-tenant}"
export BOOTSTRAP_ADMIN_USERNAME="${BOOTSTRAP_ADMIN_USERNAME:-release-review-recovery-admin}"
export BOOTSTRAP_ADMIN_PASSWORD="${BOOTSTRAP_ADMIN_PASSWORD:-release-review-recovery-password-2026}"
export EMBED_ENDPOINT="${EMBED_ENDPOINT:-http://host.docker.internal:11434/api/embeddings}"
export EMBED_MODEL="${EMBED_MODEL:-bge-m3}"
export EMBED_DIMENSION="${EMBED_DIMENSION:-1024}"
export STORE_COLLECTION="${STORE_COLLECTION:-release-review-recovery-documents}"
export AGENT_PLANNER_TYPE="auto"
export AGENT_LOCK_TTL="${AGENT_LOCK_TTL:-10s}"
export AGENT_PLANNER_TIMEOUT="${AGENT_PLANNER_TIMEOUT:-120s}"
export AGENT_PLANNER_MAX_TOKENS="${AGENT_PLANNER_MAX_TOKENS:-1200}"
export LLM_MODEL="${LLM_MODEL:-review-recovery-mock}"
PLANNER_PORT="${REVIEW_PLANNER_PORT:-18081}"
export LLM_ENDPOINT="http://host.docker.internal:${PLANNER_PORT}/v1"
export GOVERNANCE_REPORT_DIR="${GOVERNANCE_REPORT_DIR:-${ROOT_DIR}/artifacts/release-center-review-recovery-acceptance}"
mkdir -p "${GOVERNANCE_REPORT_DIR}"
export REVIEW_PLANNER_HOLD_PATH="${REVIEW_PLANNER_HOLD_PATH:-${GOVERNANCE_REPORT_DIR}/planner.hold}"
REPORT_PATH="${GOVERNANCE_REPORT_DIR}/release-center-review-recovery-$(date -u +%Y%m%dT%H%M%SZ).json"
touch "${REVIEW_PLANNER_HOLD_PATH}"
MOCK_PID=""

cleanup() {
  local code=$?
  if [[ -n "${MOCK_PID}" ]]; then
    kill "${MOCK_PID}" >/dev/null 2>&1 || true
    wait "${MOCK_PID}" >/dev/null 2>&1 || true
  fi
  rm -f "${REVIEW_PLANNER_HOLD_PATH}"
  if [[ "${RELEASE_REVIEW_RECOVERY_KEEP_SERVICES:-0}" != "1" ]]; then
    docker compose down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  exit "${code}"
}
trap cleanup EXIT

python3 "${ROOT_DIR}/scripts/review-agent-planner-mock.py" \
  --host 0.0.0.0 \
  --port "${PLANNER_PORT}" \
  --hold-path "${REVIEW_PLANNER_HOLD_PATH}" \
  --hold-after-tools 1 \
  --hold-timeout "${MAX_WAIT_SECONDS:-300}" &
MOCK_PID=$!
for _ in $(seq 1 30); do
  curl -fsS "http://127.0.0.1:${PLANNER_PORT}/healthz" >/dev/null && break
  sleep 1
done
curl -fsS "http://127.0.0.1:${PLANNER_PORT}/healthz" >/dev/null

docker compose up -d --build --quiet-pull
for _ in $(seq 1 90); do
  curl -fsS "http://127.0.0.1:${API_PORT}/healthz" >/dev/null && break
  sleep 2
done
curl -fsS "http://127.0.0.1:${API_PORT}/healthz" >/dev/null
python3 scripts/release-center-review-recovery-acceptance.py \
  --api-url "http://127.0.0.1:${API_PORT}" \
  --report "${REPORT_PATH}" \
  --timeout "${MAX_WAIT_SECONDS:-300}"
echo "[release-center-review-recovery] retained report: ${REPORT_PATH}"
