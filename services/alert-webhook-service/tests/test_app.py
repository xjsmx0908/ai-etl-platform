import base64
import hashlib
import hmac
import json
import threading
import unittest
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from app import AlertRelay, RelayConfig, build_handler, dingtalk_webhook_url


ALERT_PAYLOAD = {
    "status": "firing",
    "alerts": [
        {
            "status": "firing",
            "labels": {
                "alertname": "LLMConsecutiveFailures",
                "severity": "critical",
                "instance": "query-api:8080",
            },
            "annotations": {"summary": "LLM failed five consecutive requests"},
            "startsAt": "2026-07-13T00:00:00Z",
        }
    ],
}


class RecordingServer:
    def __init__(self):
        self.requests = []
        requests = self.requests

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = self.rfile.read(int(self.headers["Content-Length"]))
                requests.append((self.path, json.loads(body)))
                self.send_response(200)
                self.end_headers()
                self.wfile.write(b'{"errcode":0}')

            def log_message(self, *_args):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    @property
    def url(self):
        host, port = self.server.server_address
        return f"http://{host}:{port}/robot"

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *_args):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()


class AlertRelayTests(unittest.TestCase):
    def test_delivers_channel_specific_payloads(self):
        with RecordingServer() as wecom, RecordingServer() as dingtalk:
            relay = AlertRelay(
                RelayConfig(
                    token="relay-token",
                    wecom_webhook_url=wecom.url,
                    dingtalk_webhook_url=dingtalk.url,
                    dingtalk_secret="secret",
                )
            )

            relay.deliver(ALERT_PAYLOAD)

            self.assertEqual(wecom.requests[0][1]["msgtype"], "markdown")
            self.assertIn("LLMConsecutiveFailures", wecom.requests[0][1]["markdown"]["content"])
            self.assertEqual(dingtalk.requests[0][1]["msgtype"], "markdown")
            self.assertIn("timestamp=", dingtalk.requests[0][0])
            self.assertIn("sign=", dingtalk.requests[0][0])

    def test_dingtalk_signature_matches_documented_algorithm(self):
        timestamp = 1_700_000_000_000
        url = dingtalk_webhook_url("https://example.test/robot?access_token=abc", "secret", timestamp)

        query = urllib.parse.parse_qs(urllib.parse.urlparse(url).query)
        expected = base64.b64encode(
            hmac.new(
                b"secret",
                f"{timestamp}\nsecret".encode(),
                digestmod=hashlib.sha256,
            ).digest()
        ).decode()
        self.assertEqual(query["timestamp"], [str(timestamp)])
        self.assertEqual(query["sign"], [expected])

    def test_http_handler_rejects_invalid_bearer_token(self):
        relay = AlertRelay(RelayConfig(token="relay-token"))
        server = ThreadingHTTPServer(("127.0.0.1", 0), build_handler(relay))
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        host, port = server.server_address
        request = urllib.request.Request(
            f"http://{host}:{port}/alerts",
            data=json.dumps(ALERT_PAYLOAD).encode(),
            headers={
                "Authorization": "Bearer wrong-token",
                "Content-Type": "application/json",
            },
            method="POST",
        )
        try:
            with self.assertRaises(urllib.error.HTTPError) as raised:
                urllib.request.urlopen(request)
            self.assertEqual(raised.exception.code, 401)
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


if __name__ == "__main__":
    unittest.main()
