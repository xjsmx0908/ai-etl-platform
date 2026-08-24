#!/usr/bin/env bash
set -euo pipefail

es_address="${ES_ADDRESS:-http://localhost:9200}"
es_alias="${ES_INDEX:-documents_text}"
target_index="${ES_TARGET_INDEX:-${es_alias}_v2}"

if [[ ! "$es_alias" =~ ^[a-z0-9._-]+$ || ! "$target_index" =~ ^[a-z0-9._-]+$ ]]; then
  echo "ES_INDEX and ES_TARGET_INDEX must contain only lowercase letters, digits, dot, underscore, or dash" >&2
  exit 2
fi

curl_args=(-fsS -H "Content-Type: application/json")
if [[ -n "${ES_API_KEY:-}" ]]; then
  curl_args+=(-H "Authorization: ApiKey ${ES_API_KEY}")
fi

status_code() {
  curl -sS -o /dev/null -w '%{http_code}' "${curl_args[@]}" "$1"
}

if [[ "$(status_code "$es_address/_alias/$es_alias")" == "200" ]]; then
  echo "Elasticsearch alias $es_alias already exists; no migration needed."
  exit 0
fi

if [[ "$(status_code "$es_address/$es_alias")" != "200" ]]; then
  echo "Source index $es_alias does not exist; the service will create a fresh CJK index."
  exit 0
fi

if [[ "$(status_code "$es_address/$target_index")" == "200" ]]; then
  echo "Target index $target_index already exists; refusing to overwrite it." >&2
  exit 1
fi

curl "${curl_args[@]}" -X PUT "$es_address/$target_index" -d @- <<'JSON'
{
  "mappings": {
    "properties": {
      "chunk_id": {"type": "keyword"},
      "doc_id": {"type": "keyword"},
      "tenant_id": {"type": "keyword"},
      "content": {"type": "text", "analyzer": "cjk", "search_analyzer": "cjk"},
      "permission": {"type": "keyword"},
      "chunk_index": {"type": "integer"},
      "file_hash": {"type": "keyword"},
      "created_at": {"type": "date"},
      "metadata": {"type": "flattened"}
    }
  }
}
JSON

curl "${curl_args[@]}" -X POST "$es_address/_reindex?wait_for_completion=true&refresh=true" -d @- <<JSON
{"source":{"index":"$es_alias"},"dest":{"index":"$target_index"}}
JSON

source_count="$(curl "${curl_args[@]}" "$es_address/$es_alias/_count" | jq -er '.count')"
target_count="$(curl "${curl_args[@]}" "$es_address/$target_index/_count" | jq -er '.count')"
if (( target_count < source_count )); then
  echo "Reindex count mismatch: source=$source_count target=$target_count; keeping the old index." >&2
  exit 1
fi

# remove_index and add are committed as one cluster-state update, so clients do
# not observe a half-switched alias.
curl "${curl_args[@]}" -X POST "$es_address/_aliases" -d @- <<JSON
{
  "actions": [
    {"remove_index": {"index": "$es_alias"}},
    {"add": {"index": "$target_index", "alias": "$es_alias", "is_write_index": true}}
  ]
}
JSON

echo "Migrated $source_count documents to $target_index and switched alias $es_alias."
