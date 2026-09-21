#!/usr/bin/env bash
#
# Restore a backup into an isolated stack and prove the restore is real.
#
# The drill never touches the demo stack: it runs under its own compose project
# (default ai-etl-restore) on its own volumes, with docker-compose.eval.yml
# hiding every backing-service host port and docker-compose.restore.yml
# re-exposing only the endpoints the drill needs. Cleanup is a scoped
# `down -v --remove-orphans`, so the demo stack's volumes are unreachable from
# here even by mistake.
#
# What gets restored, and why in this order:
#   postgres      first, because it is the authority; the application applies
#                 migrations on boot, so the target must be empty beforehand
#   minio volume  the immutable source objects, untarred into a fresh volume
#   qdrant        snapshot upload recreates the collection and its vectors
#   elasticsearch logical archive replay
#   query-api     started last so its boot convergence sees restored rows
#
# Usage:
#   scripts/restore-stack.sh [--backup-dir DIR] [--keep]
#
# Environment:
#   BACKUP_ROOT   default ~/backups/ai-etl-platform
#   BACKUP_DIR    default ${BACKUP_ROOT}/latest
#   PROJECT       default ai-etl-restore (must contain "restore")
#   KEEP=1        leave the restored stack running for inspection
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

BACKUP_ROOT="${BACKUP_ROOT:-${HOME}/backups/ai-etl-platform}"
BACKUP_DIR="${BACKUP_DIR:-${BACKUP_ROOT}/latest}"
PROJECT="${PROJECT:-ai-etl-restore}"
KEEP="${KEEP:-0}"
PYTHON="${PYTHON:-python3}"
DRILL_TIMEOUT="${DRILL_TIMEOUT:-600}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --backup-dir) BACKUP_DIR="$2"; shift 2 ;;
        --project) PROJECT="$2"; shift 2 ;;
        --keep) KEEP=1; shift ;;
        -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
done

# A restore drill that can delete the stack it is meant to protect is worse than
# no drill. The project name is the only thing standing between `down -v` and
# the demo volumes, so refuse anything that does not look like a throwaway.
if [[ "${PROJECT}" != *restore* ]]; then
    echo "refusing to run: PROJECT=${PROJECT} does not look like a throwaway stack" >&2
    exit 2
fi
if [[ "${PROJECT}" == "ai-etl-platform" || "${PROJECT}" == "ai-etl-smoke" ]]; then
    echo "refusing to run against ${PROJECT}" >&2
    exit 2
fi

export COMPOSE_PROJECT_NAME="${PROJECT}"
export COMPOSE_FILE="docker-compose.yml:docker-compose.eval.yml:docker-compose.restore.yml"
export COMPOSE_BIND="${COMPOSE_BIND:-127.0.0.1}"
export RESTORE_API_HOST_PORT="${RESTORE_API_HOST_PORT:-8082}"
export RESTORE_QDRANT_HOST_PORT="${RESTORE_QDRANT_HOST_PORT:-6335}"
export RESTORE_ES_HOST_PORT="${RESTORE_ES_HOST_PORT:-9202}"
export RESTORE_MINIO_HOST_PORT="${RESTORE_MINIO_HOST_PORT:-9002}"
export RESTORE_PG_HOST_PORT="${RESTORE_PG_HOST_PORT:-55432}"

PG_USER="${PG_USER:-app}"
PG_DB="${PG_DB:-ai_etl}"
STORE_COLLECTION="${STORE_COLLECTION:-documents-v2}"
ES_INDEX="${ES_INDEX:-documents_text}"
S3_BUCKET="${S3_BUCKET:-documents}"

QDRANT_URL="http://127.0.0.1:${RESTORE_QDRANT_HOST_PORT}"
ES_URL="http://127.0.0.1:${RESTORE_ES_HOST_PORT}"
MINIO_URL="http://127.0.0.1:${RESTORE_MINIO_HOST_PORT}"
API_URL="http://127.0.0.1:${RESTORE_API_HOST_PORT}"

env_value() {
    local key="$1" fallback="${2-}" line=""
    if [[ -f .env ]]; then
        line="$(grep -E "^${key}=" .env | tail -n 1 || true)"
    fi
    if [[ -n "${line}" ]]; then printf '%s' "${line#*=}"; else printf '%s' "${fallback}"; fi
}
secret_file() {
    local configured
    configured="$(env_value "$1" "")"
    if [[ -n "${configured}" ]]; then printf '%s' "${configured}"; else printf '%s' "$2"; fi
}

S3_ACCESS_KEY_PATH="$(secret_file S3_ACCESS_KEY_FILE_PATH ./secrets/examples/s3_access_key)"
S3_SECRET_KEY_PATH="$(secret_file S3_SECRET_KEY_FILE_PATH ./secrets/examples/s3_secret_key)"
STORE_API_KEY_PATH="$(secret_file STORE_API_KEY_FILE_PATH ./secrets/examples/store_api_key)"

log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
die() { log "ERROR $*"; exit 2; }

cleanup() {
    if [[ "${KEEP}" == "1" ]]; then
        log "leaving ${PROJECT} running (--keep); remove with: COMPOSE_PROJECT_NAME=${PROJECT} COMPOSE_FILE='${COMPOSE_FILE}' docker compose down -v"
        return
    fi
    log "removing ${PROJECT} (scoped to its own volumes)"
    docker compose down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

wait_healthy() {
    local service="$1" container status waited=0
    container="$(docker compose ps -q "${service}")"
    [[ -n "${container}" ]] || die "${service} container did not start"
    while (( waited < 180 )); do
        status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "${container}")"
        case "${status}" in
            healthy|running) log "  ${service} ${status}"; return 0 ;;
            unhealthy|exited|dead) die "${service} is ${status}" ;;
        esac
        sleep 3
        waited=$((waited + 3))
    done
    die "${service} did not become healthy in 180s"
}

[[ -f "${BACKUP_DIR}/manifest.json" ]] || die "no manifest at ${BACKUP_DIR}/manifest.json"
BACKUP_DIR="$(cd "${BACKUP_DIR}" && pwd)"   # resolve the 'latest' symlink
log "restoring ${BACKUP_DIR} into project ${PROJECT}"

INTEGRITY="$("${PYTHON}" -c 'import json,sys; print(json.load(open(sys.argv[1]))["integrity"]["status"])' "${BACKUP_DIR}/manifest.json")"
log "source backup integrity=${INTEGRITY}"

# ---------------------------------------------------------------------------
# 1. PostgreSQL.
# ---------------------------------------------------------------------------
log "starting postgres"
docker compose up -d postgres
wait_healthy postgres
pg_container="$(docker compose ps -q postgres)"

log "restoring postgres"
docker exec -i "${pg_container}" pg_restore -U "${PG_USER}" -d "${PG_DB}" \
    --clean --if-exists --no-owner --no-privileges \
    < "${BACKUP_DIR}/postgres.dump"
log "  postgres restored"

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
    ) counts" > "${BACKUP_DIR}/restored-counts-before-boot.json"
log "  restored counts: $(cat "${BACKUP_DIR}/restored-counts-before-boot.json")"

# ---------------------------------------------------------------------------
# 2. Source objects: the volume is created empty, then untarred.
# ---------------------------------------------------------------------------
minio_volume="${PROJECT}_minio_data"
log "restoring minio volume ${minio_volume}"
# Labeled as a compose volume so `docker compose up` adopts it silently instead
# of warning that the volume was not created by Compose.
docker volume create \
    --label "com.docker.compose.project=${PROJECT}" \
    --label "com.docker.compose.volume=minio_data" \
    "${minio_volume}" >/dev/null
docker run --rm \
    -v "${minio_volume}:/data" \
    -v "${BACKUP_DIR}:/backup:ro" \
    alpine:3.20 tar -xf /backup/minio-data.tar -C /data
log "  objects restored"

# ---------------------------------------------------------------------------
# 3/4. Projections.
# ---------------------------------------------------------------------------
log "starting qdrant, elasticsearch, minio"
docker compose up -d qdrant elasticsearch minio
wait_healthy qdrant
wait_healthy elasticsearch
wait_healthy minio

store_api_key="$(cat "${STORE_API_KEY_PATH}")"
log "restoring qdrant collection ${STORE_COLLECTION}"
curl -fsS -X DELETE -H "api-key: ${store_api_key}" \
    "${QDRANT_URL}/collections/${STORE_COLLECTION}" >/dev/null 2>&1 || true
curl -fsS -X POST \
    -H "api-key: ${store_api_key}" \
    -F "snapshot=@${BACKUP_DIR}/qdrant-${STORE_COLLECTION}.snapshot" \
    "${QDRANT_URL}/collections/${STORE_COLLECTION}/snapshots/upload?priority=snapshot" >/dev/null
log "  qdrant restored"

log "restoring elasticsearch index ${ES_INDEX}"
"${PYTHON}" scripts/es-index-archive.py restore \
    --address "${ES_URL}" --index "${ES_INDEX}" \
    --in "${BACKUP_DIR}/es-${ES_INDEX}.jsonl"
log "  elasticsearch restored"

# ---------------------------------------------------------------------------
# 5. The application, which applies migrations and converges the demo rows.
# ---------------------------------------------------------------------------
log "starting query-api"
docker compose up -d query-api
wait_healthy query-api

# Boot must be idempotent: the convergence it runs on every start may not change
# what the restore produced, or the drill would be measuring the seed instead.
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
    ) counts" > "${BACKUP_DIR}/restored-counts.json"

# ---------------------------------------------------------------------------
# 6. Assertions.
# ---------------------------------------------------------------------------
log "running restore drill assertions"
set +e
drill_args=()
if [[ "${REQUIRE_INTACT:-0}" == "1" ]]; then
    drill_args+=(--require-intact)
fi
timeout "${DRILL_TIMEOUT}" "${PYTHON}" scripts/restore-drill.py \
    --manifest "${BACKUP_DIR}/manifest.json" \
    --source-inventory "${BACKUP_DIR}/object-inventory.json" \
    --catalog-keys "${BACKUP_DIR}/catalog-object-keys.txt" \
    --catalog-counts "${BACKUP_DIR}/restored-counts.json" \
    --minio-endpoint "${MINIO_URL}" \
    --s3-access-key-file "${S3_ACCESS_KEY_PATH}" \
    --s3-secret-key-file "${S3_SECRET_KEY_PATH}" \
    --qdrant-endpoint "${QDRANT_URL}" \
    --store-api-key-file "${STORE_API_KEY_PATH}" \
    --store-collection "${STORE_COLLECTION}" \
    --es-endpoint "${ES_URL}" \
    --es-index "${ES_INDEX}" \
    --api-base "${API_URL}" \
    ${drill_args[@]+"${drill_args[@]}"} \
    --out "${BACKUP_DIR}/restore-drill-report.json"
drill_code=$?
set -e

# The boot-convergence check is deliberately separate from the drill script: it
# compares the restored counts before and after the application started.
if "${PYTHON}" - "${BACKUP_DIR}/restored-counts-before-boot.json" "${BACKUP_DIR}/restored-counts.json" <<'PY'
import json, sys
before = json.load(open(sys.argv[1]))
after = json.load(open(sys.argv[2]))
drift = {k: (before.get(k), after.get(k)) for k in sorted(set(before) | set(after)) if before.get(k) != after.get(k)}
if drift:
    print(f"  FAIL  boot_convergence: rows changed on boot {drift}")
    sys.exit(1)
print("  PASS  boot_convergence: rows unchanged by application boot")
PY
then
    boot_code=0
else
    boot_code=1
fi

if [[ ${drill_code} -eq 0 && ${boot_code} -eq 0 ]]; then
    log "restore drill PASSED"
    exit 0
fi
log "restore drill FAILED (drill=${drill_code} boot=${boot_code})"
exit 1
