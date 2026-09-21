#!/usr/bin/env python3
"""Assemble the backup manifest and decide whether the backup is trustworthy.

A backup that captures a catalog referencing source objects the object store no
longer holds is not a backup of a working system, and nothing in the platform
says so: `documents` rows stay `completed`, retrieval keeps answering, and the
index manifests stay healthy. The divergence only becomes visible when a repair
replays an ingestion event and cannot materialize the object.

So the manifest carries an explicit integrity verdict instead of just a list of
files:

  status=ok         every referenced object key is present in the store
  status=degraded   N referenced keys have no object (exit code 3)

Artifacts are hashed, so a restore can prove it replayed exactly what was
written. The full key list lives in `catalog-object-keys.txt`, which is itself a
hashed component; the manifest only carries counts and samples.

Usage:
  python3 scripts/backup-manifest.py \
      --dir "$BACKUP_DIR" --project ai-etl-platform --environment staging \
      --catalog-keys "$BACKUP_DIR/catalog-object-keys.txt" \
      --catalog-counts "$BACKUP_DIR/catalog-counts.json" \
      --inventory "$BACKUP_DIR/object-inventory.json" \
      --projections "$BACKUP_DIR/projections.json" \
      --component postgres=postgres.dump \
      --component minio=minio-data.tar
"""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

SAMPLE_LIMIT = 20
SCHEMA_VERSION = 1
EXIT_DEGRADED = 3


def sha256_file(path: Path, chunk: int = 1 << 20) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        while True:
            block = handle.read(chunk)
            if not block:
                break
            digest.update(block)
    return digest.hexdigest()


def load_catalog_keys(path: Path | None) -> list[str]:
    if path is None or not path.exists():
        return []
    return [line.strip() for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dir", type=Path, required=True)
    parser.add_argument("--project", required=True)
    parser.add_argument("--environment", default="")
    parser.add_argument("--catalog-keys", type=Path)
    parser.add_argument("--catalog-counts", type=Path)
    parser.add_argument("--inventory", type=Path, required=True)
    parser.add_argument(
        "--projections",
        type=Path,
        help="JSON of projection sizes recorded at backup time (qdrant points, ES docs)",
    )
    parser.add_argument("--component", action="append", default=[])
    parser.add_argument("--out", type=Path)
    parser.add_argument(
        "--require-integrity",
        action="store_true",
        help="treat a degraded integrity verdict as a hard failure",
    )
    args = parser.parse_args()

    backup_dir: Path = args.dir
    out_path = args.out or (backup_dir / "manifest.json")

    components: dict[str, dict] = {}
    for spec in args.component:
        name, _, relative = spec.partition("=")
        if not name or not relative:
            parser.error(f"--component must be name=relative-path, got {spec!r}")
        path = backup_dir / relative
        if not path.exists():
            print(f"missing backup artifact: {path}", file=sys.stderr)
            return 2
        components[name] = {
            "file": relative,
            "bytes": path.stat().st_size,
            "sha256": sha256_file(path),
        }

    inventory = json.loads(args.inventory.read_text(encoding="utf-8"))
    present: dict[str, int] = inventory.get("objects") or {}
    referenced = load_catalog_keys(args.catalog_keys)

    referenced_set = set(referenced)
    present_set = set(present)
    missing = sorted(referenced_set - present_set)
    orphans = sorted(present_set - referenced_set)

    counts: dict[str, int] = {}
    if args.catalog_counts and args.catalog_counts.exists():
        counts = json.loads(args.catalog_counts.read_text(encoding="utf-8"))

    status = "degraded" if missing else "ok"
    projections: dict = {}
    if args.projections and args.projections.exists():
        projections = json.loads(args.projections.read_text(encoding="utf-8"))
    manifest = {
        "schema_version": SCHEMA_VERSION,
        "created_at": datetime.now(timezone.utc).isoformat(),
        "project": args.project,
        "environment": args.environment,
        "components": components,
        "catalog": {
            "counts": counts,
            "documents_with_object_key": len(referenced),
        },
        "projections": projections,
        "object_store": {
            "endpoint": inventory.get("endpoint"),
            "bucket": inventory.get("bucket"),
            "total": inventory.get("total"),
            "bytes": inventory.get("bytes"),
        },
        "integrity": {
            "status": status,
            "missing_objects": len(missing),
            "missing_sample": missing[:SAMPLE_LIMIT],
            "orphan_objects": len(orphans),
            "orphan_sample": orphans[:SAMPLE_LIMIT],
        },
    }
    out_path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")

    print(f"[manifest] {out_path}")
    for name, entry in sorted(components.items()):
        print(f"  {name}: {entry['bytes']} bytes sha256:{entry['sha256'][:16]}")
    print(f"  catalog counts: {counts or 'n/a'}")
    print(f"  projections: {projections or 'n/a'}")
    print(
        f"  object store: {manifest['object_store']['total']} objects, "
        f"{manifest['object_store']['bytes']} bytes"
    )
    print(
        f"  integrity: {status} — {len(missing)} referenced objects missing, "
        f"{len(orphans)} unreferenced objects"
    )
    for key in missing[:SAMPLE_LIMIT]:
        print(f"    missing {key}")

    if missing and args.require_integrity:
        return EXIT_DEGRADED
    return 0


if __name__ == "__main__":
    sys.exit(main())
