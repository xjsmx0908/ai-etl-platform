#!/usr/bin/env python3
"""Re-index documents from MinIO after an embedding model change.

For each object in the bucket (key = tenant/{docID}{ext}):
  1. delete the old document via DELETE /v1/documents/{docID}  (removes old
     vectors — dimensions differ after a model change)
  2. download the object and re-upload it via POST /v1/upload to re-run the
     parse -> embed -> store pipeline

Requires the `minio` Python package (pip install minio).

Usage:
  python3 scripts/reindex.py --api-base http://localhost:8080 --token <jwt> \
      --s3-endpoint localhost:9000 --s3-access-key ... --s3-secret-key ... \
      --s3-bucket documents --dry-run
"""

from __future__ import annotations

import argparse
import sys
from typing import Dict, List, Tuple
from urllib import error as urllib_error
from urllib import request


def api_json(method: str, url: str, token: str, body: bytes | None = None) -> Tuple[int, Dict]:
    req = request.Request(url=url, data=body, method=method)
    req.add_header("Authorization", f"Bearer {token}")
    if body:
        req.add_header("Content-Type", "application/json")
    try:
        with request.urlopen(req, timeout=120) as resp:
            raw = resp.read()
            return resp.status, {}
    except urllib_error.HTTPError as e:
        return e.code, {}


def parse_object_key(key: str) -> Tuple[str, str]:
    """Split tenant/{docID}{ext} into (tenant, docID). docID contains no dots."""
    rest = key.split("/", 1)[1] if "/" in key else key
    doc_id = rest.rsplit(".", 1)[0]
    return key.split("/", 1)[0] if "/" in key else "", doc_id


def reindex(client: Minio, bucket: str, api_base: str, token: str, dry_run: bool) -> List[Dict]:
    results: List[Dict] = []
    for obj in client.list_objects(bucket, recursive=True):
        key = obj.object_name
        tenant, doc_id = parse_object_key(key)
        base = api_base.rstrip("/")

        if dry_run:
            print(f"[reindex] dry-run: would delete {doc_id} then re-upload {key}")
            results.append({"key": key, "doc_id": doc_id, "action": "dry-run"})
            continue

        # 1. Delete old document (removes old vectors + ES docs).
        status, _ = api_json("DELETE", f"{base}/v1/documents/{doc_id}", token)
        if status != 204:
            print(f"[reindex] delete {doc_id} returned {status}, skipping upload", file=sys.stderr)
            results.append({"key": key, "doc_id": doc_id, "action": "delete_failed", "status": status})
            continue

        # 2. Download and re-upload.
        data = client.get_object(bucket, key)
        try:
            raw = data.read()
            ctype = data.headers.get("Content-Type", "application/octet-stream")
        finally:
            data.close()
            data.release_conn()

        boundary = "----reindex-boundary"
        filename = key.rsplit("/", 1)[-1]
        fields = {
            "doc_id": doc_id,
            "tenant_id": tenant,
        }
        parts = []
        for name, value in fields.items():
            parts.append(f"--{boundary}\r\nContent-Disposition: form-data; name=\"{name}\"\r\n\r\n{value}\r\n".encode())
        parts.append(
            f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{filename}\"\r\n"
            f"Content-Type: {ctype}\r\n\r\n".encode()
        )
        parts.append(raw)
        parts.append(f"\r\n--{boundary}--\r\n".encode())
        body = b"".join(parts)

        req = request.Request(f"{base}/v1/upload", data=body, method="POST")
        req.add_header("Authorization", f"Bearer {token}")
        req.add_header("Content-Type", f"multipart/form-data; boundary={boundary}")
        try:
            with request.urlopen(req, timeout=120) as resp:
                if resp.status == 202:
                    print(f"[reindex] re-uploaded {doc_id} from {key}")
                    results.append({"key": key, "doc_id": doc_id, "action": "reindexed"})
                else:
                    results.append({"key": key, "doc_id": doc_id, "action": "upload_status", "status": resp.status})
        except urllib_error.HTTPError as e:
            print(f"[reindex] upload {doc_id} failed: {e.code}", file=sys.stderr)
            results.append({"key": key, "doc_id": doc_id, "action": "upload_failed", "status": e.code})

    return results


def main() -> int:
    try:
        from minio import Minio
    except ImportError:
        print("missing dependency: pip install minio", file=sys.stderr)
        return 2

    parser = argparse.ArgumentParser(description="Re-index MinIO documents after embedding model change")
    parser.add_argument("--api-base", required=True, help="Query API base URL")
    parser.add_argument("--token", required=True, help="JWT with upload scope")
    parser.add_argument("--s3-endpoint", required=True, help="MinIO endpoint host:port")
    parser.add_argument("--s3-access-key", required=True)
    parser.add_argument("--s3-secret-key", required=True)
    parser.add_argument("--s3-bucket", default="documents")
    parser.add_argument("--s3-secure", action="store_true", help="use HTTPS")
    parser.add_argument("--dry-run", action="store_true", help="list what would be re-indexed")
    args = parser.parse_args()

    client = Minio(
        args.s3_endpoint,
        access_key=args.s3_access_key,
        secret_key=args.s3_secret_key,
        secure=args.s3_secure,
    )
    results = reindex(client, args.s3_bucket, args.api_base, args.token, args.dry_run)
    print(f"\n[reindex] {len(results)} object(s) processed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
