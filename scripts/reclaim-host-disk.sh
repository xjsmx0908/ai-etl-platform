#!/usr/bin/env bash
#
# Reclaim host disk without touching anything that is not this project's.
#
# The host is shared. `docker ps -a` carries other teams' containers (openclaw,
# deeptutor, umami, kafka-ui, a blog stack), so every global sweep --
# `docker system prune`, `docker image prune -a`, `docker volume prune` -- is
# banned here: on a shared host, "unused" only means "unused by anyone" by
# accident, and the blast radius is somebody else's data.
#
# What this script touches:
#   * build cache. The knob is `--min-free-space`, not `--max-used-space`:
#     measured 2026-09-26, the cache reported 55.21GB total of which 51.29GB was
#     `Shared` (layer blobs also referenced by images) and only 3.93GB was
#     private, so `--max-used-space 20GB` reclaimed 0B -- there was nothing
#     below the cap to evict -- while `--min-free-space 40GB` reclaimed 4.04GB.
#     A cap is still offered (`--build-cache-max-used`) but it is not the lever.
#   * locally built images belonging to this project's throwaway acceptance
#     stacks (`ai-etl-release-*`, `ai-etl-smoke-*`, `ai-etl-identity-demo-*`,
#     ...). Each one is re-checked against `docker ps -a --filter ancestor`
#     immediately before removal, so a stack that is still up cannot be cut out
#     from under itself.
#
# What it does not touch, by construction:
#   * volumes. 99 of 118 are unreferenced and 19.80GB of the 24.60GB total is
#     in them, but a read-only probe found a bot stack (dingtalk/napcat/qqbot/
#     wecom, 9 volumes of 1.23GB each) and a pnpm store -- other projects'.
#     "Unreferenced" is not "disposable".
#   * base / build images (`golang:*`, `node:*`, `python:*`, ...). Removing one
#     turns the next `docker compose build` into a network fetch.
#   * the live stack's own images (`ai-etl-platform-*`), including
#     `ai-etl-platform-alertmanager:latest`, which is not the ID the running
#     container uses.
#   * other projects' images, referenced or not.
#
# Dry-run is the default; `--apply` is required to change anything.
#
# Usage:
#   scripts/reclaim-host-disk.sh                    # print the plan
#   scripts/reclaim-host-disk.sh --apply            # execute
#   scripts/reclaim-host-disk.sh --no-images --apply
#   scripts/reclaim-host-disk.sh --build-cache-min-free 60GB --apply
#
# Environment:
#   APPLY                   1 is the same as --apply
#   BUILD_CACHE_MIN_FREE    default 40GB, passed to --min-free-space
#   BUILD_CACHE_MAX_USED    unset by default; when set, also passed to
#                           --max-used-space
#
# Machine-readable output (stable, one line per image):
#   CANDIDATE <ref> <size> <would-remove|keep-in-use|keep-protected>
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

DRY_RUN=1
if [[ "${APPLY:-0}" == "1" ]]; then
    DRY_RUN=0
fi
BUILD_CACHE_MIN_FREE="${BUILD_CACHE_MIN_FREE:-40GB}"
BUILD_CACHE_MAX_USED="${BUILD_CACHE_MAX_USED:-}"
DO_BUILD_CACHE=1
DO_IMAGES=1

# Locally built images for this project's throwaway stacks are the only
# images considered. The pattern is explicit rather than "everything
# unreferenced" so that a new project appearing on this host can never be
# swept up by this script.
OWNED_REPO_PATTERN='^ai-etl-'
PROTECTED_REPO_PATTERN='^ai-etl-platform-'

while [[ $# -gt 0 ]]; do
    case "$1" in
        --apply) DRY_RUN=0; shift ;;
        --build-cache-min-free) BUILD_CACHE_MIN_FREE="$2"; shift 2 ;;
        --build-cache-max-used) BUILD_CACHE_MAX_USED="$2"; shift 2 ;;
        --no-build-cache) DO_BUILD_CACHE=0; shift ;;
        --no-images) DO_IMAGES=0; shift ;;
        -h|--help) sed -n '2,60p' "$0"; exit 0 ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
done

log() { printf '%s\n' "$*"; }

print_host_state() {
    local label="$1"
    log ""
    log "== ${label} =="
    df -h / | sed -n '1,2p'
    docker system df 2>/dev/null | sed -n '1,6p' || true
}

# Human sizes from `docker images` summed into MB. Approximate on purpose: it
# is used to size the plan, never to decide whether something is removable.
sum_sizes_mb() {
    awk '
        {
            tok = $1
            if (match(tok, /^[0-9.]+/) == 0) next
            v = substr(tok, 1, RLENGTH) + 0
            unit = substr(tok, RLENGTH + 1)
            if (unit ~ /^GB/)      mb = v * 1024
            else if (unit ~ /^MB/) mb = v
            else if (unit ~ /^kB/) mb = v / 1024
            else                   mb = v / 1048576
            total += mb
        }
        END { printf "%.1f", total + 0 }
    '
}

# A container anywhere -- running or stopped -- that was created from this
# image or a descendant of it. `ancestor` matches on ancestry, so an image
# referenced only by its ID (as the running alertmanager is) still counts.
image_in_use() {
    local ref="$1"
    [[ -n "$(docker ps -a --filter "ancestor=${ref}" --format '{{.ID}}' 2>/dev/null)" ]]
}

candidate_images() {
    docker images --format '{{.Repository}}:{{.Tag}}\t{{.Size}}' 2>/dev/null \
        | grep -E "${OWNED_REPO_PATTERN}" \
        | grep -vE "${PROTECTED_REPO_PATTERN}" \
        | sort || true
}

plan_images() {
    local removable=0 total=0
    local all_sizes="" removable_sizes=""

    while IFS=$'\t' read -r ref size; do
        [[ -z "${ref}" ]] && continue
        total=$((total + 1))
        all_sizes="${all_sizes}${size}"$'\n'
        if image_in_use "${ref}"; then
            log "CANDIDATE	${ref}	${size}	keep-in-use"
            log "  keep    ${ref} (${size}) -- a container still descends from it"
            continue
        fi
        removable=$((removable + 1))
        removable_sizes="${removable_sizes}${size}"$'\n'
        log "CANDIDATE	${ref}	${size}	would-remove"
        if [[ "${DRY_RUN}" == "1" ]]; then
            log "  remove  ${ref} (${size})"
        elif docker image rm "${ref}" >/dev/null 2>&1; then
            log "  removed ${ref} (${size})"
        else
            log "  FAILED  ${ref} (${size}) -- left in place"
        fi
    done < <(candidate_images)

    log ""
    log "images: ${removable}/${total} removable, ~$(printf '%s' "${removable_sizes}" | sum_sizes_mb) MB of ~$(printf '%s' "${all_sizes}" | sum_sizes_mb) MB listed"
}

prune_build_cache() {
    local args=(-f --min-free-space "${BUILD_CACHE_MIN_FREE}")
    if [[ -n "${BUILD_CACHE_MAX_USED}" ]]; then
        args+=(--max-used-space "${BUILD_CACHE_MAX_USED}")
    fi

    log ""
    log "build cache: prune until ${BUILD_CACHE_MIN_FREE} is free${BUILD_CACHE_MAX_USED:+ (and keep at most ${BUILD_CACHE_MAX_USED})}"
    if docker buildx du >/dev/null 2>&1; then
        docker buildx du 2>/dev/null | grep -E '^(Total|Reclaimable|Private|Shared):' | sed 's/^/  before  /' || true
    fi
    if [[ "${DRY_RUN}" == "1" ]]; then
        log "  would run: docker builder prune ${args[*]}"
        log "  (only cache buildkit is not also holding for an image; a cap alone reclaims nothing when private cache is under it)"
        return
    fi
    docker builder prune "${args[@]}" 2>&1 | sed 's/^/  /' || true
    if docker buildx du >/dev/null 2>&1; then
        docker buildx du 2>/dev/null | grep -E '^(Total|Reclaimable|Private|Shared):' | sed 's/^/  after   /' || true
    fi
}

if [[ "${DRY_RUN}" == "1" ]]; then
    log "DRY RUN -- nothing will be removed. Re-run with --apply to execute."
else
    log "APPLY -- this run changes the host."
fi

print_host_state "before"

[[ "${DO_BUILD_CACHE}" == "1" ]] && prune_build_cache

if [[ "${DO_IMAGES}" == "1" ]]; then
    log ""
    log "images owned by this project's throwaway stacks (pattern ${OWNED_REPO_PATTERN}, excluding ${PROTECTED_REPO_PATTERN}):"
    plan_images
fi

log ""
log "not touched by design (reported so the next decision is not blind):"
log "  volumes:  $(docker system df 2>/dev/null | awk '$1=="Local"&&$2=="Volumes"{print "active "$4", size "$5", reclaimable "$6}' || true)"
log "            unreferenced volumes on this host were probed read-only on 2026-09-26:"
log "            a bot stack (dingtalk/napcat/qqbot/wecom) and a pnpm store -- not this project's"
log "  base images (golang/node/python/...): left in place so the next build stays offline"
log "  other projects' images: left in place"

print_host_state "after"
