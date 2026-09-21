#!/usr/bin/env bash
#
# Back up a deployed ai-etl-platform stack.
#
# What is authoritative and what is not (docs/adr/0010-production-slo-and-recovery-gates.md):
#   * PostgreSQL catalog + audit data  -> authoritative, backed up with pg_dump
#   * immutable source objects (MinIO) -> authoritative, backed up as a volume archive
#   * Qdrant / Elasticsearch           -> rebuildable projections *in principle*
#
# The projections are snapshotted anyway, because the rebuild path is not
# self-contained: re-embedding calls EMBED_ENDPOINT, which in this deployment is
# ollama on the Docker host (host.docker.internal:11434), outside the stack and
# outside any backup. If the host is lost the projections cannot be rebuilt at
# all, so per ADR-0010 snapshots are required rather than optional.
#
# The manifest also carries an integrity verdict. A backup of a catalog whose
# source objects are already gone is not a backup of a working system, and
# nothing in the platform says so: `documents` rows stay `completed`, retrieval
# keeps answering, and index manifests stay healthy. `integrity.status=degraded`
# is that missing signal.
#
# Usage:
#   scripts/backup-stack.sh
#
# Environment:
#   BACKUP_ROOT         destination root (default ~/backups/ai-etl-platform)
#   RETENTION           how many timestamped backups to keep (default 7)
#   REQUIRE_INTEGRITY   1 -> exit 3 when the integrity verdict is degraded
#   COMPOSE_PROJECT_NAME / COMPOSE_FILE   which stack to back up
#   MINIO_ENDPOINT / QDRANT_ENDPOINT / ES_ENDPOINT   host-side endpoints
#   S3_ACCESS_KEY_FILE / S3_SECRET_KEY_FILE / STORE_API_KEY_FILE   credentials
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-ai-etl-platform}"
COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.yml}"
export COMPOSE_PROJECT_NAME COMPOSE_FILE

BACKUP_ROOT="${BACKUP_ROOT:-${HOME}/backups/ai-etl-platform}"
RETENTION="${RETENTION:-7}"
REQUIRE_INTEGRITY="${REQUIRE_INTEGRITY:-0}"
PYTHON="${PYTHON:-python3}"

PG_USER="${PG_USER:-app}"
PG_DB="${PG_DB:-ai_etl}"
STORE_COLLECTION="${STORE_COLLECTION:-documents-v2}"
ES_INDEX="${ES_INDEX:-documents_text}"
S3_BUCKET="${S3_BUCKET:-documents}"

MINIO_ENDPOINT="${MINIO_ENDPOINT:-http://127.0.0.1:${MINIO_API_HOST_PORT:-9000}}"
QDRANT_ENDPOINT="${QDRANT_ENDPOINT:-http://127.0.0.1:${QDRANT_HOST_PORT:-6333}}"
ES_ENDPOINT="${ES_ENDPOINT:-http://127.0.0.1:${ELASTICSEARCH_HOST_PORT:-9200}}"

# .env is read key-by-key rather than sourced: it is written for Compose, and a
# value that is legal there (unquoted parentheses, a trailing comment) is not
# necessarily legal shell.
env_value() {
    local key="$1" fallback="${2-}" line=""
    if [[ -f .env ]]; then
        line="$(grep -E "^${key}=" .env | tail -n 1 || true)"
    fi
    if [[ -n "${line}" ]]; then
        printf '%s' "${line#*=}"
    else
        printf '%s' "${fallback}"
    fi
}

secret_file() {
    # secret_file VAR_NAME default-relative-path
    local var="$1" fallback="$2"
    local configured
    configured="$(env_value "${var}" "")"
    if [[ -n "${configured}" ]]; then
        printf '%s' "${configured}"
    else
        printf '%s' "${fallback}"
    fi
}

S3_ACCESS_KEY_PATH="$(secret_file S3_ACCESS_KEY_FILE_PATH ./secrets/examples/s3_access_key)"
S3_SECRET_KEY_PATH="$(secret_file S3_SECRET_KEY_FILE_PATH ./secrets/examples/s3_secret_key)"
STORE_API_KEY_PATH="$(secret_file STORE_API_KEY_FILE_PATH ./secrets/examples/store_api_key)"

TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
BACKUP_DIR="${BACKUP_ROOT}/${TIMESTAMP}"
STATE_FILE="${BACKUP_ROOT}/state.json"
mkdir -p "${BACKUP_ROOT}"

FAILURES=0
INTEGRITY="unknown"

log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
fail() { log "ERROR $*"; FAILURES=$((FAILURES + 1)); }

record_state() {
    local exit_code="$1"
    "${PYTHON}" - "${STATE_FILE}" "${exit_code}" "${INTEGRITY}" "${BACKUP_DIR}" "${TIMESTAMP}" <<'PY'
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

state_path, exit_code, integrity, backup_dir, timestamp = sys.argv[1:6]
path = Path(state_path)
try:
    state = json.loads(path.read_text(encoding="utf-8"))
except Exception:
    state = {}
consecutive = int(state.get("consecutive_failures") or 0)
if int(exit_code) == 0:
    consecutive = 0
else:
    consecutive += 1
record = {
    "last_run_at": datetime.now(timezone.utc).isoformat(),
    "last_exit_code": int(exit_code),
    "last_backup_dir": backup_dir,
    "last_timestamp": timestamp,
    "last_integrity": integrity,
    "consecutive_failures": consecutive,
}
state.update(record)
state["history"] = (state.get("history") or [])[-49:] + [record]
path.write_text(json.dumps(state, indent=2, sort_keys=True) + "\n", encoding="utf-8")
if consecutive >= 2:
    alert = Path(state_path).with_name("ALERT.txt")
    alert.write_text(
        f"backup has failed {consecutive} times in a row; last run {record['last_run_at']}\n",
        encoding="utf-8",
    )
    print(f"ALERT: backup failed {consecutive} consecutive runs", file=sys.stderr)
else:
    Path(state_path).with_name("ALERT.txt").unlink(missing_ok=True)
PY
}

on_exit() {
    local code=$?
    record_state "${code}" || true
    if [[ ${code} -eq 0 && ${FAILURES} -gt 0 ]]; then
        log "WARN completed with ${FAILURES} non-fatal problem(s)"
    fi
}
trap on_exit EXIT

log "backup ${TIMESTAMP} -> ${BACKUP_DIR} (project ${COMPOSE_PROJECT_NAME})"
mkdir -p "${BACKUP_DIR}"

# ---------------------------------------------------------------------------
# 1. PostgreSQL: the authority for versions, generations, releases, decisions.
# ---------------------------------------------------------------------------
pg_container="$(docker compose ps -q postgres)"
if [[ -z "${pg_container}" ]]; then
    log "ERROR postgres container not found in project ${COMPOSE_PROJECT_NAME}"
    exit 2
fi
log "dumping postgres ${PG_DB}"
docker exec "${pg_container}" pg_dump -U "${PG_USER}" -d "${PG_DB}" -Fc -Z 6 \
    > "${BACKUP_DIR}/postgres.dump"
log "  postgres.dump $(stat -c %s "${BACKUP_DIR}/postgres.dump") bytes"

# The catalog's object references are the other half of the integrity check.
docker exec "${pg_container}" psql -U "${PG_USER}" -d "${PG_DB}" -tAc \
    "SELECT object_key FROM documents WHERE object_key <> '' ORDER BY object_key" \
    > "${BACKUP_DIR}/catalog-object-keys.txt"
log "  catalog-object-keys.txt $(wc -l < "${BACKUP_DIR}/catalog-object-keys.txt") keys"

docker exec "${pg_container}" psql -U "${PG_USER}" -d "${PG_DB}" -tAc "
    SELECT json_object_agg(name, n) FROM (
        SELECT 'documents' AS name, count(*) AS n FROM documents
        UNION ALL SELECT 'documents_completed', count(*) FROM documents WHERE status='completed'
        UNION ALL SELECT 'index_manifests', count(*) FROM index_manifests
        UNION ALL SELECT 'index_manifests_active', count(*) FROM index_manifests WHERE state='active'
        UNION ALL SELECT 'release_center_requests', count(*) FROM release_center_requests
        UNION ALL SELECT 'release_center_reviews', count(*) FROM release_center_reviews
        UNION ALL SELECT 'ingestion_jobs', count(*) FROM ingestion_jobs
        UNION ALL SELECT 'ingestion_outbox', count(*) FROM ingestion_outbox
        UNION ALL SELECT 'audit_logs', count(*) FROM audit_logs
        UNION ALL SELECT 'users', count(*) FROM users
    ) counts" > "${BACKUP_DIR}/catalog-counts.json"
log "  catalog counts: $(cat "${BACKUP_DIR}/catalog-counts.json")"

# ---------------------------------------------------------------------------
# 2. Source objects: authoritative, archived as a volume so bucket metadata and
#    object data travel together and a restore needs no S3 client.
# ---------------------------------------------------------------------------
minio_volume="${COMPOSE_PROJECT_NAME}_minio_data"
if docker volume inspect "${minio_volume}" >/dev/null 2>&1; then
    log "archiving minio volume ${minio_volume}"
    docker run --rm \
        -v "${minio_volume}:/data:ro" \
        -v "${BACKUP_DIR}:/backup" \
        alpine:3.20 tar -cf /backup/minio-data.tar -C /data .
    log "  minio-data.tar $(stat -c %s "${BACKUP_DIR}/minio-data.tar") bytes"
else
    fail "minio volume ${minio_volume} not found; source objects are not covered"
fi

# What the store actually answers, which is what the application sees.
if "${PYTHON}" scripts/object-store-inventory.py \
    --endpoint "${MINIO_ENDPOINT}" --bucket "${S3_BUCKET}" \
    --access-key-file "${S3_ACCESS_KEY_PATH}" --secret-key-file "${S3_SECRET_KEY_PATH}" \
    --out "${BACKUP_DIR}/object-inventory.json"; then
    :
else
    fail "object store inventory failed; integrity verdict will be incomplete"
    printf '{"endpoint":"%s","bucket":"%s","total":0,"bytes":0,"objects":{}}\n' \
        "${MINIO_ENDPOINT}" "${S3_BUCKET}" > "${BACKUP_DIR}/object-inventory.json"
fi

# ---------------------------------------------------------------------------
# 3. Projections: Qdrant snapshot, Elasticsearch logical archive.
# ---------------------------------------------------------------------------
store_api_key="$(cat "${STORE_API_KEY_PATH}")"
log "snapshotting qdrant collection ${STORE_COLLECTION}"
snapshot_name="$(curl -fsS -X POST \
    -H "api-key: ${store_api_key}" \
    "${QDRANT_ENDPOINT}/collections/${STORE_COLLECTION}/snapshots" \
    | "${PYTHON}" -c 'import json,sys; print(json.load(sys.stdin)["result"]["name"])')"
curl -fsS -o "${BACKUP_DIR}/qdrant-${STORE_COLLECTION}.snapshot" \
    -H "api-key: ${store_api_key}" \
    "${QDRANT_ENDPOINT}/collections/${STORE_COLLECTION}/snapshots/${snapshot_name}"
log "  qdrant snapshot $(stat -c %s "${BACKUP_DIR}/qdrant-${STORE_COLLECTION}.snapshot") bytes"
# The server-side copy lives inside the Qdrant volume; leaving it there would
# make every backup grow the volume it is backing up.
curl -fsS -X DELETE -H "api-key: ${store_api_key}" \
    "${QDRANT_ENDPOINT}/collections/${STORE_COLLECTION}/snapshots/${snapshot_name}" >/dev/null

log "archiving elasticsearch index ${ES_INDEX}"
"${PYTHON}" scripts/es-index-archive.py dump \
    --address "${ES_ENDPOINT}" --index "${ES_INDEX}" \
    --out "${BACKUP_DIR}/es-${ES_INDEX}.jsonl"
log "  es archive $(stat -c %s "${BACKUP_DIR}/es-${ES_INDEX}.jsonl") bytes"

# Recorded so a restore can prove it replayed the same projection sizes rather
# than merely producing a cluster that answers some queries.
qdrant_points="$(curl -fsS -H "api-key: ${store_api_key}" \
    "${QDRANT_ENDPOINT}/collections/${STORE_COLLECTION}" \
    | "${PYTHON}" -c 'import json,sys; print(json.load(sys.stdin)["result"]["points_count"])')"
es_documents="$(curl -fsS "${ES_ENDPOINT}/${ES_INDEX}/_count" \
    | "${PYTHON}" -c 'import json,sys; print(json.load(sys.stdin)["count"])')"
printf '{"qdrant_collection":"%s","qdrant_points":%s,"es_index":"%s","es_documents":%s}\n' \
    "${STORE_COLLECTION}" "${qdrant_points}" "${ES_INDEX}" "${es_documents}" \
    > "${BACKUP_DIR}/projections.json"
log "  projections: qdrant=${qdrant_points} points, es=${es_documents} documents"

# ---------------------------------------------------------------------------
# 4. Manifest + integrity verdict.
# ---------------------------------------------------------------------------
manifest_args=(
    --dir "${BACKUP_DIR}"
    --project "${COMPOSE_PROJECT_NAME}"
    --environment "$(env_value ENVIRONMENT staging)"
    --catalog-keys "${BACKUP_DIR}/catalog-object-keys.txt"
    --catalog-counts "${BACKUP_DIR}/catalog-counts.json"
    --inventory "${BACKUP_DIR}/object-inventory.json"
    --projections "${BACKUP_DIR}/projections.json"
    --component "postgres=postgres.dump"
    --component "catalog_object_keys=catalog-object-keys.txt"
    --component "qdrant=qdrant-${STORE_COLLECTION}.snapshot"
    --component "elasticsearch=es-${ES_INDEX}.jsonl"
)
if [[ -f "${BACKUP_DIR}/minio-data.tar" ]]; then
    manifest_args+=(--component "minio=minio-data.tar")
fi
if [[ "${REQUIRE_INTEGRITY}" == "1" ]]; then
    manifest_args+=(--require-integrity)
fi

set +e
"${PYTHON}" scripts/backup-manifest.py "${manifest_args[@]}"
manifest_code=$?
set -e
INTEGRITY="$("${PYTHON}" -c \
    'import json,sys; print(json.load(open(sys.argv[1]))["integrity"]["status"])' \
    "${BACKUP_DIR}/manifest.json")"
if [[ ${manifest_code} -eq 3 ]]; then
    log "ERROR integrity is degraded; see manifest.json integrity block"
fi

# ---------------------------------------------------------------------------
# 5. Rotation. Keeps the newest RETENTION timestamped directories.
# ---------------------------------------------------------------------------
mapfile -t existing < <(find "${BACKUP_ROOT}" -maxdepth 1 -type d -name '20*T*Z' -printf '%f\n' | sort)
if (( ${#existing[@]} > RETENTION )); then
    for stale in "${existing[@]:0:$(( ${#existing[@]} - RETENTION ))}"; do
        log "rotating out ${stale}"
        rm -rf -- "${BACKUP_ROOT:?}/${stale}"
    done
fi
ln -sfn "${BACKUP_DIR}" "${BACKUP_ROOT}/latest"

log "backup finished: integrity=${INTEGRITY} dir=${BACKUP_DIR} keep=${RETENTION}"
if [[ ${FAILURES} -gt 0 ]]; then
    exit 2
fi
exit 0
