#!/usr/bin/env python3
"""Exercise the personal-demo identity runtime through public HTTPS seams."""

import argparse
import base64
import hashlib
import hmac
import html.parser
import http.cookiejar
import json
import ssl
import struct
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path


WEB_ORIGIN = "https://ai-etl.localhost:3443"
ISSUER = "https://keycloak.localhost:8443/realms/ai-etl-demo"
DEMO_USER_ID = "11111111-2222-4333-8444-555555555555"
ALLOWED_REDIRECTS = {"ai-etl.localhost:3443", "keycloak.localhost:8443"}


class RestrictedRedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        parsed = urllib.parse.urlsplit(newurl)
        if parsed.scheme != "https" or parsed.netloc not in ALLOWED_REDIRECTS:
            raise RuntimeError(f"untrusted redirect target: {parsed.scheme}://{parsed.netloc}{parsed.path}")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


class FormParser(html.parser.HTMLParser):
    def __init__(self):
        super().__init__()
        self.forms = []
        self._form = None

    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        if tag == "form":
            self._form = {"action": values.get("action", ""), "fields": {}}
            self.forms.append(self._form)
        elif tag == "input" and self._form is not None and values.get("name"):
            self._form["fields"][values["name"]] = values.get("value", "")

    def handle_endtag(self, tag):
        if tag == "form":
            self._form = None


def load_form(body, required_field):
    parser = FormParser()
    parser.feed(body.decode("utf-8", errors="replace"))
    for form in parser.forms:
        if required_field in form["fields"]:
            return form
    raise RuntimeError(f"Keycloak form containing {required_field!r} was not found")


def totp(secret, now=None):
    secret_bytes = base64.b32decode(secret.strip().upper() + "=" * ((8 - len(secret.strip()) % 8) % 8))
    counter = int((time.time() if now is None else now) // 30)
    digest = hmac.new(secret_bytes, struct.pack(">Q", counter), hashlib.sha1).digest()
    offset = digest[-1] & 0x0F
    value = (struct.unpack(">I", digest[offset : offset + 4])[0] & 0x7FFFFFFF) % 1_000_000
    return f"{value:06d}"


def request(opener, url, method="GET", data=None, headers=None):
    encoded = None if data is None else json.dumps(data).encode("utf-8")
    request_headers = dict(headers or {})
    if encoded is not None:
        request_headers.setdefault("Content-Type", "application/json")
    req = urllib.request.Request(url, data=encoded, headers=request_headers, method=method)
    try:
        return opener.open(req, timeout=20)
    except urllib.error.HTTPError as error:
        detail = error.read(4096).decode("utf-8", errors="replace")
        raise RuntimeError(f"{method} {url} returned HTTP {error.code}: {detail}") from error


def submit_form(opener, form, values):
    fields = dict(form["fields"])
    fields.update(values)
    req = urllib.request.Request(
        form["action"],
        data=urllib.parse.urlencode(fields).encode("utf-8"),
        headers={"Content-Type": "application/x-www-form-urlencoded"},
        method="POST",
    )
    return opener.open(req, timeout=20)


def find_cookie(jar, name):
    return next((cookie for cookie in jar if cookie.name == name), None)


def run(asset_dir):
    context = ssl.create_default_context(cafile=str(asset_dir / "ca.pem"))
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(
        RestrictedRedirectHandler(),
        urllib.request.HTTPCookieProcessor(jar),
        urllib.request.HTTPSHandler(context=context),
    )

    scim_token = (asset_dir / "scim-bearer-token").read_text(encoding="utf-8").strip()
    password = (asset_dir / "demo-password").read_text(encoding="utf-8").strip()
    totp_secret = (asset_dir / "totp-secret").read_text(encoding="utf-8").strip()

    print("[identity-demo] provisioning fictional user through SCIM")
    with request(
        opener,
        f"{WEB_ORIGIN}/scim/v2/Users",
        method="POST",
        headers={"Authorization": f"Bearer {scim_token}", "X-Idempotency-Key": "pd2-demo-user-v1"},
        data={
            "schemas": ["urn:ietf:params:scim:schemas:core:2.0:User"],
            "userName": "demo.reader",
            "externalId": DEMO_USER_ID,
            "displayName": "Demo Reader",
            "active": True,
            "emails": [{"value": "demo.reader@example.invalid", "primary": True}],
        },
    ) as response:
        if response.status not in (200, 201):
            raise RuntimeError(f"SCIM provisioning returned {response.status}")

    print("[identity-demo] starting OIDC code flow through the Web BFF")
    with request(opener, f"{WEB_ORIGIN}/api/auth/oidc/start?return_to=%2F") as response:
        password_form = load_form(response.read(), "username")
    with submit_form(opener, password_form, {"username": "demo.reader", "password": password}) as response:
        otp_form = load_form(response.read(), "otp")
    with submit_form(opener, otp_form, {"otp": totp(totp_secret)}) as response:
        response.read()

    credential = find_cookie(jar, "ai_etl_token")
    if credential is None or not credential.value.startswith("ps1_"):
        raise RuntimeError("OIDC login did not establish a ps1_ Web session")
    if not credential.secure:
        raise RuntimeError("Web session cookie is not Secure")

    print("[identity-demo] verifying federated platform session")
    with request(opener, f"{WEB_ORIGIN}/api/auth/sessions") as response:
        sessions = json.load(response)
    resources = sessions.get("sessions") if isinstance(sessions, dict) else None
    if not isinstance(resources, list) or not any(
        item.get("current") is True and item.get("authentication_method") == "federated"
        for item in resources
    ):
        raise RuntimeError(f"federated current session was not returned: {sessions!r}")

    print("[identity-demo] revoking locally and completing RP-initiated logout")
    with request(
        opener,
        f"{WEB_ORIGIN}/api/auth/logout?return_to=%2Flogin",
        method="POST",
        headers={"Origin": WEB_ORIGIN, "Sec-Fetch-Site": "same-origin"},
    ) as response:
        logout = json.load(response) if response.status != 204 else {}
    if find_cookie(jar, "ai_etl_token") is not None:
        raise RuntimeError("local logout did not clear the Web session cookie")
    authorization_url = logout.get("authorization_url")
    if not isinstance(authorization_url, str) or not authorization_url.startswith(ISSUER + "/"):
        raise RuntimeError("federated logout did not return the trusted Keycloak endpoint")
    with request(opener, authorization_url) as response:
        response.read()

    try:
        request(opener, f"{WEB_ORIGIN}/api/auth/sessions")
    except RuntimeError as error:
        if "HTTP 401" not in str(error):
            raise
    else:
        raise RuntimeError("revoked session remained accessible after logout")
    print("[identity-demo] PASS: password + TOTP, ps1_ session, and logout verified over HTTPS")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--asset-dir", type=Path, required=True)
    args = parser.parse_args()
    run(args.asset_dir.resolve())


if __name__ == "__main__":
    main()
