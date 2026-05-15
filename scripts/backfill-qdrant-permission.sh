#!/usr/bin/env bash

set -euo pipefail

QDRANT_ENDPOINT="${QDRANT_ENDPOINT:-http://localhost:6333}"
QDRANT_COLLECTION="${QDRANT_COLLECTION:-documents}"
QDRANT_API_KEY="${QDRANT_API_KEY:-}"
DEFAULT_PERMISSION="${DEFAULT_PERMISSION:-confidential}"
TENANT_ID="${TENANT_ID:-}"
APPLY=0

usage() {
  cat <<'EOF'
Backfill missing `permission` payload in Qdrant points.

Usage:
  scripts/backfill-qdrant-permission.sh [options]

Options:
  --apply                    Apply changes (default is dry-run count only)
  --endpoint <url>           Qdrant endpoint (default: $QDRANT_ENDPOINT or http://localhost:6333)
  --collection <name>        Qdrant collection (default: $QDRANT_COLLECTION or documents)
  --api-key <key>            Qdrant API key (default: $QDRANT_API_KEY)
  --permission <level>       Backfill value: public|internal|confidential (default: confidential)
  --tenant-id <tenant>       Optional tenant scope (default: all tenants)
  -h, --help                 Show help

Examples:
  scripts/backfill-qdrant-permission.sh
  scripts/backfill-qdrant-permission.sh --apply
  scripts/backfill-qdrant-permission.sh --apply --permission confidential --tenant-id tenant-a
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --apply)
      APPLY=1
      shift
      ;;
    --endpoint)
      QDRANT_ENDPOINT="$2"
      shift 2
      ;;
    --collection)
      QDRANT_COLLECTION="$2"
      shift 2
      ;;
    --api-key)
      QDRANT_API_KEY="$2"
      shift 2
      ;;
    --permission)
      DEFAULT_PERMISSION="$2"
      shift 2
      ;;
    --tenant-id)
      TENANT_ID="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage
      exit 2
      ;;
  esac
done

case "${DEFAULT_PERMISSION}" in
  public|internal|confidential)
    ;;
  *)
    echo "invalid --permission: ${DEFAULT_PERMISSION}" >&2
    exit 2
    ;;
esac

PYTHON_BIN="$(command -v python3 || command -v python || true)"
if [[ -z "${PYTHON_BIN}" ]]; then
  echo "python3/python is required to run this script" >&2
  exit 2
fi

call_qdrant() {
  local method="$1"
  local url="$2"
  local body="$3"

  if [[ -n "${QDRANT_API_KEY}" ]]; then
    curl -fsS -X "${method}" \
      "${url}" \
      -H "Content-Type: application/json" \
      -H "api-key: ${QDRANT_API_KEY}" \
      --data "${body}"
  else
    curl -fsS -X "${method}" \
      "${url}" \
      -H "Content-Type: application/json" \
      --data "${body}"
  fi
}

build_filter_json() {
  "${PYTHON_BIN}" - "$TENANT_ID" <<'PY'
import json
import sys

tenant_id = sys.argv[1].strip()
must = [{"is_empty": {"key": "permission"}}]
if tenant_id:
    must.append({"key": "tenant_id", "match": {"value": tenant_id}})

print(json.dumps({"must": must}, ensure_ascii=False))
PY
}

build_count_body() {
  local filter_json="$1"
  "${PYTHON_BIN}" - "$filter_json" <<'PY'
import json
import sys

f = json.loads(sys.argv[1])
print(json.dumps({"filter": f, "exact": True}, ensure_ascii=False))
PY
}

extract_count() {
  "${PYTHON_BIN}" - <<'PY'
import json
import sys

data = json.load(sys.stdin)
print(int(data.get("result", {}).get("count", 0)))
PY
}

filter_json="$(build_filter_json)"
count_body="$(build_count_body "${filter_json}")"

count_url="${QDRANT_ENDPOINT}/collections/${QDRANT_COLLECTION}/points/count"
set_payload_url="${QDRANT_ENDPOINT}/collections/${QDRANT_COLLECTION}/points/payload?wait=true"
create_index_url="${QDRANT_ENDPOINT}/collections/${QDRANT_COLLECTION}/index?wait=true"

before_resp="$(call_qdrant POST "${count_url}" "${count_body}")"
before_count="$(printf '%s' "${before_resp}" | extract_count)"

echo "Qdrant endpoint  : ${QDRANT_ENDPOINT}"
echo "Collection       : ${QDRANT_COLLECTION}"
echo "Tenant scope     : ${TENANT_ID:-ALL}"
echo "Backfill value   : ${DEFAULT_PERMISSION}"
echo "Missing count    : ${before_count}"

if [[ "${APPLY}" -ne 1 ]]; then
  echo "Dry-run only. Re-run with --apply to execute backfill."
  exit 0
fi

if [[ "${before_count}" -eq 0 ]]; then
  echo "No points require backfill."
  exit 0
fi

# Create payload index for query-time filtering performance.
index_body='{"field_name":"permission","field_schema":"keyword"}'
if ! call_qdrant PUT "${create_index_url}" "${index_body}" >/dev/null; then
  echo "warning: failed to create permission payload index (continuing)" >&2
fi

update_body="$("${PYTHON_BIN}" - "${filter_json}" "${DEFAULT_PERMISSION}" <<'PY'
import json
import sys

f = json.loads(sys.argv[1])
permission = sys.argv[2]
print(json.dumps({"filter": f, "payload": {"permission": permission}}, ensure_ascii=False))
PY
)"

call_qdrant POST "${set_payload_url}" "${update_body}" >/dev/null

after_resp="$(call_qdrant POST "${count_url}" "${count_body}")"
after_count="$(printf '%s' "${after_resp}" | extract_count)"

updated=$((before_count - after_count))
echo "Backfill applied. Updated points: ${updated}"
echo "Remaining missing permission points: ${after_count}"
echo "Note: defaulting to '${DEFAULT_PERMISSION}' is least-privilege fallback. Re-ingest for precise ACL labels."
