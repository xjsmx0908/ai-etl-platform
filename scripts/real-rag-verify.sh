#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

usage() {
  cat <<'EOF'
Usage:
  scripts/real-rag-verify.sh <real_file_path> <question>

Example:
  scripts/real-rag-verify.sh ./README.md "这个项目的基础设施层包含哪些组件？"

Environment variables (optional):
  TENANT_ID=tenant-real-verify
  DOC_PERMISSION=internal
  QUERY_TOP_K=3
  MAX_WAIT_SECONDS=180
  API_BASE_URL=http://127.0.0.1:8080
  PARSER_HEALTH_URL=http://127.0.0.1:8000/healthz
  QDRANT_BASE_URL=http://127.0.0.1:6333
  KEEP_ARTIFACTS=0
EOF
}

if [[ $# -lt 2 ]]; then
  usage
  exit 1
fi

REAL_FILE="$1"
shift
QUESTION="$*"

TENANT_ID="${TENANT_ID:-tenant-real-verify}"
DOC_PERMISSION="${DOC_PERMISSION:-internal}"
QUERY_TOP_K="${QUERY_TOP_K:-3}"
MAX_WAIT_SECONDS="${MAX_WAIT_SECONDS:-180}"
API_BASE_URL="${API_BASE_URL:-http://127.0.0.1:8080}"
PARSER_HEALTH_URL="${PARSER_HEALTH_URL:-http://127.0.0.1:8000/healthz}"
QDRANT_BASE_URL="${QDRANT_BASE_URL:-http://127.0.0.1:6333}"
KEEP_ARTIFACTS="${KEEP_ARTIFACTS:-0}"

for cmd in docker curl python3; do
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "[real-rag] missing command: ${cmd}" >&2
    exit 1
  fi
done

if [[ ! -f "${REAL_FILE}" ]]; then
  echo "[real-rag] file not found: ${REAL_FILE}" >&2
  exit 1
fi

TMP_DIR="$(mktemp -d)"
TOKEN_GO_FILE="${ROOT_DIR}/services/etl-worker/tmp_real_rag_gen_token.go"
UPLOAD_JSON="${TMP_DIR}/upload.json"
QUERY_JSON="${TMP_DIR}/query.json"
QUERY_BODY_JSON="${TMP_DIR}/query-body.json"
QDRANT_REQ_JSON="${TMP_DIR}/qdrant-req.json"
QDRANT_RESP_JSON="${TMP_DIR}/qdrant-resp.json"

cleanup() {
  local code=$?
  rm -f "${TOKEN_GO_FILE}" >/dev/null 2>&1 || true
  if [[ "${KEEP_ARTIFACTS}" != "1" ]]; then
    rm -rf "${TMP_DIR}"
  else
    echo "[real-rag] kept artifacts at: ${TMP_DIR}"
  fi
  exit "${code}"
}
trap cleanup EXIT

echo "[real-rag] 1/6 health checks"
curl -fsS "${API_BASE_URL}/healthz" >/dev/null
curl -fsS "${PARSER_HEALTH_URL}" >/dev/null

echo "[real-rag] 2/6 generating JWT token"
cat >"${TOKEN_GO_FILE}" <<'EOF'
package main

import (
	"fmt"
	"os"

	"ai-etl-pipeline/internal/auth"
)

func main() {
	token, err := auth.GenerateTestToken(os.Args[1], os.Args[2], "real-rag-user", []string{"upload", "query"})
	if err != nil {
		panic(err)
	}
	fmt.Println(token)
}
EOF

JWT_SECRET="$(
  docker compose exec -T query-api sh -c '
    if [ -n "${JWT_SECRET:-}" ]; then
      printf "%s" "${JWT_SECRET}"
    elif [ -f /run/secrets/jwt_secret ]; then
      cat /run/secrets/jwt_secret
    fi
  ' 2>/dev/null || true
)"

if [[ -z "${JWT_SECRET}" ]]; then
  echo "[real-rag] could not read JWT secret from query-api" >&2
  exit 1
fi

TOKEN="$(
  docker run --rm \
    -v "${ROOT_DIR}:/workspace" \
    -w /workspace/services/etl-worker \
    golang:1.25 \
    sh -c "go run /workspace/services/etl-worker/tmp_real_rag_gen_token.go \"${JWT_SECRET}\" \"${TENANT_ID}\""
)"

if [[ -z "${TOKEN}" ]]; then
  echo "[real-rag] failed to generate JWT token" >&2
  exit 1
fi

echo "[real-rag] 3/6 uploading file: ${REAL_FILE}"
UPLOAD_STATUS="$(
  curl -sS -o "${UPLOAD_JSON}" -w "%{http_code}" \
    -X POST "${API_BASE_URL}/v1/upload" \
    -H "Authorization: Bearer ${TOKEN}" \
    -F "file=@${REAL_FILE}" \
    -F "permission=${DOC_PERMISSION}"
)"
echo "[real-rag] upload status=${UPLOAD_STATUS}"

if [[ "${UPLOAD_STATUS}" != "202" ]]; then
  echo "[real-rag] upload failed" >&2
  cat "${UPLOAD_JSON}" >&2 || true
  exit 1
fi

DOC_ID="$(
  python3 - "${UPLOAD_JSON}" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1], "r", encoding="utf-8"))
print(data["doc_id"])
PY
)"
echo "[real-rag] doc_id=${DOC_ID}"

python3 - "${QUESTION}" "${QUERY_TOP_K}" "${QUERY_BODY_JSON}" <<'PY'
import json
import sys

question = sys.argv[1]
top_k = int(sys.argv[2])
path = sys.argv[3]

with open(path, "w", encoding="utf-8") as f:
    json.dump({"question": question, "top_k": top_k}, f, ensure_ascii=False)
PY

echo "[real-rag] 4/6 polling query until hit doc_id"
deadline=$((SECONDS + MAX_WAIT_SECONDS))
query_ok=0
last_query_status=""
while (( SECONDS < deadline )); do
  last_query_status="$(
    curl -sS -o "${QUERY_JSON}" -w "%{http_code}" \
      -X POST "${API_BASE_URL}/v1/query" \
      -H "Authorization: Bearer ${TOKEN}" \
      -H "Content-Type: application/json" \
      --data @"${QUERY_BODY_JSON}"
  )"

  if [[ "${last_query_status}" == "200" ]]; then
    if python3 - "${DOC_ID}" "${QUERY_JSON}" <<'PY'
import json
import sys

doc_id = sys.argv[1]
path = sys.argv[2]
data = json.load(open(path, "r", encoding="utf-8"))
sources = data.get("sources") or []
hit = any((s.get("doc_id") == doc_id) for s in sources)
print(f"[real-rag] sources={len(sources)} doc_hit={hit}")
answer = (data.get("answer") or "").replace("\n", " ")
if answer:
    print("[real-rag] answer preview:", answer[:220])
sys.exit(0 if hit else 1)
PY
    then
      query_ok=1
      break
    fi
  fi
  sleep 3
done

echo "[real-rag] query status=${last_query_status:-N/A}"
if [[ "${query_ok}" != "1" ]]; then
  echo "[real-rag] query did not hit doc_id within ${MAX_WAIT_SECONDS}s" >&2
  cat "${QUERY_JSON}" >&2 || true
  exit 1
fi

echo "[real-rag] 5/6 verifying points in qdrant"
COLLECTION="$(
  docker compose exec -T query-api sh -c 'echo -n "${STORE_COLLECTION:-documents}"'
)"

cat >"${QDRANT_REQ_JSON}" <<EOF
{
  "limit": 10,
  "with_payload": true,
  "with_vector": false,
  "filter": {
    "must": [
      { "key": "doc_id", "match": { "value": "${DOC_ID}" } },
      { "key": "tenant_id", "match": { "value": "${TENANT_ID}" } }
    ]
  }
}
EOF

curl -sS "${QDRANT_BASE_URL}/collections/${COLLECTION}/points/scroll" \
  -H "Content-Type: application/json" \
  -d @"${QDRANT_REQ_JSON}" >"${QDRANT_RESP_JSON}"

python3 - "${QDRANT_RESP_JSON}" <<'PY'
import json
import sys

data = json.load(open(sys.argv[1], "r", encoding="utf-8"))
points = ((data.get("result") or {}).get("points") or [])
print(f"[real-rag] qdrant points={len(points)}")
if len(points) <= 0:
    sys.exit(1)
PY

echo "[real-rag] 6/6 PASS: real file rag verification done"
echo "[real-rag] upload response:"
cat "${UPLOAD_JSON}"
echo
echo "[real-rag] query response:"
cat "${QUERY_JSON}"
echo
