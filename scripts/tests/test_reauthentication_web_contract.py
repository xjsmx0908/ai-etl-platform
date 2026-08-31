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
        cls.web_port = unused_port()
        environment = os.environ.copy()
        environment.update(
            {
                "BACKEND_URL": f"http://127.0.0.1:{cls.backend.server_port}",
                "HOSTNAME": "127.0.0.1",
                "PORT": str(cls.web_port),
            }
        )
        cls.web = subprocess.Popen(
            ["node", str(server_file)],
            cwd=WEB,
            env=environment,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if cls.web.poll() is not None:
                output = cls.web.stdout.read() if cls.web.stdout else ""
                raise RuntimeError(f"Next server exited before readiness:\n{output}")
            try:
                connection = http.client.HTTPConnection("127.0.0.1", cls.web_port, timeout=1)
                connection.request("GET", "/login")
                response = connection.getresponse()
                response.read()
                connection.close()
                break
            except OSError:
                time.sleep(0.1)
        else:
            raise RuntimeError("Next server did not become ready")

    @classmethod
    def tearDownClass(cls):
        cls.web.terminate()
        try:
            cls.web.wait(timeout=5)
        except subprocess.TimeoutExpired:
            cls.web.kill()
        cls.backend.shutdown()
        cls.backend.server_close()

    def setUp(self):
        MockBackendHandler.responses.clear()
        MockBackendHandler.requests.clear()

    def request(self, method, path, *, body=None, cookie=None, same_origin=True):
        headers = {"Host": "rag.example.test", "X-Forwarded-Proto": "https"}
        if body is not None:
            headers["Content-Type"] = "application/json"
            body = json.dumps(body)
        if cookie:
            headers["Cookie"] = cookie
        if same_origin is not None:
            headers["Origin"] = "https://rag.example.test" if same_origin else "https://attacker.example"
            headers["Sec-Fetch-Site"] = "same-origin" if same_origin else "cross-site"
        connection = http.client.HTTPConnection("127.0.0.1", self.web_port, timeout=5)
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
        MockBackendHandler.responses.append((200, {"token": "login-token", "return_to": "/documents"}))
        status, headers, _ = self.request(
            "GET", "/api/auth/oidc/callback?code=login-code&state=login-state",
            cookie="ai_etl_reauth_state=abandoned-state; ai_etl_oidc_state=login-state", same_origin=None,
        )
        self.assertEqual(307, status)
        self.assertEqual("/v1/auth/oidc/callback", MockBackendHandler.requests[0][0])
        self.assertTrue(any(value.startswith("ai_etl_token=login-token") for value in self.cookies(headers)))


if __name__ == "__main__":
    unittest.main()
