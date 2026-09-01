import http.client
import json
import os
import socket
import subprocess
import threading
import time
import unittest
from collections import deque
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlsplit


ROOT = Path(__file__).resolve().parents[2]
WEB = ROOT / "web"


def unused_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class MockBackendHandler(BaseHTTPRequestHandler):
    responses = deque()
    requests = []

    def do_POST(self):
        length = int(self.headers.get("content-length", "0"))
        body = self.rfile.read(length)
        type(self).requests.append((self.path, self.headers, json.loads(body or b"{}")))
        status, payload = type(self).responses.popleft()
        encoded = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def respond_without_body(self):
        type(self).requests.append((self.path, self.headers, None))
        status, payload = type(self).responses.popleft()
        encoded = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def do_GET(self):
        self.respond_without_body()

    def do_DELETE(self):
        self.respond_without_body()

    def log_message(self, _format, *_args):
        return


@unittest.skipUnless(
    os.environ.get("RUN_WEB_HTTP_CONTRACT") == "1",
    "set RUN_WEB_HTTP_CONTRACT=1 after building web/.next/standalone",
)
class ReauthenticationWebContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        server_file = WEB / ".next" / "standalone" / "server.js"
        if not server_file.exists():
            raise unittest.SkipTest("run npm run build before the HTTP contract tests")
        cls.backend = ThreadingHTTPServer(("127.0.0.1", 0), MockBackendHandler)
        cls.backend_thread = threading.Thread(target=cls.backend.serve_forever, daemon=True)
        cls.backend_thread.start()
        cls.web_port, cls.web = cls.start_web(server_file, session_core_enabled=True)
        cls.legacy_web_port, cls.legacy_web = cls.start_web(server_file, session_core_enabled=False)
        cls.unavailable_web_port, cls.unavailable_web = cls.start_web(
            server_file, session_core_enabled=True, backend_url=f"http://127.0.0.1:{unused_port()}",
        )

    @classmethod
    def start_web(cls, server_file, *, session_core_enabled, backend_url=None):
        web_port = unused_port()
        environment = os.environ.copy()
        environment.update({
            "BACKEND_URL": backend_url or f"http://127.0.0.1:{cls.backend.server_port}",
            "HOSTNAME": "127.0.0.1",
            "PORT": str(web_port),
            "SESSION_CORE_ENABLED": "true" if session_core_enabled else "false",
            "COOKIE_SECURE": "true",
        })
        web = subprocess.Popen(
            ["node", str(server_file)], cwd=WEB, env=environment,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
        )
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if web.poll() is not None:
                output = web.stdout.read() if web.stdout else ""
                raise RuntimeError(f"Next server exited before readiness:\n{output}")
            try:
                connection = http.client.HTTPConnection("127.0.0.1", web_port, timeout=1)
                connection.request("GET", "/login")
                response = connection.getresponse()
                response.read()
                connection.close()
                break
            except OSError:
                time.sleep(0.1)
        else:
            raise RuntimeError("Next server did not become ready")
        return web_port, web

    @classmethod
    def tearDownClass(cls):
        for web in (cls.web, cls.legacy_web, cls.unavailable_web):
            web.terminate()
            try:
                web.wait(timeout=5)
            except subprocess.TimeoutExpired:
                web.kill()
        cls.backend.shutdown()
        cls.backend.server_close()

    def setUp(self):
        MockBackendHandler.responses.clear()
        MockBackendHandler.requests.clear()

    def request(self, method, path, *, body=None, cookie=None, same_origin=True, legacy=False, unavailable=False):
        headers = {"Host": "rag.example.test", "X-Forwarded-Proto": "https"}
        if body is not None:
            headers["Content-Type"] = "application/json"
            body = json.dumps(body)
        if cookie:
            headers["Cookie"] = cookie
        if same_origin is not None:
            headers["Origin"] = "https://rag.example.test" if same_origin else "https://attacker.example"
            headers["Sec-Fetch-Site"] = "same-origin" if same_origin else "cross-site"
        port = self.unavailable_web_port if unavailable else (self.legacy_web_port if legacy else self.web_port)
        connection = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
        connection.request(method, path, body=body, headers=headers)
        response = connection.getresponse()
        payload = response.read()
        result = (response.status, response.getheaders(), payload)
        connection.close()
        return result

    @staticmethod
    def cookies(headers):
        return [value for name, value in headers if name.lower() == "set-cookie"]

    def test_start_rejects_cross_site_without_contacting_backend(self):
        status, headers, _ = self.request(
            "POST", "/api/auth/reauth/start",
            body={"action": "identity.binding.change", "return_to": "/users"},
            cookie="ai_etl_token=ps1_old", same_origin=False,
        )
        self.assertEqual(403, status)
        self.assertEqual([], MockBackendHandler.requests)
        self.assertEqual([], self.cookies(headers))

    def test_start_sets_scoped_secure_state_cookie_from_valid_backend_response(self):
        MockBackendHandler.responses.append(
            (200, {"authorization_url": "https://idp.example/authorize", "state": "state-1", "expires_in": 300})
        )
        status, headers, payload = self.request(
            "POST", "/api/auth/reauth/start",
            body={"action": "identity.binding.change", "return_to": "/users"},
            cookie="ai_etl_token=ps1_old",
        )
        self.assertEqual(200, status)
        self.assertEqual({"authorization_url": "https://idp.example/authorize"}, json.loads(payload))
        cookie = self.cookies(headers)
        self.assertEqual(1, len(cookie))
        for attribute in (
            "ai_etl_reauth_state=state-1", "Path=/api/auth/oidc/callback", "Max-Age=300",
            "HttpOnly", "Secure", "SameSite=lax",
        ):
            self.assertIn(attribute, cookie[0])

    def test_start_rejects_malformed_backend_response_without_setting_cookie(self):
        MockBackendHandler.responses.append(
            (200, {"authorization_url": "http://idp.example/authorize", "state": "state-1", "expires_in": 901})
        )
        status, headers, _ = self.request(
            "POST", "/api/auth/reauth/start",
            body={"action": "identity.binding.change", "return_to": "/users"},
            cookie="ai_etl_token=ps1_old",
        )
        self.assertEqual(502, status)
        self.assertEqual([], self.cookies(headers))

    def test_successful_callback_rotates_cookie_and_uses_safe_return_path(self):
        MockBackendHandler.responses.append(
            (200, {"token": "ps1_new", "expires_at": "2099-01-01T00:00:00Z", "return_to": "/users?step=confirm"})
        )
        status, headers, _ = self.request(
            "GET", "/api/auth/oidc/callback?code=code-1&state=state-1",
            cookie="ai_etl_token=ps1_old; ai_etl_reauth_state=state-1", same_origin=None,
        )
        self.assertEqual(307, status)
        destination = urlsplit(dict(headers)["location"])
        self.assertEqual("/users", destination.path)
        self.assertEqual("step=confirm", destination.query)
        cookies = self.cookies(headers)
        self.assertTrue(any("ai_etl_token=ps1_new" in value and "Max-Age=1800" in value for value in cookies))
        self.assertTrue(any("ai_etl_reauth_state=" in value and "Max-Age=0" in value for value in cookies))

    def test_failed_or_malformed_callback_clears_only_reauth_state(self):
        for backend_response in ((503, {"error": "unavailable"}), (200, {"token": "not-a-session"})):
            with self.subTest(backend_response=backend_response):
                MockBackendHandler.responses.append(backend_response)
                status, headers, _ = self.request(
                    "GET", "/api/auth/oidc/callback?code=code-1&state=state-1",
                    cookie="ai_etl_token=ps1_old; ai_etl_reauth_state=state-1", same_origin=None,
                )
                self.assertEqual(307, status)
                cookies = self.cookies(headers)
                self.assertTrue(any("ai_etl_reauth_state=" in value and "Max-Age=0" in value for value in cookies))
                self.assertFalse(any(value.startswith("ai_etl_token=") for value in cookies))

    def test_stale_reauth_cookie_does_not_hijack_ordinary_oidc_callback(self):
        MockBackendHandler.responses.append(
            (200, {"token": "ps1_login", "expires_at": "2099-01-01T00:00:00Z", "return_to": "/documents"})
        )
        status, headers, _ = self.request(
            "GET", "/api/auth/oidc/callback?code=login-code&state=login-state",
            cookie="ai_etl_reauth_state=abandoned-state; ai_etl_oidc_state=login-state", same_origin=None,
        )
        self.assertEqual(307, status)
        self.assertEqual("/v1/auth/oidc/callback", MockBackendHandler.requests[0][0])
        cookies = self.cookies(headers)
        session_cookie = next(value for value in cookies if value.startswith("ai_etl_token=ps1_login"))
        for attribute in ("Path=/", "Max-Age=1800", "HttpOnly", "Secure", "SameSite=lax"):
            self.assertIn(attribute, session_cookie)

    def test_password_login_sets_bounded_platform_session_cookie(self):
        MockBackendHandler.responses.append(
            (200, {"token": "ps1_password", "expires_at": "2099-01-01T00:00:00Z", "user": {"id": "user-1"}})
        )
        status, headers, payload = self.request(
            "POST", "/api/auth/login", body={"username": "alice", "password": "secret"}, same_origin=None,
        )
        self.assertEqual(200, status)
        self.assertEqual({"id": "user-1"}, json.loads(payload)["user"])
        cookies = self.cookies(headers)
        self.assertEqual(1, len(cookies))
        for attribute in ("ai_etl_token=ps1_password", "Path=/", "Max-Age=1800", "HttpOnly", "Secure", "SameSite=lax"):
            self.assertIn(attribute, cookies[0])

    def test_login_routes_reject_non_session_or_expired_response_without_cookie(self):
        cases = (
            ("POST", "/api/auth/login", {"token": "legacy-jwt", "expires_at": "2099-01-01T00:00:00Z"}, None),
            ("GET", "/api/auth/oidc/callback?code=code-1&state=oidc-state", {"token": "ps1_expired", "expires_at": "2000-01-01T00:00:00Z"}, "ai_etl_oidc_state=oidc-state"),
        )
        for method, path, backend_payload, cookie in cases:
            with self.subTest(path=path):
                MockBackendHandler.responses.append((200, backend_payload))
                body = {"username": "alice", "password": "secret"} if method == "POST" else None
                status, headers, _ = self.request(method, path, body=body, cookie=cookie, same_origin=None)
                self.assertIn(status, (307, 502))
                self.assertFalse(any(value.startswith("ai_etl_token=") for value in self.cookies(headers)))

    def test_default_disabled_keeps_password_and_oidc_jwt_cookies(self):
        cases = (
            ("POST", "/api/auth/login", {"token": "legacy-password-jwt", "expires_at": "2099-01-01T00:00:00Z", "user": {"id": "user-1"}}, None),
            ("GET", "/api/auth/oidc/callback?code=code-1&state=oidc-state", {"token": "legacy-oidc-jwt", "expires_at": "2099-01-01T00:00:00Z", "return_to": "/"}, "ai_etl_oidc_state=oidc-state"),
        )
        for method, path, backend_payload, cookie in cases:
            with self.subTest(path=path):
                MockBackendHandler.responses.append((200, backend_payload))
                body = {"username": "alice", "password": "secret"} if method == "POST" else None
                status, headers, _ = self.request(
                    method, path, body=body, cookie=cookie, same_origin=None, legacy=True,
                )
                self.assertIn(status, (200, 307))
                session_cookie = next(value for value in self.cookies(headers) if value.startswith("ai_etl_token="))
                for attribute in ("Path=/", "Max-Age=86400", "HttpOnly", "Secure", "SameSite=lax"):
                    self.assertIn(attribute, session_cookie)

    def test_logout_rejects_cross_site_and_preserves_cookie_on_backend_failure(self):
        status, headers, _ = self.request(
            "POST", "/api/auth/logout", cookie="ai_etl_token=ps1_old", same_origin=False,
        )
        self.assertEqual(403, status)
        self.assertEqual([], MockBackendHandler.requests)
        self.assertFalse(any(value.startswith("ai_etl_token=") for value in self.cookies(headers)))

        MockBackendHandler.responses.append((503, {"error": "logout unavailable"}))
        status, headers, _ = self.request(
            "POST", "/api/auth/logout", cookie="ai_etl_token=ps1_old",
        )
        self.assertEqual(503, status)
        self.assertEqual("Bearer ps1_old", MockBackendHandler.requests[0][1].get("authorization"))
        self.assertFalse(any(value.startswith("ai_etl_token=") for value in self.cookies(headers)))

    def test_local_logout_clears_cookie_only_after_backend_success(self):
        MockBackendHandler.responses.append((204, b""))
        status, headers, _ = self.request(
            "POST", "/api/auth/logout", cookie="ai_etl_token=ps1_old",
        )
        self.assertEqual(204, status)
        cookie = next(value for value in self.cookies(headers) if value.startswith("ai_etl_token="))
        for attribute in ("Path=/", "Max-Age=0", "HttpOnly", "Secure", "SameSite=lax"):
            self.assertIn(attribute, cookie)

    def test_federated_logout_clears_session_and_sets_scoped_state_cookie(self):
        MockBackendHandler.responses.append((200, {
            "authorization_url": "https://idp.example/logout?state=logout-state",
            "state": "logout-state", "expires_in": 300,
        }))
        status, headers, payload = self.request(
            "POST", "/api/auth/logout?return_to=/documents", cookie="ai_etl_token=ps1_old",
        )
        self.assertEqual(200, status)
        self.assertEqual("https://idp.example/logout?state=logout-state", json.loads(payload)["authorization_url"])
        cookies = self.cookies(headers)
        self.assertTrue(any(value.startswith("ai_etl_token=") and "Max-Age=0" in value for value in cookies))
        state_cookie = next(value for value in cookies if value.startswith("ai_etl_logout_state="))
        for attribute in (
            "Path=/api/auth/logout/callback", "Max-Age=300", "HttpOnly", "Secure", "SameSite=lax",
        ):
            self.assertIn(attribute, state_cookie)

    def test_malformed_optional_provider_logout_still_completes_local_logout(self):
        MockBackendHandler.responses.append((200, {
            "authorization_url": "http://unsafe.example/logout",
            "state": "logout-state", "expires_in": 300,
        }))
        status, headers, _ = self.request(
            "POST", "/api/auth/logout", cookie="ai_etl_token=ps1_old",
        )
        self.assertEqual(204, status)
        cookies = self.cookies(headers)
        self.assertTrue(any(value.startswith("ai_etl_token=") and "Max-Age=0" in value for value in cookies))
        self.assertFalse(any(value.startswith("ai_etl_logout_state=") for value in cookies))

    def test_logout_callback_uses_state_and_clears_it_on_success_or_failure(self):
        for backend_response, expected_location in (
            ((200, {"return_to": "/documents"}), "/documents"),
            ((401, {"error": "logout callback failed"}), "/login?error=logout_callback_failed"),
        ):
            with self.subTest(backend_response=backend_response):
                MockBackendHandler.responses.append(backend_response)
                status, headers, _ = self.request(
                    "GET", "/api/auth/logout/callback?state=logout-state",
                    cookie="ai_etl_logout_state=logout-state", same_origin=None,
                )
                self.assertEqual(307, status)
                self.assertEqual(expected_location, urlsplit(dict(headers)["location"]).path + (
                    "?" + urlsplit(dict(headers)["location"]).query if urlsplit(dict(headers)["location"]).query else ""
                ))
                self.assertTrue(any(
                    value.startswith("ai_etl_logout_state=") and "Max-Age=0" in value
                    for value in self.cookies(headers)
                ))

    def test_session_management_lists_using_only_the_http_only_cookie(self):
        backend_payload = {"sessions": [{
            "handle": "sm1_11111111-1111-4111-8111-111111111111",
            "authentication_method": "federated",
            "created_at": "2026-09-01T12:00:00Z",
            "last_activity_at": "2026-09-01T12:01:00Z",
            "expires_at": "2026-09-01T20:00:00Z",
            "current": True,
        }]}
        MockBackendHandler.responses.append((200, backend_payload))
        status, headers, payload = self.request(
            "GET", "/api/auth/sessions", cookie="ai_etl_token=ps1_current", same_origin=None,
        )
        self.assertEqual(200, status)
        self.assertEqual(backend_payload, json.loads(payload))
        self.assertEqual("/v1/auth/sessions", MockBackendHandler.requests[0][0])
        self.assertEqual("Bearer ps1_current", MockBackendHandler.requests[0][1].get("authorization"))
        self.assertFalse(any(value.startswith("ai_etl_token=") for value in self.cookies(headers)))

    def test_session_management_revoke_requires_same_origin_and_forwards_encoded_handle(self):
        path = "/api/auth/sessions/sm1_11111111-1111-4111-8111-111111111111"
        status, _, _ = self.request(
            "DELETE", path, cookie="ai_etl_token=ps1_current", same_origin=False,
        )
        self.assertEqual(403, status)
        self.assertEqual([], MockBackendHandler.requests)

        MockBackendHandler.responses.append((204, b""))
        status, _, payload = self.request(
            "DELETE", path, cookie="ai_etl_token=ps1_current",
        )
        self.assertEqual(204, status)
        self.assertEqual(b"", payload)
        self.assertEqual(
            "/v1/auth/sessions/sm1_11111111-1111-4111-8111-111111111111",
            MockBackendHandler.requests[0][0],
        )
        self.assertEqual("Bearer ps1_current", MockBackendHandler.requests[0][1].get("authorization"))

    def test_session_management_returns_503_when_backend_is_unreachable(self):
        paths = (
            ("GET", "/api/auth/sessions"),
            ("DELETE", "/api/auth/sessions/sm1_11111111-1111-4111-8111-111111111111"),
        )
        for method, path in paths:
            with self.subTest(method=method):
                status, _, payload = self.request(
                    method, path, cookie="ai_etl_token=ps1_current", unavailable=True,
                )
                self.assertEqual(503, status)
                self.assertEqual({"error": "session management unavailable"}, json.loads(payload))


if __name__ == "__main__":
    unittest.main()
