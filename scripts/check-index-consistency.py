#!/usr/bin/env python3
"""Cross-layer index consistency check.

Four layers describe the same document, and nothing in this repository compares
them:

    Postgres        documents / index_manifests     what the product believes exists
    Qdrant          documents-v2                    what vector search can return
    Elasticsearch   documents_text_v2               what keyword search can return
    Query API       GET /v1/documents/{id}/chunks   what the UI can show

A published document can therefore be visible in one layer and invisible in
another with no test, alert or reconcile job noticing. The existing manifest
reconcile only covers documents that *have* a manifest row, so anything that
never got one is outside its reach by construction.

Findings reported
-----------------
keyword_unsearchable    Elasticsearch holds no chunks for a document Qdrant does
                        -> keyword/full-text recall silently loses that document
citation_unverifiable   the chunks endpoint hides chunks Qdrant still serves
                        -> a citation can name a chunk the document page never shows
count_mismatch          Elasticsearch and Qdrant disagree on the chunk count
registry_count_stale    the registry's own chunk counters disagree with the store
                        -> the document list and the detail page render those
                           numbers, so the page reads "-" for a document that
                           does have chunks
space_key_unset         chunks with no stored copy carrying the document's knowledge
                        space
                        -> the vector branch cannot return them inside that space,
                           so part of the document is keyword-only. A chunk whose
                           *current* copy carries the key is not reported, even when a
                           superseded copy has no metadata at all.
permission_drift        stored points whose permission differs from the registry
                        -> retrieval filters on the stale value (leak or black-hole)
duplicate_chunk_points  more than one stored point carries the same chunk_id *within
                        the same generation*
                        -> a write that should have replaced appended instead; counts
                           and digests can still look right while the store is not.
                           Several points for one chunk id across *different*
                           generations is the design, not a finding.

Exit status: 0 when every layer agrees, 1 when any finding is reported.
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request

DEFAULT_API = "http://127.0.0.1:8080"
DEFAULT_ES = "http://127.0.0.1:9200"
DEFAULT_QDRANT = "http://127.0.0.1:6333"
DEFAULT_ES_INDEX = "documents_text_v2"
DEFAULT_COLLECTION = "documents-v2"
DEFAULT_REPORT_DIR = "artifacts/index-consistency"

# The deployment host may carry http_proxy/https_proxy for other work; these
# targets are loopback services and must never go through a proxy.
_OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


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


def list_documents(api, token, limit):
    documents = []
    offset = 0
    page_size = 100
    while len(documents) < limit:
        payload = http_json(
            f"{api}/v1/documents?limit={page_size}&offset={offset}",
            headers={"Authorization": f"Bearer {token}"},
        )
        items = payload.get("items") or []
        if not items:
            break
        documents.extend(items)
        offset += len(items)
        if len(items) < page_size:
            break
    return documents[:limit]


def es_count(es, index, doc_id):
    payload = http_json(
        f"{es}/{index}/_search",
        method="POST",
        body={"size": 0, "query": {"term": {"doc_id": doc_id}}},
    )
    return payload["hits"]["total"]["value"]


MAX_SCROLL_PAGES = 10000


def scroll_pages(fetch):
    """Every point, following next_page_offset to the end.

    Qdrant's scroll returns at most `limit` points per call plus a next_page_offset.
    Ignoring that offset silently truncates the document -- and since every finding
    here compares the stored ids against another layer, a truncated read makes the
    stored side *smaller* and the check *quieter*. That is the worst direction for a
    check to be wrong: it hides problems instead of inventing them. One document in
    the live tenant has 2170 stored points, so this is not hypothetical.

    `fetch(offset)` returns `(points, next_page_offset)`. The page cap only exists to
    turn a server that never terminates into an error rather than a hang.
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
    """One entry per stored point: the chunk id plus the fields retrieval filters on.

    The payload is restricted to those fields on purpose. Requesting the whole
    payload pulls every chunk body back for no reason, and the filter fields are
    the only ones this check has an opinion about.
    """
    headers = {"api-key": api_key} if api_key else {}

    def fetch(offset):
        body = {
            "filter": {"must": [{"key": "doc_id", "match": {"value": doc_id}}]},
            "limit": 1000,
            "with_payload": [
                "chunk_id",
                "permission",
                "metadata",
                "document_version_id",
                "generation_id",
            ],
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
            data = point.get("payload", {})
            metadata = data.get("metadata") or {}
            page.append(
                {
                    "chunk_id": data.get("chunk_id") or str(point.get("id")),
                    "permission": data.get("permission", ""),
                    "knowledge_base_id": metadata.get("knowledge_base_id", ""),
                    "document_version_id": data.get("document_version_id") or "",
                    "generation_id": data.get("generation_id") or "",
                }
            )
        return page, result.get("next_page_offset")

    return scroll_pages(fetch)


def qdrant_chunk_ids(points):
    """The stored chunk ids in storage order. Duplicates are preserved on purpose:
    a second point for the same chunk is itself a finding, and dropping it here
    would erase the evidence before classify_payload_fields can see it."""
    return [point["chunk_id"] for point in points]


def endpoint_chunk_ids(api, token, doc_id):
    payload = http_json(
        f"{api}/v1/documents/{doc_id}/chunks",
        headers={"Authorization": f"Bearer {token}"},
    )
    return [item.get("chunk_id") for item in payload.get("items") or []]


def classify_document(doc_id, document, stored_ids, indexed_count, visible_ids):
    """Return the findings for one document.

    Kept as a pure function so the decision rules can be unit tested without a
    running stack.
    """
    findings = []
    file_name = document.get("file_name", "")
    publication_status = document.get("publication_status", "")

    if stored_ids and indexed_count == 0:
        findings.append(
            {
                "kind": "keyword_unsearchable",
                "doc_id": doc_id,
                "file_name": file_name,
                "publication_status": publication_status,
                "qdrant_chunks": len(stored_ids),
                "es_chunks": indexed_count,
                "detail": "Elasticsearch holds no chunks: keyword search cannot reach this document",
            }
        )
    elif stored_ids and indexed_count != len(stored_ids):
        findings.append(
            {
                "kind": "count_mismatch",
                "doc_id": doc_id,
                "file_name": file_name,
                "qdrant_chunks": len(stored_ids),
                "es_chunks": indexed_count,
                "detail": "Elasticsearch and Qdrant disagree on the chunk count",
            }
        )

    hidden = sorted(set(stored_ids) - set(visible_ids))
    if hidden:
        findings.append(
            {
                "kind": "citation_unverifiable",
                "doc_id": doc_id,
                "file_name": file_name,
                "publication_status": publication_status,
                "qdrant_chunks": len(stored_ids),
                "endpoint_chunks": len(set(visible_ids)),
                "hidden_chunk_ids": hidden,
                "detail": (
                    "the chunks endpoint hides chunks that vector search still serves: "
                    "a citation naming one of them cannot be checked on the document page"
                ),
            }
        )

    # The registry's own counters are a fifth statement about the same fact: how
    # many chunks this document has. Both document pages render them, so a stale
    # counter is visible to the reader while every other layer looks right. Only
    # compared when the registry answered with both fields -- an absent field is
    # not a disagreement, and inventing one would make this rule fire on every
    # caller that does not pass the registry row.
    #
    # The baseline is the *distinct stored chunk ids*, not the published
    # generation's count: this layer has no way to tell which generation the
    # counters were last written for (they are written per ingestion, and a
    # re-ingested document keeps every generation in the store). That makes the
    # rule's claim conditional, so the detail has to be too -- a zero total means
    # the page renders a dash, a non-zero one means the page renders a count that
    # disagrees. Calling both "reads a dash" would be a wrong label on half the
    # population.
    if "chunks_done" in document and "chunks_total" in document:
        expected = len(set(stored_ids))
        if document["chunks_done"] != expected or document["chunks_total"] != expected:
            if not document["chunks_total"]:
                detail = (
                    "the registry never recorded a chunk total for this document: "
                    "both document pages render a dash where a document that does "
                    "have chunks should show a count"
                )
            else:
                detail = (
                    "the registry's chunk counters disagree with the store, and both "
                    "document pages render these numbers"
                )
            findings.append(
                {
                    "kind": "registry_count_stale",
                    "doc_id": doc_id,
                    "file_name": file_name,
                    "publication_status": publication_status,
                    "stored_chunks": expected,
                    "stored_points": len(stored_ids),
                    "registry_chunks_done": document["chunks_done"],
                    "registry_chunks_total": document["chunks_total"],
                    "detail": detail,
                }
            )

    return findings


def classify_payload_fields(doc_id, document, points):
    """Return the findings about the payload fields retrieval filters on.

    Qdrant retrieval adds `metadata.knowledge_base_id` and `permission` clauses
    whenever the request names a space or an access scope (internal/retrieval/
    qdrant.go), and Elasticsearch adds the same clauses (elastic.go). Those two
    values are written at ingestion into the index while the authoritative copy
    lives in Postgres, so they can drift the same way the chunk set can -- and
    the drift is invisible end to end, because the keyword branch still reaches
    the document. Defect 9 was this shape (ES file_name was never written), and
    scripts/backfill-es-file-name.sh / scripts/backfill-qdrant-permission.sh
    exist because it happened before.

    Kept pure so the rules can be unit tested without a running stack.
    """
    findings = []
    if not points:
        return findings

    file_name = document.get("file_name", "")
    publication_status = document.get("publication_status", "")
    space = (document.get("knowledge_space_id") or "").strip()
    permission = (document.get("permission") or "").strip()

    if space:
        # A chunk id is unreachable inside the space only when *no* stored copy of it
        # carries the document's space key. Grouping by chunk id is the whole point:
        # the store keeps every generation, and the superseded copies from the
        # 2026-08-16 seed carry no metadata at all while the current generation
        # carries the right key. A per-point rule therefore flags chunks that the
        # space-filtered branch does reach -- which is a wrong label, not a finding.
        # Live check: `HR-2024-001_0000` has two points, the current one
        # (`metadata.knowledge_base_id = enterprise-demo`) and a metadata-less legacy
        # one; the chunk is reachable, and the per-point rule flagged it.
        carries = {}
        for point in points:
            chunk_id = point["chunk_id"]
            ok = (point.get("knowledge_base_id") or "").strip() == space
            carries[chunk_id] = carries.get(chunk_id, False) or ok
        unset = sorted(chunk_id for chunk_id, ok in carries.items() if not ok)
        if unset:
            findings.append(
                {
                    "kind": "space_key_unset",
                    "doc_id": doc_id,
                    "file_name": file_name,
                    "publication_status": publication_status,
                    "knowledge_space_id": space,
                    "points_total": len(points),
                    "points_unset": len(unset),
                    "unset_chunk_ids": unset,
                    "detail": (
                        "no stored copy of these chunks carries the document's "
                        "knowledge space: the vector branch cannot return them inside "
                        "that space, so those chunks are reachable by keyword search "
                        "only"
                    ),
                }
            )

    if permission:
        drifted = sorted({point["chunk_id"] for point in points
                          if (point.get("permission") or "").strip() != permission})
        if drifted:
            findings.append(
                {
                    "kind": "permission_drift",
                    "doc_id": doc_id,
                    "file_name": file_name,
                    "publication_status": publication_status,
                    "registry_permission": permission,
                    "drifted_chunk_ids": drifted,
                    "detail": (
                        "stored points disagree with the registry on permission: "
                        "retrieval filters on the stored value, so the document can "
                        "leak to a role that may not read it or vanish for one that may"
                    ),
                }
            )

    # The alarm is NOT "one chunk id, several points". A point id is derived from the
    # identity it belongs to -- `chunkIDToUint(generation_id + "\x00" + chunk_id)` in
    # UpsertGeneration -- and Qdrant overwrites any point whose id already exists. So
    # re-writing the same generation replaces, and two points can only share a chunk
    # id when they belong to *different* generations. That is the design: the store
    # keeps every generation so a re-ingest does not lose the published one before the
    # new one is promoted. Live check: 116 chunk ids across 114 documents have more
    # than one point, and **zero** of them share an identity.
    #
    # The real alarm is the opposite shape: one chunk id, several points *with the same
    # identity*. The id derivation makes that impossible, so if it ever fires the
    # derivation changed or something wrote with a different scheme -- i.e. a write
    # that is no longer idempotent, which is exactly what silently duplicates chunks.
    by_identity = {}
    for point in points:
        identity = (point["chunk_id"], point.get("document_version_id") or "",
                    point.get("generation_id") or "")
        by_identity[identity] = by_identity.get(identity, 0) + 1

    non_idempotent = sorted(
        chunk_id for (chunk_id, _, _), count in by_identity.items() if count > 1
    )
    if non_idempotent:
        findings.append(
            {
                "kind": "duplicate_chunk_points",
                "doc_id": doc_id,
                "file_name": file_name,
                "publication_status": publication_status,
                "points_total": len(points),
                "unique_chunk_ids": len({point["chunk_id"] for point in points}),
                "duplicated_chunk_ids": non_idempotent,
                "detail": (
                    "the same chunk id appears more than once *within one generation*: "
                    "the point id is derived from (generation, chunk id), so a write "
                    "that used the same identity should have replaced instead of "
                    "appending -- the store now holds copies no count or digest can "
                    "tell apart"
                ),
            }
        )

    return findings


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--api-base", default=os.environ.get("AI_ETL_API", DEFAULT_API))
    parser.add_argument("--token", default=os.environ.get("AI_ETL_TOKEN", ""))
    parser.add_argument("--es", default=os.environ.get("AI_ETL_ES", DEFAULT_ES))
    parser.add_argument("--es-index", default=os.environ.get("AI_ETL_ES_INDEX", DEFAULT_ES_INDEX))
    parser.add_argument("--qdrant", default=os.environ.get("AI_ETL_QDRANT", DEFAULT_QDRANT))
    parser.add_argument("--collection", default=os.environ.get("AI_ETL_COLLECTION", DEFAULT_COLLECTION))
    parser.add_argument("--qdrant-key", default=os.environ.get("QDRANT_API_KEY", ""))
    parser.add_argument("--limit", type=int, default=500)
    parser.add_argument("--report-dir", default=DEFAULT_REPORT_DIR)
    parser.add_argument("--quiet", action="store_true")
    args = parser.parse_args()

    if not args.token:
        print("AI_ETL_TOKEN (or --token) is required", file=sys.stderr)
        return 2

    documents = list_documents(args.api_base, args.token, args.limit)
    if not args.quiet:
        print(f"documents listed : {len(documents)}")

    findings = []
    checked = 0
    for document in documents:
        doc_id = document.get("doc_id") or ""
        if not doc_id or document.get("status") != "completed":
            continue
        checked += 1

        try:
            points = qdrant_points(args.qdrant, args.collection, doc_id, args.qdrant_key)
            stored = qdrant_chunk_ids(points)
            indexed = es_count(args.es, args.es_index, doc_id)
            visible = endpoint_chunk_ids(args.api_base, args.token, doc_id)
        except urllib.error.HTTPError as error:
            findings.append(
                {
                    "kind": "probe_failed",
                    "doc_id": doc_id,
                    "detail": f"HTTP {error.code} {error.reason}",
                }
            )
            continue

        findings.extend(classify_document(doc_id, document, stored, indexed, visible))
        findings.extend(classify_payload_fields(doc_id, document, points))

    by_kind = {}
    for finding in findings:
        by_kind.setdefault(finding["kind"], []).append(finding)

    if not args.quiet:
        print(f"documents checked: {checked}")
        for kind in sorted(by_kind):
            print(f"  {kind:<24} {len(by_kind[kind])}")

    report = {
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "api_base": args.api_base,
        "es_index": args.es_index,
        "collection": args.collection,
        "documents_checked": checked,
        "counts": {kind: len(items) for kind, items in sorted(by_kind.items())},
        "findings": findings,
    }

    if args.report_dir:
        os.makedirs(args.report_dir, exist_ok=True)
        stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
        path = os.path.join(args.report_dir, f"index-consistency-{stamp}.json")
        with open(path, "w", encoding="utf-8") as handle:
            json.dump(report, handle, ensure_ascii=False, indent=2)
        if not args.quiet:
            print(f"report           : {path}")

    if not args.quiet:
        for finding in findings:
            print(f"  - [{finding['kind']}] {finding['doc_id']} {finding.get('file_name', '')}")
            if finding.get("hidden_chunk_ids"):
                print(f"      hidden: {finding['hidden_chunk_ids']}")
            for key in ("unset_chunk_ids", "drifted_chunk_ids", "duplicated_chunk_ids"):
                if finding.get(key):
                    print(f"      {key}: {finding[key][:8]}")

    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
