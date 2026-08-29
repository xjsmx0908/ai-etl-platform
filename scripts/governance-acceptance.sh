#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-ai-etl-governance-acceptance}"
export COMPOSE_FILE="docker-compose.yml:docker-compose.eval.yml:docker-compose.smoke.yml:docker-compose.governance.yml"
export API_PORT="${API_PORT:-8083}"
export WEB_PORT="${WEB_PORT:-3103}"
export QUERY_API_HOST_PORT="${API_PORT}"
export WORKER_METRICS_HOST_PORT="${WORKER_METRICS_HOST_PORT:-18084}"
export ENVIRONMENT=staging
export WORKER_REPLICAS=1
export API_REPLICAS=1
export EMBED_DIMENSION="${EMBED_DIMENSION:-768}"
export STORE_COLLECTION="${STORE_COLLECTION:-documents-governance-acceptance}"
export JWT_SECRET="${JWT_SECRET:-governance-acceptance-jwt-secret-2026}"
export BOOTSTRAP_ADMIN_TENANT="${BOOTSTRAP_ADMIN_TENANT:-tenant-governance-acceptance}"
export BOOTSTRAP_ADMIN_USERNAME="${BOOTSTRAP_ADMIN_USERNAME:-governance-admin}"
export BOOTSTRAP_ADMIN_PASSWORD="${BOOTSTRAP_ADMIN_PASSWORD:-governance-admin-password-2026}"
export GOVERNANCE_REVIEWER_USERNAME="${GOVERNANCE_REVIEWER_USERNAME:-governance-reviewer}"
export GOVERNANCE_REVIEWER_PASSWORD="${GOVERNANCE_REVIEWER_PASSWORD:-governance-reviewer-password-2026}"
export RETRIEVAL_DIAGNOSTICS_ENABLED=true
export OUTBOX_RELAY_POLL_INTERVAL="${OUTBOX_RELAY_POLL_INTERVAL:-1s}"
export DELETION_INTERVAL="${DELETION_INTERVAL:-2s}"
export DELETION_RETRY_BACKOFF="${DELETION_RETRY_BACKOFF:-2s}"
export INDEX_RETENTION_ENABLED=false

MOCK_PORT="${MOCK_PORT:-18083}"
export EMBED_MODEL=governance-acceptance-embed
export LLM_MODEL=governance-acceptance-chat
export EMBED_ENDPOINT="http://host.docker.internal:${MOCK_PORT}/v1/embeddings"
export LLM_ENDPOINT="http://host.docker.internal:${MOCK_PORT}/v1/chat/completions"

MAX_WAIT_SECONDS="${MAX_WAIT_SECONDS:-300}"
GOVERNANCE_REPORT_DIR="${GOVERNANCE_REPORT_DIR:-${ROOT_DIR}/artifacts/governance-acceptance}"
REPORT_PATH="${GOVERNANCE_REPORT_DIR}/governance-acceptance-$(date -u +%Y%m%dT%H%M%SZ).json"
TMP_DIR="$(mktemp -d)"
MOCK_LOG="${TMP_DIR}/mock-openai.log"

cleanup() {
  local code=$?
  if [[ -n "${MOCK_PID:-}" ]]; then
    kill "${MOCK_PID}" >/dev/null 2>&1 || true
    wait "${MOCK_PID}" >/dev/null 2>&1 || true
  fi
  if [[ "${GOVERNANCE_KEEP_SERVICES:-0}" != "1" ]]; then
    docker compose down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  rm -rf "${TMP_DIR}"
  exit "${code}"
}
trap cleanup EXIT

python3 scripts/mock-openai-server.py --port "${MOCK_PORT}" --dim "${EMBED_DIMENSION}" >"${MOCK_LOG}" 2>&1 &
MOCK_PID=$!
for _ in $(seq 1 30); do
  curl -fsS "http://127.0.0.1:${MOCK_PORT}/healthz" >/dev/null && break
  sleep 1
done
curl -fsS "http://127.0.0.1:${MOCK_PORT}/healthz" >/dev/null

echo "[governance] starting isolated full stack"
compose_up=(up -d)
if [[ "${GOVERNANCE_BUILD:-1}" == "1" ]]; then
  compose_up+=(--build)
fi
docker compose "${compose_up[@]}" --quiet-pull

api_healthy=0
for _ in $(seq 1 90); do
  if curl -fsS "http://127.0.0.1:${API_PORT}/healthz" >/dev/null; then
    api_healthy=1
    break
  fi
  sleep 2
done
if [[ "${api_healthy}" != "1" ]]; then
  docker compose logs --tail=160 query-api etl-worker parser-service >&2 || true
  exit 1
fi

docker compose exec -T kafka \
  kafka-topics.sh --bootstrap-server localhost:9092 --create --if-not-exists \
  --topic "${KAFKA_TOPIC:-doc-processing}" --partitions 1 --replication-factor 1 >/dev/null
docker compose restart etl-worker >/dev/null

python3 scripts/governance-acceptance.py \
  --api-url "http://127.0.0.1:${API_PORT}" \
  --worker-metrics-url "http://127.0.0.1:${WORKER_METRICS_HOST_PORT}/metrics" \
  --report "${REPORT_PATH}" \
  --timeout "${MAX_WAIT_SECONDS}"

echo "[governance] retained report: ${REPORT_PATH}"
