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


def qdrant_chunk_ids(qdrant, collection, doc_id, api_key):
    headers = {"api-key": api_key} if api_key else {}
    payload = http_json(
        f"{qdrant}/collections/{collection}/points/scroll",
        method="POST",
        body={
            "filter": {"must": [{"key": "doc_id", "match": {"value": doc_id}}]},
            "limit": 1000,
            "with_payload": True,
            "with_vector": False,
        },
        headers=headers,
    )
    ids = []
    for point in payload.get("result", {}).get("points", []):
        chunk_id = point.get("payload", {}).get("chunk_id")
        ids.append(chunk_id or str(point.get("id")))
    return ids


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
            stored = qdrant_chunk_ids(args.qdrant, args.collection, doc_id, args.qdrant_key)
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

    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main())
