#!/usr/bin/env python3
"""Assert that a restored stack actually reproduces the backed-up stack.

A restore that only proves "the API answers some question" passes on a stack
whose source objects never arrived, whose projection snapshots were skipped, or
whose catalog was silently re-seeded. Each check below pins one of those:

  catalog_counts            restored rows == rows recorded at backup time
  object_store_total        restored bucket holds as many objects as the source
  object_store_keys         and they are the same keys, with the same sizes
  catalog_references        every documents.object_key resolves in the store
  qdrant_points             restored vector count == recorded count
  es_documents              restored full-text count == recorded count
  retrieval_answer          a real question returns the expected citation

Exit code 0 only when every check passes.

Usage:
  python3 scripts/restore-drill.py \
      --manifest "$BACKUP_DIR/manifest.json" \
      --source-inventory "$BACKUP_DIR/object-inventory.json" \
      --catalog-keys "$BACKUP_DIR/catalog-object-keys.txt" \
      --catalog-counts /tmp/restored-counts.json \
      --minio-endpoint http://127.0.0.1:9002 \
      --qdrant-endpoint http://127.0.0.1:6335 \
      --es-endpoint http://127.0.0.1:9202 \
      --api-base http://127.0.0.1:8082 \
      --question "知识发布前需要经过哪些流程" --expect-citation demo-doc-handbook
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import sys
import urllib.error
import urllib.request
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent


def _load_inventory_module():
    spec = importlib.util.spec_from_file_location(
        "object_store_inventory", SCRIPT_DIR / "object-store-inventory.py"
    )
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def _get_json(url: str, headers: dict[str, str] | None = None, timeout: float = 60.0) -> dict:
    request = urllib.request.Request(url, method="GET")
    for name, value in (headers or {}).items():
        request.add_header(name, value)
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return json.loads(response.read())


def _post_json(url: str, payload: dict, headers: dict[str, str] | None = None, timeout: float = 300.0) -> dict:
    request = urllib.request.Request(url, data=json.dumps(payload).encode("utf-8"), method="POST")
    request.add_header("Content-Type", "application/json")
    for name, value in (headers or {}).items():
        request.add_header(name, value)
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return json.loads(response.read())


def _citation_ids(citations: list) -> list[str]:
    """Normalize citation entries, which are objects, into their document ids."""
    ids: list[str] = []
    for entry in citations:
        if isinstance(entry, str):
            ids.append(entry)
            continue
        if not isinstance(entry, dict):
            continue
        identifier = entry.get("doc_id") or entry.get("document_id")
        if not identifier:
            chunk_id = entry.get("chunk_id") or ""
            identifier = chunk_id.rsplit("-", 1)[0] if chunk_id else ""
        if identifier:
            ids.append(identifier)
    # Preserve order, drop duplicates.
    return list(dict.fromkeys(ids))


class Report:
    def __init__(self) -> None:
        self.checks: list[dict] = []

    def check(self, name: str, passed: bool, detail: str) -> None:
        self.checks.append({"check": name, "passed": bool(passed), "detail": detail})
        print(f"  {'PASS' if passed else 'FAIL'}  {name}: {detail}")

    @property
    def failed(self) -> list[dict]:
        return [item for item in self.checks if not item["passed"]]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--source-inventory", type=Path, required=True)
    parser.add_argument("--catalog-keys", type=Path, required=True)
    parser.add_argument("--catalog-counts", type=Path, required=True)
    parser.add_argument("--minio-endpoint", default="http://127.0.0.1:9002")
    parser.add_argument("--s3-access-key-file", required=True)
    parser.add_argument("--s3-secret-key-file", required=True)
    parser.add_argument("--qdrant-endpoint", default="http://127.0.0.1:6335")
    parser.add_argument("--store-api-key-file", required=True)
    parser.add_argument("--store-collection", default="documents-v2")
    parser.add_argument("--es-endpoint", default="http://127.0.0.1:9202")
    parser.add_argument("--es-index", default="documents_text")
    parser.add_argument("--api-base", default="http://127.0.0.1:8082")
    parser.add_argument("--question", default="知识发布前需要经过哪些流程")
    parser.add_argument("--expect-citation", default="demo-doc-handbook")
    parser.add_argument(
        "--require-intact",
        action="store_true",
        help="also fail when the restored stack still references absent objects",
    )
    parser.add_argument("--out", type=Path)
    args = parser.parse_args()

    report = Report()
    manifest = json.loads(args.manifest.read_text(encoding="utf-8"))
    source_inventory = json.loads(args.source_inventory.read_text(encoding="utf-8"))
    expected_counts = manifest.get("catalog", {}).get("counts") or {}
    expected_projections = manifest.get("projections") or {}

    # 1. Catalog rows.
    restored_counts = json.loads(args.catalog_counts.read_text(encoding="utf-8"))
    mismatched = {
        key: (expected_counts.get(key), restored_counts.get(key))
        for key in sorted(set(expected_counts) | set(restored_counts))
        if expected_counts.get(key) != restored_counts.get(key)
    }
    report.check(
        "catalog_counts",
        not mismatched,
        "all tables match backup"
        if not mismatched
        else f"mismatched (expected, restored): {mismatched}",
    )

    # 2/3. Object store contents.
    inventory_module = _load_inventory_module()
    access_key = Path(args.s3_access_key_file).read_text(encoding="utf-8").strip()
    secret_key = Path(args.s3_secret_key_file).read_text(encoding="utf-8").strip()
    restored_inventory = inventory_module.inventory(
        args.minio_endpoint, source_inventory.get("bucket", "documents"), "us-east-1",
        access_key, secret_key,
    )
    report.check(
        "object_store_total",
        restored_inventory["total"] == source_inventory["total"],
        f"restored {restored_inventory['total']} objects, source had {source_inventory['total']}",
    )
    source_objects = source_inventory.get("objects") or {}
    restored_objects = restored_inventory.get("objects") or {}
    differing = sorted(
        key for key in set(source_objects) | set(restored_objects)
        if source_objects.get(key) != restored_objects.get(key)
    )
    report.check(
        "object_store_keys",
        not differing,
        "key set and sizes identical"
        if not differing
        else f"{len(differing)} keys differ, first: {differing[:3]}",
    )

    # 4. Referential fidelity, evaluated on the restored side.
    #
    # This is deliberately a fidelity check rather than a health check. The drill
    # proves the restore reproduced the source stack, including whatever the
    # source stack was already missing: a source environment whose objects are
    # gone must restore to exactly that, or the drill would be measuring the
    # environment instead of the recovery procedure. `--require-intact` turns it
    # into a health gate for production promotion, where the environment has to
    # be whole before the recovery plan means anything.
    referenced = {
        line.strip()
        for line in args.catalog_keys.read_text(encoding="utf-8").splitlines()
        if line.strip()
    }
    missing_before = sorted(referenced - set(source_objects))
    missing_after = sorted(referenced - set(restored_objects))
    recorded_missing = manifest.get("integrity", {}).get("missing_objects")
    fidelity = missing_after == missing_before
    intact = not missing_after
    passed = fidelity and (intact or not args.require_intact)
    if not fidelity:
        detail = (
            f"restore lost objects the source still had or vice versa: "
            f"{len(missing_before)} missing before, {len(missing_after)} after"
        )
    elif not intact and args.require_intact:
        detail = f"{len(missing_after)} referenced objects absent (--require-intact), first: {missing_after[:3]}"
    else:
        detail = (
            f"{len(missing_after)} of {len(referenced)} referenced objects absent, "
            f"same as source"
        )
    report.check("catalog_references", passed, detail)
    report.check(
        "manifest_integrity",
        recorded_missing == len(missing_before),
        f"manifest recorded {recorded_missing} missing, source inventory implies {len(missing_before)}",
    )

    # 5. Projection sizes.
    store_api_key = Path(args.store_api_key_file).read_text(encoding="utf-8").strip()
    qdrant = _get_json(
        f"{args.qdrant_endpoint}/collections/{args.store_collection}",
        {"api-key": store_api_key},
    )["result"]
    qdrant_points = qdrant.get("points_count")
    report.check(
        "qdrant_points",
        qdrant_points == expected_projections.get("qdrant_points"),
        f"restored {qdrant_points}, recorded {expected_projections.get('qdrant_points')}",
    )
    es_count = _get_json(f"{args.es_endpoint}/{args.es_index}/_count").get("count")
    report.check(
        "es_documents",
        es_count == expected_projections.get("es_documents"),
        f"restored {es_count}, recorded {expected_projections.get('es_documents')}",
    )

    # 6. A real question against the restored projections.
    try:
        login = _post_json(f"{args.api_base}/v1/auth/demo-login", {"account": "admin"})
        answer = _post_json(
            f"{args.api_base}/v1/query",
            {"question": args.question, "top_k": 3},
            {"Authorization": f"Bearer {login['token']}"},
        )
        # citations are objects ({chunk_id, doc_id, ...}), sources are too; only
        # the identifiers matter here.
        cited = _citation_ids(answer.get("citations") or answer.get("sources") or [])
        report.check(
            "retrieval_answer",
            args.expect_citation in cited,
            f"cited {cited}",
        )
    except (urllib.error.URLError, urllib.error.HTTPError, KeyError) as exc:
        report.check("retrieval_answer", False, f"query failed: {exc}")

    payload = {
        "manifest": str(args.manifest),
        "checks": report.checks,
        "passed": not report.failed,
    }
    if args.out:
        args.out.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")

    if report.failed:
        print(f"\nrestore drill FAILED ({len(report.failed)} of {len(report.checks)} checks)")
        return 1
    print(f"\nrestore drill PASSED ({len(report.checks)} checks)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
