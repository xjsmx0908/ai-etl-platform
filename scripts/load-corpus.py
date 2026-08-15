#!/usr/bin/env python3
"""Load a corpus from a JSON eval set into the knowledge base via the upload API.

Each case in the source file (default: the semantic golden set) is uploaded as a
document so a fresh environment has searchable content without manual uploads.
Authenticates via /v1/auth/login (username+password) or accepts a pre-issued
token.

Usage:
  python3 scripts/load-corpus.py --api-base http://localhost:8080 \
      --username admin --password <pw>
  python3 scripts/load-corpus.py --api-base http://localhost:8080 --token <jwt>
  python3 scripts/load-corpus.py --source ./my-corpus.json --tenant-id acme \
      --username alice --password <pw>
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from urllib import error as urllib_error
from urllib import request

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_SOURCE = ROOT / "docs" / "evals" / "semantic-golden-set.json"


def login(api_base: str, username: str, password: str) -> tuple[str, str]:
    body = json.dumps({"username": username, "password": password}).encode()
    req = request.Request(f"{api_base}/v1/auth/login", data=body, method="POST")
    req.add_header("Content-Type", "application/json")
    with request.urlopen(req, timeout=30) as resp:
        data = json.loads(resp.read())
    return data["token"], data["user"]["tenant_id"]


def multipart_body(filename: str, content: str, doc_id: str, tenant_id: str, permission: str) -> bytes:
    boundary = "----load-corpus-boundary"
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
    parser = argparse.ArgumentParser(description="Load a corpus into the knowledge base")
    parser.add_argument("--api-base", default="http://localhost:8080")
    auth = parser.add_mutually_exclusive_group(required=True)
    auth.add_argument("--token", help="JWT with upload scope (skips login)")
    auth.add_argument("--username", help="login username (with --password)")
    parser.add_argument("--password", default="")
    parser.add_argument("--source", type=Path, default=DEFAULT_SOURCE)
    parser.add_argument("--tenant-id", help="upload tenant; defaults to the login user's tenant")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    base = args.api_base.rstrip("/")

    token = args.token
    tenant_id = args.tenant_id
    if token is None:
        if not args.username or not args.password:
            print("--username/--password are required when --token is not given", file=sys.stderr)
            return 2
        token, logged_in_tenant = login(base, args.username, args.password)
        tenant_id = tenant_id or logged_in_tenant
        print(f"[load] logged in as {args.username} (tenant {tenant_id})")
    tenant_id = tenant_id or "default"

    cases = json.loads(args.source.read_text(encoding="utf-8"))["cases"]
    ok, failed = 0, 0
    for case in cases:
        doc_id = case["id"]
        body = multipart_body(
            f"{doc_id}.txt",
            case["content"],
            doc_id,
            tenant_id,
            case.get("permission", "internal"),
        )
        req = request.Request(f"{base}/v1/upload", data=body, method="POST")
        req.add_header("Authorization", f"Bearer {token}")
        req.add_header("Content-Type", "multipart/form-data; boundary=----load-corpus-boundary")
        try:
            if args.dry_run:
                print(f"[load] dry-run: {doc_id}")
                ok += 1
                continue
            with request.urlopen(req, timeout=60) as resp:
                if resp.status == 202:
                    ok += 1
                else:
                    failed += 1
                    print(f"[load] {doc_id} returned {resp.status}")
        except urllib_error.HTTPError as e:
            failed += 1
            print(f"[load] {doc_id} failed: {e.code} {e.read().decode()[:120]}")

    print(f"\n[load] uploaded {ok}, failed {failed} ({len(cases)} total)")
    return 0 if failed == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
