#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

export COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-ai-etl-ingestion-crash}"
export COMPOSE_FILE="docker-compose.yml:docker-compose.eval.yml:docker-compose.smoke.yml"
export API_PORT="${API_PORT:-8082}"
export WEB_PORT="${WEB_PORT:-3102}"
export QUERY_API_HOST_PORT="${API_PORT}"
export ENVIRONMENT=staging
export WORKER_REPLICAS=1
export API_REPLICAS=1
export EMBED_DIMENSION="${EMBED_DIMENSION:-768}"
export STORE_COLLECTION="${STORE_COLLECTION:-documents-ingestion-crash}"
export JWT_SECRET="${JWT_SECRET:-change-me-in-production-please-use-32-plus-chars}"
export BOOTSTRAP_ADMIN_TENANT="${BOOTSTRAP_ADMIN_TENANT:-tenant-ingestion-crash}"
export BOOTSTRAP_ADMIN_USERNAME="${BOOTSTRAP_ADMIN_USERNAME:-crash-admin}"
export BOOTSTRAP_ADMIN_PASSWORD="${BOOTSTRAP_ADMIN_PASSWORD:-crash-admin-password-2026}"
export OUTBOX_RELAY_POLL_INTERVAL="${OUTBOX_RELAY_POLL_INTERVAL:-2s}"
export INGESTION_METRICS_INTERVAL="${INGESTION_METRICS_INTERVAL:-1s}"
export INGESTION_JOB_LEASE="${INGESTION_JOB_LEASE:-75m}"

MOCK_PORT="${MOCK_PORT:-18082}"
export EMBED_MODEL=crash-embed
export LLM_MODEL=crash-chat
export EMBED_ENDPOINT="http://host.docker.internal:${MOCK_PORT}/v1/embeddings"
export LLM_ENDPOINT="http://host.docker.internal:${MOCK_PORT}/v1/chat/completions"
MAX_WAIT_SECONDS="${MAX_WAIT_SECONDS:-240}"
TMP_DIR="$(mktemp -d)"
MOCK_LOG="${TMP_DIR}/mock-openai.log"

cleanup() {
  local code=$?
  if [[ -n "${MOCK_PID:-}" ]]; then
    kill "${MOCK_PID}" >/dev/null 2>&1 || true
    wait "${MOCK_PID}" >/dev/null 2>&1 || true
  fi
  docker compose down -v --remove-orphans >/dev/null 2>&1 || true
  rm -rf "${TMP_DIR}"
  exit "${code}"
}
trap cleanup EXIT

wait_health() {
  local deadline=$((SECONDS + MAX_WAIT_SECONDS))
  until curl -fsS "http://127.0.0.1:${API_PORT}/healthz" >/dev/null; do
    (( SECONDS < deadline )) || return 1
    sleep 2
  done
}

python3 scripts/mock-openai-server.py --port "${MOCK_PORT}" --dim "${EMBED_DIMENSION}" >"${MOCK_LOG}" 2>&1 &
MOCK_PID=$!
for _ in $(seq 1 30); do
  curl -fsS "http://127.0.0.1:${MOCK_PORT}/healthz" >/dev/null && break
  sleep 1
done

echo "[crash-e2e] starting isolated stack"
docker compose up -d --build
wait_health

TOKEN="$(curl -fsS -H 'Content-Type: application/json' \
  -d "{\"username\":\"${BOOTSTRAP_ADMIN_USERNAME}\",\"password\":\"${BOOTSTRAP_ADMIN_PASSWORD}\"}" \
  "http://127.0.0.1:${API_PORT}/v1/auth/login" | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])')"

echo "[crash-e2e] stopping worker and Kafka before durable admission"
docker compose stop etl-worker >/dev/null
docker compose stop kafka >/dev/null
printf '%s\n' 'Durable admission survives relay and worker process crashes.' >"${TMP_DIR}/crash.txt"
UPLOAD_STATUS="$(curl -sS -o "${TMP_DIR}/upload.json" -w '%{http_code}' \
  -X POST "http://127.0.0.1:${API_PORT}/v1/upload" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H 'Idempotency-Key: crash-after-admission' \
  -F "file=@${TMP_DIR}/crash.txt;type=text/plain" \
  -F 'permission=internal')"
[[ "${UPLOAD_STATUS}" == "202" ]] || { cat "${TMP_DIR}/upload.json" >&2; exit 1; }
DOC_ID="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["doc_id"])' "${TMP_DIR}/upload.json")"

echo "[crash-e2e] simulating API/relay crash after HTTP 202"
docker compose stop query-api >/dev/null
docker compose up -d kafka >/dev/null
docker compose up -d query-api etl-worker >/dev/null
wait_health

deadline=$((SECONDS + MAX_WAIT_SECONDS))
task_state=""
until [[ "${task_state}" == "completed" ]]; do
  (( SECONDS < deadline )) || { docker compose logs --tail=160 query-api etl-worker kafka >&2; exit 1; }
  task_state="$(curl -fsS -H "Authorization: Bearer ${TOKEN}" \
    "http://127.0.0.1:${API_PORT}/v1/tasks/${DOC_ID}" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status", ""))')"
  [[ "${task_state}" != "failed" ]] || exit 1
  sleep 2
done

document_state="$(curl -fsS -H "Authorization: Bearer ${TOKEN}" \
  "http://127.0.0.1:${API_PORT}/v1/documents/${DOC_ID}" | \
  python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status", "")+":"+d.get("publication_status", ""))')"
[[ "${document_state}" == "completed:published" ]]

metrics="$(curl -fsS "http://127.0.0.1:${API_PORT}/metrics")"
grep -q 'ai_etl_ingestion_outbox_pending' <<<"${metrics}"
grep -q 'ai_etl_ingestion_jobs{status="completed"}' <<<"${metrics}"

echo "[crash-e2e] PASS: HTTP 202 -> relay crash -> Kafka recovery -> completed/published"
