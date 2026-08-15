#!/usr/bin/env python3
"""Generate demo JWT tokens for the web frontend (user + admin roles).

Signs HS256 tokens with the same claims the Go auth package expects
(`internal/auth.Claims`: tenant_id, user_id, permission, scopes, exp, iat).
Prints both tokens plus their expiry, ready to paste into `.env`
(`WEB_JWT` / `WEB_JWT_ADMIN`).

Usage:
  python3 scripts/gen-demo-token.py                      # reads JWT_SECRET from .env
  python3 scripts/gen-demo-token.py --secret <s> --ttl-days 90
  python3 scripts/gen-demo-token.py --tenant acme --ttl-days 7

After regenerating, update `web/.env` (and `web/.env.local` for dev) and rebuild
the web image so the new NEXT_PUBLIC_* values are baked in.
"""
from __future__ import annotations

import argparse
import base64
import hashlib
import hmac
import json
import re
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def b64url(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def sign(secret: str, claims: dict) -> str:
    header = b64url(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(",", ":")).encode())
    payload = b64url(json.dumps(claims, separators=(",", ":")).encode())
    sig = b64url(hmac.new(secret.encode(), f"{header}.{payload}".encode(), hashlib.sha256).digest())
    return f"{header}.{payload}.{sig}"


def read_secret_from_env() -> str:
    env = (ROOT / ".env")
    if env.exists():
        for line in env.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if line.startswith("JWT_SECRET=") and not line.startswith("#"):
                return line.split("=", 1)[1].strip()
    return ""


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--secret", default="", help="JWT secret (default: JWT_SECRET from .env)")
    ap.add_argument("--tenant", default="demo", help="tenant_id claim (default: demo)")
    ap.add_argument("--ttl-days", type=int, default=30, help="token lifetime in days (default: 30)")
    args = ap.parse_args()

    secret = args.secret or read_secret_from_env()
    if not secret:
        print("error: no JWT_SECRET found (pass --secret or set it in .env)", file=sys.stderr)
        return 1
    if len(secret) < 16:
        print("warning: JWT_SECRET looks too short; use a strong secret for real deployments", file=sys.stderr)

    now = int(time.time())
    exp = now + args.ttl_days * 86400
    expires = time.strftime("%Y-%m-%d %H:%M UTC", time.gmtime(exp))

    user = sign(secret, {
        "tenant_id": args.tenant, "user_id": "demo-user", "permission": "user",
        "scopes": ["query", "upload", "agent"], "exp": exp, "iat": now,
    })
    admin = sign(secret, {
        "tenant_id": args.tenant, "user_id": "demo-admin", "permission": "admin",
        "scopes": ["query"], "exp": exp, "iat": now,
    })

    print(f"# expires {expires} (ttl {args.ttl_days}d, tenant {args.tenant})")
    print(f"WEB_JWT={user}")
    print(f"WEB_JWT_ADMIN={admin}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
