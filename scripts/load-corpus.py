#!/usr/bin/env python3
"""Seed the demo knowledge base from the semantic golden set.

Uploads each case in docs/evals/semantic-golden-set.json as a document so the
interview demo has searchable content immediately. Uses the demo tenant.

Usage:
  python3 scripts/seed-demo-data.py --api-base http://localhost:8080 --token <jwt>
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from urllib import error as urllib_error
from urllib import request

ROOT = Path(__file__).resolve().parent.parent
GOLDEN_SET = ROOT / "docs" / "evals" / "semantic-golden-set.json"


def multipart_body(filename: str, content: str, doc_id: str, tenant_id: str, permission: str) -> bytes:
    boundary = "----seed-boundary"
    parts = []
    for name, value in (("doc_id", doc_id), ("tenant_id", tenant_id), ("permission", permission)):
        parts.append(
            f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n{value}\r\n'.encode()
        )
    parts.append(
        f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{filename}"\r\n'
        f"Content-Type: text/plain\r\n\r\n".encode()
    )
    parts.append(content.encode("utf-8"))
    parts.append(f"\r\n--{boundary}--\r\n".encode())
    return b"".join(parts)


def main() -> int:
    parser = argparse.ArgumentParser(description="Seed demo knowledge base from semantic golden set")
    parser.add_argument("--api-base", default="http://localhost:8080")
    parser.add_argument("--token", required=True, help="JWT with upload scope")
    parser.add_argument("--tenant-id", default="demo")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    cases = json.loads(GOLDEN_SET.read_text(encoding="utf-8"))["cases"]
    base = args.api_base.rstrip("/")

    ok, failed = 0, 0
    for case in cases:
        doc_id = case["id"]
        body = multipart_body(
            f"{doc_id}.txt",
            case["content"],
            doc_id,
            args.tenant_id,
            case.get("permission", "internal"),
        )
        req = request.Request(f"{base}/v1/upload", data=body, method="POST")
        req.add_header("Authorization", f"Bearer {args.token}")
        req.add_header("Content-Type", "multipart/form-data; boundary=----seed-boundary")
        try:
            if args.dry_run:
                print(f"[seed] dry-run: {doc_id}")
                ok += 1
                continue
            with request.urlopen(req, timeout=60) as resp:
                if resp.status == 202:
                    ok += 1
                else:
                    failed += 1
                    print(f"[seed] {doc_id} returned {resp.status}")
        except urllib_error.HTTPError as e:
            failed += 1
            print(f"[seed] {doc_id} failed: {e.code} {e.read().decode()[:120]}")

    print(f"\n[seed] uploaded {ok}, failed {failed} ({len(cases)} total)")
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
