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

from app import AlertRelay, RelayConfig, RelayError, build_handler, dingtalk_webhook_url, format_notification


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
    def __init__(self, status=200, response_body=b'{"errcode":0}'):
        self.requests = []
        self.auths = []
        requests = self.requests
        auths = self.auths
        status_code = status
        body_bytes = response_body

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = self.rfile.read(int(self.headers["Content-Length"]))
                requests.append((self.path, json.loads(body)))
                auths.append(self.headers.get("Authorization"))
                self.send_response(status_code)
                self.end_headers()
                self.wfile.write(body_bytes)

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


class GovernanceNotificationTests(unittest.TestCase):
    def test_format_notification_omits_findings_and_summary(self):
        content = format_notification({
            "event_id": "ntf-1",
            "event_type": "release.request.opened",
            "tenant_id": "acme",
            "payload": {
                "document_id": "doc-1",
                "state": "approval_pending",
                "summary": "身份证泄露",
                "findings": "secret",
                "public_path": "/agent?document=doc-1",
                "decision_path": "/v1/release-center/workflow/decision",
            },
        })
        self.assertIn("doc-1", content)
        self.assertIn("待审批", content)
        self.assertIn("处理入口", content)
        self.assertIn("知识发布待审批", content)
        self.assertNotIn("身份证", content)
        self.assertNotIn("secret", content)
        self.assertNotIn("findings", content)
        self.assertNotIn("decision_path", content)

    def test_delivers_wecom_and_workflow_engine(self):
        payload = {
            "event_id": "ntf-1",
            "event_type": "release.request.opened",
            "tenant_id": "acme",
            "payload": {
                "document_id": "doc-1",
                "state": "approval_pending",
                "decision_path": "/v1/release-center/workflow/decision",
                "summary": "身份证泄露",
            },
        }
        with RecordingServer() as wecom, RecordingServer() as workflow:
            relay = AlertRelay(
                RelayConfig(
                    token="relay-token",
                    wecom_webhook_url=wecom.url,
                    workflow_engine_webhook_url=workflow.url,
                    workflow_engine_webhook_token="engine-token",
                )
            )
            relay.deliver_notification(payload)
            self.assertIn("待审批", wecom.requests[0][1]["markdown"]["content"])
            self.assertNotIn("身份证", wecom.requests[0][1]["markdown"]["content"])
            self.assertEqual(workflow.requests[0][1]["event_type"], "release.request.opened")
            self.assertEqual(workflow.requests[0][1]["payload"]["decision_path"], "/v1/release-center/workflow/decision")
            self.assertEqual(workflow.auths[0], "Bearer engine-token")

    def test_workflow_failure_still_attempts_wecom(self):
        payload = {
            "event_id": "ntf-2",
            "event_type": "release.request.opened",
            "tenant_id": "acme",
            "payload": {"document_id": "doc-2", "state": "approval_pending"},
        }
        with RecordingServer() as wecom, RecordingServer(status=500) as workflow:
            relay = AlertRelay(
                RelayConfig(
                    token="relay-token",
                    wecom_webhook_url=wecom.url,
                    workflow_engine_webhook_url=workflow.url,
                )
            )
            with self.assertRaises(RelayError):
                relay.deliver_notification(payload)
            self.assertEqual(len(wecom.requests), 1)
            self.assertEqual(len(workflow.requests), 1)

    def test_notifications_endpoint_skips_when_no_channel(self):
        relay = AlertRelay(RelayConfig(token="relay-token"))
        server = ThreadingHTTPServer(("127.0.0.1", 0), build_handler(relay))
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        host, port = server.server_address
        request = urllib.request.Request(
            f"http://{host}:{port}/notifications",
            data=json.dumps({
                "event_id": "ntf-1",
                "event_type": "release.request.opened",
                "tenant_id": "acme",
                "payload": {"document_id": "doc-1", "state": "approval_pending"},
            }).encode(),
            headers={
                "Authorization": "Bearer relay-token",
                "Content-Type": "application/json",
            },
            method="POST",
        )
        try:
            with urllib.request.urlopen(request) as response:
                self.assertEqual(response.status, 202)
        finally:
            server.shutdown()
            server.server_close()
            thread.join()
