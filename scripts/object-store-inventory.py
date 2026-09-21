#!/usr/bin/env python3
"""List every object in the source-object bucket, over the S3 API.

The catalog (`documents.object_key`) is the only place that says a document's
immutable source object exists. Nothing else cross-checks it against the object
store, so this script is the cross-check: it answers "which keys does the store
actually hold?" using only the standard library (SigV4 + ListObjectsV2), which
matters because the deployment image has neither `mc` nor the AWS CLI.

Usage:
  python3 scripts/object-store-inventory.py \
      --endpoint http://127.0.0.1:9000 --bucket documents \
      --access-key-file secrets/dev/s3_access_key \
      --secret-key-file secrets/dev/s3_secret_key \
      --out /path/to/objects.json

Output JSON:
  {"endpoint": ..., "bucket": ..., "total": N, "bytes": B, "objects": {key: size}}
"""

from __future__ import annotations

import argparse
import datetime
import hashlib
import hmac
import json
import sys
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET
from pathlib import Path

EMPTY_SHA256 = hashlib.sha256(b"").hexdigest()
XMLNS = "{http://s3.amazonaws.com/doc/2006-03-01/}"
MAX_PAGES = 500


def _sign(key: bytes, message: str) -> bytes:
    return hmac.new(key, message.encode("utf-8"), hashlib.sha256).digest()


def _list_page(
    endpoint: str,
    bucket: str,
    region: str,
    access_key: str,
    secret_key: str,
    prefix: str,
    continuation: str | None,
    timeout: float,
) -> ET.Element:
    now = datetime.datetime.now(datetime.timezone.utc)
    amz_date = now.strftime("%Y%m%dT%H%M%SZ")
    datestamp = now.strftime("%Y%m%d")

    params = {"list-type": "2", "max-keys": "1000"}
    if prefix:
        params["prefix"] = prefix
    if continuation:
        params["continuation-token"] = continuation
    # SigV4 requires the query string to be URI-encoded and sorted by name.
    query = "&".join(
        f"{urllib.parse.quote(k, safe='-_.~')}={urllib.parse.quote(v, safe='-_.~')}"
        for k, v in sorted(params.items())
    )

    host = urllib.parse.urlparse(endpoint).netloc
    canonical_headers = (
        f"host:{host}\n"
        f"x-amz-content-sha256:{EMPTY_SHA256}\n"
        f"x-amz-date:{amz_date}\n"
    )
    signed_headers = "host;x-amz-content-sha256;x-amz-date"
    canonical_request = "\n".join(
        ["GET", f"/{bucket}/", query, canonical_headers, signed_headers, EMPTY_SHA256]
    )
    scope = f"{datestamp}/{region}/s3/aws4_request"
    string_to_sign = "\n".join(
        [
            "AWS4-HMAC-SHA256",
            amz_date,
            scope,
            hashlib.sha256(canonical_request.encode("utf-8")).hexdigest(),
        ]
    )
    signing_key = _sign(("AWS4" + secret_key).encode("utf-8"), datestamp)
    for part in (region, "s3", "aws4_request"):
        signing_key = _sign(signing_key, part)
    signature = hmac.new(
        signing_key, string_to_sign.encode("utf-8"), hashlib.sha256
    ).hexdigest()

    request = urllib.request.Request(f"{endpoint}/{bucket}/?{query}", method="GET")
    request.add_header("Host", host)
    request.add_header("x-amz-content-sha256", EMPTY_SHA256)
    request.add_header("x-amz-date", amz_date)
    request.add_header(
        "Authorization",
        f"AWS4-HMAC-SHA256 Credential={access_key}/{scope}, "
        f"SignedHeaders={signed_headers}, Signature={signature}",
    )
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return ET.fromstring(response.read())


def inventory(
    endpoint: str,
    bucket: str,
    region: str,
    access_key: str,
    secret_key: str,
    prefix: str = "",
    timeout: float = 30.0,
) -> dict:
    objects: dict[str, int] = {}
    continuation: str | None = None
    for _ in range(MAX_PAGES):
        root = _list_page(
            endpoint, bucket, region, access_key, secret_key, prefix, continuation, timeout
        )
        for content in root.findall(f"{XMLNS}Contents"):
            key = content.findtext(f"{XMLNS}Key") or ""
            if not key:
                continue
            objects[key] = int(content.findtext(f"{XMLNS}Size") or 0)
        if (root.findtext(f"{XMLNS}IsTruncated") or "false") != "true":
            break
        continuation = root.findtext(f"{XMLNS}NextContinuationToken")
        if not continuation:
            break
    else:
        raise RuntimeError(f"object listing exceeded {MAX_PAGES} pages")
    return {
        "endpoint": endpoint,
        "bucket": bucket,
        "prefix": prefix,
        "total": len(objects),
        "bytes": sum(objects.values()),
        "objects": objects,
    }


def _read_secret(inline: str | None, path: str | None, label: str) -> str:
    if inline:
        return inline
    if not path:
        raise SystemExit(f"{label} is required (use --{label.replace('_', '-')})")
    value = Path(path).read_text(encoding="utf-8").strip()
    if not value:
        raise SystemExit(f"{label} file {path} is empty")
    return value


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--endpoint", default="http://127.0.0.1:9000")
    parser.add_argument("--bucket", default="documents")
    parser.add_argument("--region", default="us-east-1")
    parser.add_argument("--access-key")
    parser.add_argument("--secret-key")
    parser.add_argument("--access-key-file")
    parser.add_argument("--secret-key-file")
    parser.add_argument("--prefix", default="")
    parser.add_argument("--timeout", type=float, default=30.0)
    parser.add_argument("--out", help="write the JSON inventory here instead of stdout")
    args = parser.parse_args()

    access_key = _read_secret(args.access_key, args.access_key_file, "access_key")
    secret_key = _read_secret(args.secret_key, args.secret_key_file, "secret_key")
    try:
        result = inventory(
            args.endpoint,
            args.bucket,
            args.region,
            access_key,
            secret_key,
            args.prefix,
            args.timeout,
        )
    except Exception as exc:  # noqa: BLE001 - the CLI reports the raw cause
        print(f"object store inventory failed: {exc}", file=sys.stderr)
        return 2

    payload = json.dumps(result, indent=2, sort_keys=True)
    if args.out:
        Path(args.out).write_text(payload + "\n", encoding="utf-8")
    else:
        print(payload)
    print(
        f"[inventory] {result['total']} objects, {result['bytes']} bytes "
        f"in {args.endpoint}/{args.bucket}",
        file=sys.stderr,
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
