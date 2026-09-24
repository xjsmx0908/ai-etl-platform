#!/usr/bin/env python3
"""Backfill documents.chunks_done / chunks_total for rows written before 4cf5963.

`documents.chunks_done` / `chunks_total` are a denormalised cache of the chunk count.
The document list and the detail page render them, and the expression is
`chunks_total ? `${done}/${total}` : "-"` -- so a row whose total was never recorded
shows a dash. On the detail page that dash sits directly above "文档切块（N）", which
lists the chunks: the page contradicts itself.

The write path is already fixed (4cf5963, defect 3, 2026-09-21); every row created on
or after 2026-09-22 has both columns. This script repairs the rows created before it --
99 rows in the `default` tenant, all with chunks_total = 0.

The value written is the number of **distinct chunk ids in the store** for that
document, which is the same baseline `scripts/check-index-consistency.py` compares
against, so a repaired row and the store agree and the check goes quiet.

Dry run by default. `--apply` writes; `--rollback <path>` always writes a script that
puts every row it is about to touch back to the values it had.

Usage:
    python3 scripts/backfill-document-chunk-counts.py --dump /tmp/before.tsv
    python3 scripts/backfill-document-chunk-counts.py --apply --rollback /tmp/rollback.sql

Run it from the repository root: it shells out to `docker compose exec postgres psql`.
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request

DEFAULT_REPO = "."
DEFAULT_QDRANT = "http://127.0.0.1:6333"
DEFAULT_COLLECTION = "documents-v2"

# Loopback services; the host may carry http_proxy for other work.
_OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def psql(repo, sql):
    result = subprocess.run(
        ["docker", "compose", "exec", "-T", "postgres", "psql", "-U", "app",
         "-d", "ai_etl", "-tA", "-F", "\t", "-c", sql],
        cwd=repo, capture_output=True, text=True, timeout=180,
    )
    if result.returncode != 0:
        raise SystemExit(f"psql failed: {result.stderr.strip()}")
    return result.stdout.strip()


def scroll(qdrant, collection, doc_id, key):
    """Every stored point for one document, following next_page_offset."""
    points, offset = [], None
    while True:
        body = {
            "filter": {"must": [{"key": "doc_id", "match": {"value": doc_id}}]},
            "limit": 1000,
            "with_payload": ["chunk_id"],
            "with_vector": False,
        }
        if offset is not None:
            body["offset"] = offset
        request = urllib.request.Request(
            f"{qdrant}/collections/{collection}/points/scroll",
            data=json.dumps(body).encode(),
            headers={"Content-Type": "application/json", "api-key": key},
            method="POST",
        )
        with _OPENER.open(request, timeout=180) as response:
            result = json.load(response).get("result", {})
        points.extend(result.get("points", []))
        offset = result.get("next_page_offset")
        if offset is None:
            return points


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--repo", default=os.environ.get("AI_ETL_REPO", DEFAULT_REPO))
    parser.add_argument("--tenant", default="default")
    parser.add_argument("--qdrant", default=os.environ.get("AI_ETL_QDRANT", DEFAULT_QDRANT))
    parser.add_argument("--collection", default=os.environ.get("AI_ETL_COLLECTION", DEFAULT_COLLECTION))
    parser.add_argument("--qdrant-key", default=os.environ.get("QDRANT_API_KEY", ""))
    parser.add_argument("--apply", action="store_true", help="write; without it this is a dry run")
    parser.add_argument("--dump", default="", help="write the before/after table here (TSV)")
    parser.add_argument("--rollback", default="", help="write a rollback SQL script here")
    args = parser.parse_args()

    if not args.qdrant_key:
        print("QDRANT_API_KEY (or --qdrant-key) is required", file=sys.stderr)
        return 2

    # Only rows whose total was never recorded. A row whose total is set is either
    # correct or a different story (re-ingested), and this script has no business
    # guessing about it.
    rows = psql(
        args.repo,
        "SELECT doc_id, COALESCE(chunks_done,0), COALESCE(chunks_total,0) FROM documents "
        f"WHERE tenant_id='{args.tenant}' AND status='completed' "
        "AND COALESCE(chunks_total,0)=0 ORDER BY doc_id",
    )
    targets = []
    for line in rows.split("\n"):
        parts = line.split("\t")
        if len(parts) == 3:
            targets.append((parts[0], int(parts[1]), int(parts[2])))

    print(f"tenant {args.tenant}: {len(targets)} completed rows with chunks_total = 0")

    plan, skipped = [], []
    for doc_id, old_done, old_total in targets:
        try:
            points = scroll(args.qdrant, args.collection, doc_id, args.qdrant_key)
        except urllib.error.HTTPError as error:
            print(f"  probe failed for {doc_id}: HTTP {error.code} {error.reason}", file=sys.stderr)
            return 1
        expected = len({p.get("payload", {}).get("chunk_id") for p in points} - {None, ""})
        if expected == 0:
            # Nothing stored: 0 is the right answer, so the row is already consistent
            # and the consistency check would not have reported it.
            skipped.append(doc_id)
            continue
        plan.append((doc_id, old_done, old_total, expected))

    print(f"  to repair : {len(plan)}")
    print(f"  left alone: {len(skipped)} (no stored chunks, 0 is already correct)")

    if args.dump:
        with open(args.dump, "w", encoding="utf-8") as handle:
            handle.write("doc_id\told_chunks_done\told_chunks_total\tnew_count\n")
            for doc_id, old_done, old_total, new in plan:
                handle.write(f"{doc_id}\t{old_done}\t{old_total}\t{new}\n")
        print(f"  before/after dumped to {args.dump}")

    if args.rollback:
        with open(args.rollback, "w", encoding="utf-8") as handle:
            handle.write("BEGIN;\n")
            for doc_id, old_done, old_total, _ in plan:
                handle.write(
                    "UPDATE documents SET chunks_done=%d, chunks_total=%d "
                    "WHERE tenant_id='%s' AND doc_id='%s';\n"
                    % (old_done, old_total, args.tenant, doc_id)
                )
            handle.write("COMMIT;\n")
        print(f"  rollback written to {args.rollback}")

    if not args.apply:
        print("dry run -- nothing written. Re-run with --apply to write.")
        return 0

    if not plan:
        print("nothing to write.")
        return 0

    # updated_at is deliberately not touched: this is a repair of a cache column, not
    # a change to the document, and bumping it would falsify "last modified".
    #
    # No explicit BEGIN/COMMIT: psql -c sends the whole string as one simple query, and
    # PostgreSQL runs a multi-statement simple query in a single implicit transaction,
    # so the repair is all-or-nothing either way.
    statements = []
    for doc_id, _, _, new in plan:
        statements.append(
            "UPDATE documents SET chunks_done=%d, chunks_total=%d "
            "WHERE tenant_id='%s' AND doc_id='%s';" % (new, new, args.tenant, doc_id)
        )
    psql(args.repo, "".join(statements))
    print(f"applied: {len(plan)} rows set to their stored distinct chunk count")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
