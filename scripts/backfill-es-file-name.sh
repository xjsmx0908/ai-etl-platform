#!/usr/bin/env bash
# Backfill file_name into the Elasticsearch chunk index from the document registry.
#
# Why this exists: the lexical retrieval branch boosts match_phrase/match on
# file_name above every other signal (boost 6.0 / 3.0). The field was never
# written to the index, so those clauses matched nothing and every title-ish
# query silently degraded to a content-only match. Ingestion now carries the
# upload filename, but chunks indexed before that have no value — and a
# re-upload is not an option for the documents whose source objects are gone.
#
# Idempotent: adding the mapping twice is a no-op and re-running the update
# writes the same value.
#
# Usage:
#   docker exec -i ai-etl-platform-postgres-1 psql -U app -d ai_etl -tA -F$'\t' \
#     -c "SELECT tenant_id, doc_id, file_name FROM documents WHERE file_name <> ''" \
#     > /tmp/docs.tsv
#   DOCS_TSV=/tmp/docs.tsv bash scripts/backfill-es-file-name.sh
#
# Env:
#   DOCS_TSV         required; "tenant_id<TAB>doc_id<TAB>file_name" lines
#   ES_ADDRESS       default http://localhost:9200
#   ES_INDEX         alias, default documents_text
#   ES_TARGET_INDEX  physical index, default ${ES_INDEX}_v2
#   ES_API_KEY       optional
#   DRY_RUN          1 to report without writing
set -euo pipefail

es_address="${ES_ADDRESS:-http://localhost:9200}"
es_alias="${ES_INDEX:-documents_text}"
es_target="${ES_TARGET_INDEX:-${es_alias}_v2}"
docs_tsv="${DOCS_TSV:-}"
dry_run="${DRY_RUN:-0}"

if [[ -z "$docs_tsv" || ! -r "$docs_tsv" ]]; then
  echo "DOCS_TSV must point to a readable tenant/doc_id/file_name TSV" >&2
  exit 2
fi

curl_args=(-fsS -H "Content-Type: application/json")
if [[ -n "${ES_API_KEY:-}" ]]; then
  curl_args+=(-H "Authorization: ApiKey ${ES_API_KEY}")
fi

if [[ "$dry_run" != "1" ]]; then
  # The index may predate the field; add it before writing any value into it.
  # Same analyzer as content, otherwise a CJK filename tokenizes differently on
  # the query side than on the index side.
  curl "${curl_args[@]}" -X PUT "$es_address/$es_target/_mapping" -d @- >/dev/null <<'JSON'
{"properties":{"file_name":{"type":"text","analyzer":"cjk","search_analyzer":"cjk"}}}
JSON
fi

total=0
updated=0
while IFS=$'\t' read -r tenant doc_id file_name; do
  [[ -z "${doc_id:-}" || -z "${file_name:-}" ]] && continue
  total=$((total + 1))
  if [[ "$dry_run" == "1" ]]; then
    printf '[dry-run] %s/%s -> %s\n' "$tenant" "$doc_id" "$file_name"
    continue
  fi
  body="$(jq -nc --arg t "$tenant" --arg d "$doc_id" --arg n "$file_name" \
    '{query:{bool:{filter:[{term:{tenant_id:$t}},{term:{doc_id:$d}}]}},
      script:{lang:"painless",source:"ctx._source.file_name = params.name",params:{name:$n}}}')"
  resp="$(curl "${curl_args[@]}" -X POST \
    "$es_address/$es_target/_update_by_query?conflicts=proceed" -d "$body")"
  updated=$((updated + $(jq -er '.updated // 0' <<<"$resp")))
done < "$docs_tsv"

if [[ "$dry_run" == "1" ]]; then
  echo "[dry-run] $total documents would be backfilled"
  exit 0
fi

curl "${curl_args[@]}" -X POST "$es_address/$es_target/_refresh" >/dev/null

with_name="$(curl "${curl_args[@]}" -X POST "$es_address/$es_target/_count" \
  -d '{"query":{"exists":{"field":"file_name"}}}' | jq -er '.count')"
total_chunks="$(curl "${curl_args[@]}" "$es_address/$es_target/_count" | jq -er '.count')"

echo "backfill done: documents=$total chunks_updated=$updated chunks_with_file_name=$with_name/$total_chunks"
if [[ "$with_name" -ne "$total_chunks" ]]; then
  echo "WARNING: $((total_chunks - with_name)) chunks still lack file_name (doc_id absent from the registry)" >&2
fi
