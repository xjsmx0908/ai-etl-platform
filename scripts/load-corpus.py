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
import io
import json
import sys
import zipfile
from pathlib import Path
from urllib import error as urllib_error
from urllib import request
from xml.sax.saxutils import escape

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_SOURCE = ROOT / "docs" / "evals" / "semantic-golden-set.json"


def login(api_base: str, username: str, password: str) -> tuple[str, str]:
    body = json.dumps({"username": username, "password": password}).encode()
    req = request.Request(f"{api_base}/v1/auth/login", data=body, method="POST")
    req.add_header("Content-Type", "application/json")
    with request.urlopen(req, timeout=30) as resp:
        data = json.loads(resp.read())
    return data["token"], data["user"]["tenant_id"]


def build_docx(text: str) -> bytes:
    """Build a minimal but valid .docx (OPC/zip + XML) using only the stdlib.

    python-docx (used by parser-service) opens this fine: it reads the
    document.xml paragraphs. The corpus content is single-paragraph text, so
    each newline becomes one <w:p>. Must escape XML special chars.
    """
    paragraphs = text.split("\n")
    body = "".join(
        f'<w:p><w:r><w:t>{escape(p)}</w:t></w:r></w:p>' for p in paragraphs
    )
    sectpr = (
        '<w:sectPr><w:pgSz w:w="11906" w:h="16838"/>'
        '<w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" '
        'w:header="720" w:footer="720" w:gutter="0"/></w:sectPr>'
    )
    content_types = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n'
        '<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">'
        '<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>'
        '<Default Extension="xml" ContentType="application/xml"/>'
        '<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>'
        "</Types>"
    )
    rels = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n'
        '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
        '<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>'
        "</Relationships>"
    )
    document = (
        '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>\n'
        '<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">'
        f"<w:body>{body}{sectpr}</w:body></w:document>"
    )
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr("[Content_Types].xml", content_types)
        z.writestr("_rels/.rels", rels)
        z.writestr("word/document.xml", document)
    return buf.getvalue()


# Controlled-document fields the upload API accepts. They are admin-only server
# side, so a corpus that carries them must be loaded with an admin token; a
# non-admin load of such a corpus is rejected with 400 rather than silently
# dropping the governance intent.
GOVERNANCE_FIELDS = ("doc_status", "effective_date", "supersedes", "owner")


def multipart_body(
    filename: str,
    content_type: str,
    data: bytes,
    doc_id: str,
    tenant_id: str,
    permission: str,
    knowledge_base_id: str,
    applicable_scope: str,
    governance: dict[str, str] | None = None,
) -> bytes:
    boundary = "----load-corpus-boundary"
    parts = []
    fields = [
        ("doc_id", doc_id),
        ("tenant_id", tenant_id),
        ("permission", permission),
        ("knowledge_base_id", knowledge_base_id),
        ("applicable_scope", applicable_scope),
    ]
    # Only send governance fields the case actually sets: an empty value means
    # "not supplied" to the registry, and sending one for every case would make
    # the request look like an editorial act on documents that need none.
    for name, value in (governance or {}).items():
        fields.append((name, value))
    for name, value in fields:
        parts.append(
            f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n{value}\r\n'.encode()
        )
    parts.append(
        f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{filename}"\r\n'
        f"Content-Type: {content_type}\r\n\r\n".encode()
    )
    parts.append(data)
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
    parser.add_argument(
        "--generated-dir",
        type=Path,
        default=ROOT / "docs" / "corpora" / "generated",
        help="dir with rendered pdf/png files (from generate-corpus-files.py)",
    )
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

    corpus = json.loads(args.source.read_text(encoding="utf-8"))
    cases = corpus["cases"]
    knowledge_base_id = corpus.get("knowledge_base_id", corpus.get("name", "enterprise-demo"))
    applicable_scope = corpus.get("applicable_scope", "demo")
    ok, failed = 0, 0
    for case in cases:
        doc_id = case["id"]
        fmt = case.get("format", "txt").lower()
        filename = case.get("filename") or f"{doc_id}.txt"
        if fmt in ("txt", "md"):
            data = case["content"].encode("utf-8")
            content_type = "text/markdown; charset=utf-8" if fmt == "md" else "text/plain; charset=utf-8"
        elif fmt == "docx":
            data = build_docx(case["content"])
            content_type = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
        elif fmt in ("pdf", "pdf-scan", "png"):
            ext = "pdf" if fmt in ("pdf", "pdf-scan") else "png"
            gen_path = args.generated_dir / f"{doc_id}.{ext}"
            if not gen_path.exists():
                failed += 1
                print(f"[load] {doc_id}: missing rendered file {gen_path} — run generate-corpus-files.py first")
                continue
            data = gen_path.read_bytes()
            content_type = "application/pdf" if fmt in ("pdf", "pdf-scan") else "image/png"
        else:
            data = case["content"].encode("utf-8")
            content_type = "text/plain; charset=utf-8"
        governance = {f: str(case[f]) for f in GOVERNANCE_FIELDS if case.get(f)}
        body = multipart_body(
            filename,
            content_type,
            data,
            doc_id,
            tenant_id,
            case.get("permission", "internal"),
            case.get("knowledge_base_id", knowledge_base_id),
            case.get("applicable_scope", applicable_scope),
            governance,
        )
        req = request.Request(f"{base}/v1/upload", data=body, method="POST")
        req.add_header("Authorization", f"Bearer {token}")
        req.add_header("Content-Type", "multipart/form-data; boundary=----load-corpus-boundary")
        try:
            if args.dry_run:
                gov = f" governance={governance}" if governance else ""
                print(f"[load] dry-run: {doc_id} ({fmt}, {filename}, {len(data)} bytes){gov}")
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
