#!/usr/bin/env python3
"""Restore the Elasticsearch rows for documents the store still holds.

Why this exists
---------------
`internal/pipeline/pipeline.go:983` treats a full-text indexing failure as
non-fatal by design ("Qdrant success is primary; ES errors never fail main
pipeline"). `internal/es/sink.go:104` then retries the write, and when the
retries run out the message moves to the `es:index:deadletter` Redis list.
`internal/es/queue.go:205` only ever *writes* to that list: nothing reads it,
nothing alerts on it, and the manifest reconciler cannot see it either, because
that loop only covers documents which have a manifest row.

On 2026-09-02 the Elasticsearch disk crossed the flood-stage watermark, the
index was flipped to read-only-allow-delete, and every write answered
`429 cluster_block_exception`. Two published documents were dropped that way
and stayed invisible to keyword search. Their chunks never left Qdrant, so
nothing needs re-parsing or re-embedding: the missing rows are rebuilt from
what the store already holds.

What this does
--------------
Reads the stored points for a document and PUTs the Elasticsearch rows the
writer would have produced for those exact points. It never deletes a document,
never re-parses a file, and never changes a doc_id or a chunk_id -- so citations
and releases that name these chunks keep working.

The `_id` is derived the way the writers derive it (`internal/es/indexer.go:166`
for the legacy path, `:204` for the generation path): a point carrying a
generation id gets `<generation>__<chunk_id>`, one that does not gets
`<chunk_id>`. Inventing a generation id for a point that has none would put a
row into Elasticsearch claiming an identity the store never had -- a fresh
inconsistency in the name of repairing an old one.

What it deliberately does not do
--------------------------------
It does not repair documents whose Elasticsearch count is merely *lower* than
the store's. That is `count_mismatch`, a different finding with a different
cause, and silently topping it up would hide whatever produced it. Only
documents with zero rows are in scope, which is also what makes a re-run
idempotent: after a successful backfill the count is non-zero, so the document
is no longer selected.

Usage
-----
  # 1. export the registry columns the ES rows need, from inside the network:
  #      SELECT doc_id, tenant_id, file_name, file_hash, permission,
  #             to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
  #      FROM documents WHERE ...
  #    (tab-separated, no header)
  # 2. dry run -- prints what would be written, writes nothing
  python3 scripts/backfill-elasticsearch-from-store.py \
      --documents-tsv /tmp/documents.tsv --doc-id doc-123
  # 3. write
  python3 scripts/backfill-elasticsearch-from-store.py \
      --documents-tsv /tmp/documents.tsv --doc-id doc-123 --apply

Exit status: 0 when every requested document is reachable afterwards, 1 when
any of them is still missing.
"""
from __future__ import annotations

import argparse
import json
import sys
import urllib.error
import urllib.parse
import urllib.request

DEFAULT_ES = "http://127.0.0.1:9200"
DEFAULT_ES_INDEX = "documents_text_v2"
DEFAULT_QDRANT = "http://127.0.0.1:6333"
DEFAULT_COLLECTION = "documents-v2"

# The deployment host may carry http_proxy/https_proxy for other work; these
# targets are loopback services and must never go through a proxy.
_OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))

MAX_SCROLL_PAGES = 10000


def http_json(url, *, method="GET", body=None, headers=None, timeout=60):
    data = json.dumps(body).encode("utf-8") if body is not None else None
    request = urllib.request.Request(
        url,
        data=data,
        headers={"Content-Type": "application/json", **(headers or {})},
        method=method,
    )
    with _OPENER.open(request, timeout=timeout) as response:
        return json.load(response)


def scroll_pages(fetch):
    """Every point, following next_page_offset to the end.

    Same loop as the consistency check, for the same reason: a single scroll
    call returns at most `limit` points, and a truncated read here would
    backfill *part* of a document and leave it in a state that looks repaired.
    """
    points, offset, pages = [], None, 0
    while True:
        page, offset = fetch(offset)
        points.extend(page)
        pages += 1
        if offset is None:
            return points
        if pages >= MAX_SCROLL_PAGES:
            raise RuntimeError(f"scroll did not terminate after {pages} pages")


def qdrant_points(qdrant, collection, doc_id, api_key):
    """The stored points for one document, flattened the way the check flattens them.

    Unlike the consistency check this keeps the whole payload, because the point
    *is* the source of the row being rebuilt. The identity fields are lifted to
    the top level so that `es_document_id` reads the same keys here as the
    check's `duplicate_chunk_points` reads there: one definition of what a
    point's identity is, in both places.
    """
    headers = {"api-key": api_key} if api_key else {}

    def fetch(offset):
        body = {
            "filter": {"must": [{"key": "doc_id", "match": {"value": doc_id}}]},
            "limit": 1000,
            "with_payload": True,
            "with_vector": False,
        }
        if offset is not None:
            body["offset"] = offset
        payload = http_json(
            f"{qdrant}/collections/{collection}/points/scroll",
            method="POST",
            body=body,
            headers=headers,
        )
        result = payload.get("result", {})
        page = []
        for point in result.get("points", []):
            data = point.get("payload") or {}
            page.append(
                {
                    "id": point.get("id"),
                    "chunk_id": data.get("chunk_id") or str(point.get("id")),
                    "generation_id": data.get("generation_id") or "",
                    "payload": data,
                }
            )
        return page, result.get("next_page_offset")

    return scroll_pages(fetch)


def es_count(es, index, doc_id):
    payload = http_json(
        f"{es}/{index}/_search",
        method="POST",
        body={"size": 0, "query": {"term": {"doc_id": doc_id}}},
    )
    return payload["hits"]["total"]["value"]


def normalize_permission(raw):
    """Mirror of normalizePermission in internal/es/indexer.go:597.

    Retrieval filters on this value, so a row written with a value the writer
    would not have produced can be reachable in one query and invisible in the
    next.
    """
    value = (raw or "").strip().lower()
    if value in ("public", "confidential"):
        return value
    return "internal"


def es_document_id(point):
    """The `_id` the writer would have used for this point.

    internal/es/indexer.go derives it from the point's own identity: the
    generation path prefixes `<generation>__`, the legacy path uses the bare
    chunk id. Which one applies is a property of the point, not a choice.
    """
    chunk_id = point["chunk_id"]
    generation_id = (point.get("generation_id") or "").strip()
    if generation_id:
        return f"{generation_id}__{chunk_id}"
    return chunk_id


def build_es_document(point, document):
    """One stored point plus its registry row -> the Elasticsearch _source.

    Keys the point does not carry are omitted rather than defaulted: a legacy
    point has no content_hash and no document_version_id, and writing empty
    strings for them would make the row claim fields the store never had.
    """
    payload = point.get("payload") or {}
    doc = {
        "chunk_id": payload.get("chunk_id") or str(point.get("id", "")),
        "doc_id": payload.get("doc_id") or document["doc_id"],
        "tenant_id": payload.get("tenant_id") or document.get("tenant_id", ""),
        "content": payload.get("content", ""),
        "permission": normalize_permission(payload.get("permission")),
        "chunk_index": payload.get("index", 0),
        "file_name": document.get("file_name", ""),
        "file_hash": document.get("file_hash", ""),
        "created_at": document.get("created_at", ""),
    }
    metadata = payload.get("metadata")
    if metadata:
        doc["metadata"] = metadata
    for key in ("document_version_id", "generation_id", "content_hash"):
        value = payload.get(key)
        if value:
            doc[key] = value
    return doc


def parse_documents_tsv(text):
    """doc_id / tenant_id / file_name / file_hash / permission / created_at.

    Tab separated, no header. Blank lines are ignored so a psql export that
    ends in a newline needs no post-processing.
    """
    documents = {}
    for line in text.splitlines():
        if not line.strip():
            continue
        fields = line.split("\t")
        if len(fields) < 6:
            raise ValueError(f"expected 6 tab-separated columns, got {len(fields)}: {line[:120]!r}")
        doc_id, tenant_id, file_name, file_hash, permission, created_at = fields[:6]
        documents[doc_id] = {
            "doc_id": doc_id,
            "tenant_id": tenant_id,
            "file_name": file_name,
            "file_hash": file_hash,
            "permission": permission,
            "created_at": created_at,
        }
    return documents


def plan_document(doc_id, points, document, indexed_count):
    """The rows to write for one document, or the reason to skip it.

    Returns `(rows, skip_reason)`. Exactly one of them is set.
    """
    if not points:
        return [], "the store holds no points for this document"
    if indexed_count > 0:
        return [], (
            f"Elasticsearch already holds {indexed_count} row(s); this script only "
            f"fills a document that is entirely absent"
        )
    if document is None:
        return [], "no registry row was supplied for this document"

    rows = []
    seen = {}
    for point in points:
        es_id = es_document_id(point)
        row = build_es_document(point, document)
        if es_id in seen:
            # Two stored points mapping to one _id means one would overwrite the
            # other. That is duplicate_chunk_points inside a generation, and it
            # is the consistency check's finding to report -- not something to
            # paper over here by writing whichever point came last.
            seen[es_id] += 1
            continue
        seen[es_id] = 1
        rows.append((es_id, row))
    return rows, None


def put_row(es, index, es_id, row):
    url = f"{es}/{index}/_doc/{urllib.parse.quote(es_id, safe='')}?refresh=wait_for"
    return http_json(url, method="PUT", body=row)


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--documents-tsv", required=True, help="path to the registry export (see Usage)")
    parser.add_argument("--doc-id", action="append", default=[], help="document to backfill; repeatable")
    parser.add_argument("--all-missing", action="store_true", help="backfill every document in the TSV that Elasticsearch cannot reach")
    parser.add_argument("--es", default=DEFAULT_ES)
    parser.add_argument("--es-index", default=DEFAULT_ES_INDEX)
    parser.add_argument("--qdrant", default=DEFAULT_QDRANT)
    parser.add_argument("--collection", default=DEFAULT_COLLECTION)
    parser.add_argument("--qdrant-api-key", default="")
    parser.add_argument("--apply", action="store_true", help="write the rows; without it nothing is written")
    parser.add_argument("--dump", default="", help="write the plan as JSON to this path")
    args = parser.parse_args()

    with open(args.documents_tsv, encoding="utf-8") as handle:
        documents = parse_documents_tsv(handle.read())

    doc_ids = list(args.doc_id)
    if args.all_missing:
        doc_ids = sorted(documents)
    if not doc_ids:
        print("no documents selected: pass --doc-id or --all-missing", file=sys.stderr)
        return 2

    plan = []
    still_missing = []
    for doc_id in doc_ids:
        document = documents.get(doc_id)
        try:
            points = qdrant_points(args.qdrant, args.collection, doc_id, args.qdrant_api_key)
        except urllib.error.HTTPError as exc:
            print(f"[backfill] {doc_id}: store read failed: {exc}", file=sys.stderr)
            still_missing.append(doc_id)
            continue
        indexed_count = es_count(args.es, args.es_index, doc_id)
        rows, skip = plan_document(doc_id, points, document, indexed_count)

        if skip:
            print(f"[backfill] {doc_id}: skipped -- {skip}")
            # Already present is not missing. Absent for any other reason is.
            if indexed_count == 0:
                still_missing.append(doc_id)
            plan.append({"doc_id": doc_id, "stored_points": len(points), "indexed": indexed_count, "written": 0, "skipped": skip})
            continue

        print(f"[backfill] {doc_id}: {len(points)} stored point(s) -> {len(rows)} row(s) to write")
        if args.dump:
            for es_id, row in rows:
                print(f"           {es_id}  ({len(row.get('content', ''))} chars, permission={row['permission']})")

        if not args.apply:
            plan.append({"doc_id": doc_id, "stored_points": len(points), "indexed": indexed_count, "written": 0, "planned": len(rows)})
            continue

        written = 0
        for es_id, row in rows:
            try:
                put_row(args.es, args.es_index, es_id, row)
                written += 1
            except urllib.error.HTTPError as exc:
                print(f"[backfill] {doc_id}: PUT {es_id} failed: {exc}", file=sys.stderr)

        after = es_count(args.es, args.es_index, doc_id)
        print(f"[backfill] {doc_id}: wrote {written}/{len(rows)}, Elasticsearch now holds {after}")
        if after == 0:
            still_missing.append(doc_id)
        plan.append({"doc_id": doc_id, "stored_points": len(points), "indexed": indexed_count, "written": written, "after": after})

    if args.dump:
        with open(args.dump, "w", encoding="utf-8") as handle:
            json.dump(
                {
                    "es_index": args.es_index,
                    "applied": bool(args.apply),
                    "documents": plan,
                    "rollback": [
                        {"doc_id": entry["doc_id"], "command": f"POST /{args.es_index}/_delete_by_query", "body": {"query": {"term": {"doc_id": entry["doc_id"]}}}}
                        for entry in plan
                        if entry.get("written")
                    ],
                },
                handle,
                indent=2,
                ensure_ascii=False,
            )
        print(f"[backfill] plan written to {args.dump}")

    if not args.apply:
        print("\n[backfill] dry run: nothing was written (pass --apply to write)")
        return 0

    if still_missing:
        print(f"\n[backfill] still missing from Elasticsearch: {', '.join(still_missing)}", file=sys.stderr)
        return 1
    print("\n[backfill] every requested document is reachable from Elasticsearch")
    return 0


if __name__ == "__main__":
    sys.exit(main())
