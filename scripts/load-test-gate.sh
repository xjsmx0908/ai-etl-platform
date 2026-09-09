#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

MOCK_PORT="${MOCK_PORT:-18082}"
API_PORT="${API_PORT:-8082}"
WEB_PORT="${WEB_PORT:-3102}"
EMBED_DIMENSION="${EMBED_DIMENSION:-768}"
export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-ai-etl-loadtest}"
export COMPOSE_FILE="docker-compose.yml:docker-compose.eval.yml:docker-compose.smoke.yml"
export QUERY_API_HOST_PORT="${API_PORT}"
TENANT_ID="${TENANT_ID:-tenant-loadtest}"
export BOOTSTRAP_ADMIN_TENANT="${TENANT_ID}"
export BOOTSTRAP_ADMIN_USERNAME="${BOOTSTRAP_ADMIN_USERNAME:-admin}"
export BOOTSTRAP_ADMIN_PASSWORD="${BOOTSTRAP_ADMIN_PASSWORD:-admin}"
JWT_SECRET="${JWT_SECRET:-change-me-in-production-please-use-32-plus-chars}"
KAFKA_TOPIC="${KAFKA_TOPIC:-doc-processing}"
LOADTEST_KEEP_SERVICES="${LOADTEST_KEEP_SERVICES:-0}"
REQUESTS="${REQUESTS:-40}"
CONCURRENCY="${CONCURRENCY:-5}"
REPORT_DIR="${REPORT_DIR:-${ROOT_DIR}/docs/evals/reports}"
export RETRIEVAL_DIAGNOSTICS_ENABLED=true

TMP_DIR="$(mktemp -d)"
MOCK_LOG="${TMP_DIR}/mock-openai.log"

cleanup() {
  local code=$?
  if [[ -n "${MOCK_PID:-}" ]]; then
    kill "${MOCK_PID}" >/dev/null 2>&1 || true
    wait "${MOCK_PID}" >/dev/null 2>&1 || true
  fi
  if [[ "${LOADTEST_KEEP_SERVICES}" != "1" ]]; then
    docker compose down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  rm -rf "${TMP_DIR}"
  exit "${code}"
}
trap cleanup EXIT

echo "[loadtest-gate] starting mock model server on :${MOCK_PORT}"
python3 scripts/mock-openai-server.py --port "${MOCK_PORT}" --dim "${EMBED_DIMENSION}" >"${MOCK_LOG}" 2>&1 &
MOCK_PID=$!
for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:${MOCK_PORT}/healthz" >/dev/null; then
    break
  fi
  sleep 1
done
curl -fsS "http://127.0.0.1:${MOCK_PORT}/healthz" >/dev/null

export ENVIRONMENT=staging
export WORKER_REPLICAS=1
export API_REPLICAS=1
export EMBED_DIMENSION
export STORE_COLLECTION="${STORE_COLLECTION:-documents-loadtest}"
export REDIS_CACHE_ADDR=redis-cache:6379
export REDIS_CACHE_DB=0
export REDIS_STATE_ADDR=redis-state:6379
export REDIS_STATE_DB=0
export EMBED_MODEL=loadtest-embed
export LLM_MODEL=loadtest-chat
export EMBED_ENDPOINT="http://host.docker.internal:${MOCK_PORT}/v1/embeddings"
export LLM_ENDPOINT="http://host.docker.internal:${MOCK_PORT}/v1/chat/completions"
export JWT_SECRET
export API_PORT
export WEB_PORT

echo "[loadtest-gate] starting isolated docker compose stack"
docker compose up -d --build

echo "[loadtest-gate] waiting query-api healthz"
api_healthy=0
for _ in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:${API_PORT}/healthz" >/dev/null; then
    api_healthy=1
    break
  fi
  sleep 2
done
if [[ "${api_healthy}" != "1" ]]; then
  echo "[loadtest-gate] query-api not healthy in time" >&2
  docker compose logs --tail=120 query-api >&2 || true
  exit 1
fi

echo "[loadtest-gate] ensuring kafka topic exists: ${KAFKA_TOPIC}"
docker compose exec -T kafka \
  kafka-topics.sh \
  --bootstrap-server localhost:9092 \
  --create \
  --if-not-exists \
  --topic "${KAFKA_TOPIC}" \
  --partitions 1 \
  --replication-factor 1 >/dev/null

echo "[loadtest-gate] restarting etl-worker after topic creation"
docker compose restart etl-worker >/dev/null
sleep 5

run_profile() {
  local profile="$1"
  echo "[loadtest-gate] running profile ${profile}"
  python3 scripts/load-test.py \
    --api-base "http://127.0.0.1:${API_PORT}" \
    --keep-mock-server \
    --username "${BOOTSTRAP_ADMIN_USERNAME}" \
    --password "${BOOTSTRAP_ADMIN_PASSWORD}" \
    --profile "${profile}" \
    --requests "${REQUESTS}" \
    --concurrency "${CONCURRENCY}" \
    --report-dir "${REPORT_DIR}"
}

run_profile cold-retrieval
run_profile cached-e2e

echo "[loadtest-gate] PASS"
