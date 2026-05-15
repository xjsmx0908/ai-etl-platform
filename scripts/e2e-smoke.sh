#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

MOCK_PORT="${MOCK_PORT:-18080}"
EMBED_DIMENSION="${EMBED_DIMENSION:-8}"
TENANT_ID="${TENANT_ID:-tenant-e2e}"
QUERY_TOP_K="${QUERY_TOP_K:-3}"
MAX_WAIT_SECONDS="${MAX_WAIT_SECONDS:-180}"
JWT_SECRET="${JWT_SECRET:-change-me-in-production-please-use-32-plus-chars}"
E2E_KEEP_SERVICES="${E2E_KEEP_SERVICES:-0}"
DOC_PERMISSION="${DOC_PERMISSION:-internal}"

TMP_DIR="$(mktemp -d)"
MOCK_LOG="${TMP_DIR}/mock-openai.log"
DOC_FILE="${TMP_DIR}/smoke-doc.txt"
TOKEN_GO_FILE="${ROOT_DIR}/services/etl-worker/tmp_e2e_gen_token.go"

cleanup() {
  local code=$?
  if [[ -n "${MOCK_PID:-}" ]]; then
    kill "${MOCK_PID}" >/dev/null 2>&1 || true
    wait "${MOCK_PID}" >/dev/null 2>&1 || true
  fi
  rm -f "${TOKEN_GO_FILE}" >/dev/null 2>&1 || true
  if [[ "${E2E_KEEP_SERVICES}" != "1" ]]; then
    docker compose down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  rm -rf "${TMP_DIR}"
  exit "${code}"
}
trap cleanup EXIT

echo "[e2e] starting mock model server on :${MOCK_PORT}"
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
export EMBED_MODEL=smoke-embed
export LLM_MODEL=smoke-chat
export EMBED_ENDPOINT="http://host.docker.internal:${MOCK_PORT}/v1/embeddings"
export LLM_ENDPOINT="http://host.docker.internal:${MOCK_PORT}/v1/chat/completions"
export JWT_SECRET
export REDIS_ADDR=redis:6379
export REDIS_DB=0

echo "[e2e] starting docker compose stack"
docker compose up -d --build

echo "[e2e] waiting query-api healthz"
api_healthy=0
for _ in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:8080/healthz" >/dev/null; then
    api_healthy=1
    break
  fi
  sleep 2
done

if [[ "${api_healthy}" != "1" ]]; then
  echo "[e2e] query-api not healthy in time" >&2
  docker compose logs --tail=120 query-api >&2 || true
  exit 1
fi

cat >"${TOKEN_GO_FILE}" <<'EOF'
package main

import (
	"fmt"
	"os"

	"ai-etl-pipeline/internal/auth"
)

func main() {
	token, err := auth.GenerateTestToken(os.Args[1], os.Args[2], "e2e-user", []string{"upload", "query"})
	if err != nil {
		panic(err)
	}
	fmt.Println(token)
}
EOF

TOKEN="$(
docker run --rm \
  -v "${ROOT_DIR}:/workspace" \
  -w /workspace/services/etl-worker \
  golang:1.24 \
  sh -c "go run /workspace/services/etl-worker/tmp_e2e_gen_token.go \"${JWT_SECRET}\" \"${TENANT_ID}\""
)"

cat >"${DOC_FILE}" <<'EOF'
AI ETL smoke test document.
This file is used to verify upload, parsing, embedding, indexing and query pipeline.
EOF

echo "[e2e] uploading sample document"
UPLOAD_STATUS="$(
curl -sS -o "${TMP_DIR}/upload.json" -w "%{http_code}" \
  -X POST "http://127.0.0.1:8080/v1/upload" \
  -H "Authorization: Bearer ${TOKEN}" \
  -F "file=@${DOC_FILE};type=text/plain" \
  -F "permission=${DOC_PERMISSION}"
)"

if [[ "${UPLOAD_STATUS}" != "202" ]]; then
  echo "[e2e] upload failed, status=${UPLOAD_STATUS}" >&2
  cat "${TMP_DIR}/upload.json" >&2 || true
  exit 1
fi

echo "[e2e] polling /v1/query until indexed"
deadline=$((SECONDS + MAX_WAIT_SECONDS))
success=0
while (( SECONDS < deadline )); do
  QUERY_STATUS="$(
    curl -sS -o "${TMP_DIR}/query.json" -w "%{http_code}" \
      -X POST "http://127.0.0.1:8080/v1/query" \
      -H "Authorization: Bearer ${TOKEN}" \
      -H "Content-Type: application/json" \
      -d "{\"question\":\"What does the smoke test verify?\",\"top_k\":${QUERY_TOP_K}}"
  )"

  if [[ "${QUERY_STATUS}" == "200" ]]; then
    if python3 - "${TMP_DIR}/query.json" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1], "r", encoding="utf-8"))
sources = data.get("sources") or []
if sources:
    print("sources:", len(sources))
    sys.exit(0)
sys.exit(1)
PY
    then
      success=1
      break
    fi
  fi
  sleep 3
done

if [[ "${success}" != "1" ]]; then
  echo "[e2e] query did not return indexed sources within ${MAX_WAIT_SECONDS}s" >&2
  cat "${TMP_DIR}/query.json" >&2 || true
  docker compose logs --tail=120 query-api etl-worker parser-service >&2 || true
  echo "[e2e] mock model log:" >&2
  tail -n 80 "${MOCK_LOG}" >&2 || true
  exit 1
fi

echo "[e2e] PASS: upload -> kafka -> parse/embed/store -> query"
