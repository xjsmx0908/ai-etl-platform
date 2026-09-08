import base64
import hashlib
import hmac
import json
import logging
import os
import time
import urllib.parse
import urllib.request
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any


LOGGER = logging.getLogger("alert-webhook-service")
MAX_BODY_BYTES = 1024 * 1024


class RelayError(RuntimeError):
    pass


def read_secret(name: str) -> str:
    file_path = os.getenv(f"{name}_FILE", "").strip()
    if file_path:
        try:
            return Path(file_path).read_text(encoding="utf-8").strip()
        except FileNotFoundError:
            LOGGER.warning("secret file is missing", extra={"secret_name": name})
            return ""
    return os.getenv(name, "").strip()


@dataclass(frozen=True)
class RelayConfig:
    token: str
    wecom_webhook_url: str = ""
    dingtalk_webhook_url: str = ""
    dingtalk_secret: str = ""
    request_timeout_seconds: float = 5.0

    @classmethod
    def from_env(cls) -> "RelayConfig":
        return cls(
            token=read_secret("ALERT_WEBHOOK_TOKEN"),
            wecom_webhook_url=read_secret("WECOM_WEBHOOK_URL"),
            dingtalk_webhook_url=read_secret("DINGTALK_WEBHOOK_URL"),
            dingtalk_secret=read_secret("DINGTALK_SECRET"),
            request_timeout_seconds=float(os.getenv("ALERT_DELIVERY_TIMEOUT_SECONDS", "5")),
        )


class AlertRelay:
    def __init__(self, config: RelayConfig):
        self.config = config

    def deliver(self, payload: dict[str, Any]) -> None:
        self._deliver_markdown(format_alerts(payload), alert_title(payload))

    def deliver_notification(self, payload: dict[str, Any]) -> None:
        self._deliver_markdown(format_notification(payload), notification_title(payload), allow_skip=True)

    def _deliver_markdown(self, content: str, title: str, allow_skip: bool = False) -> None:
        deliveries = []
        if self.config.wecom_webhook_url:
            deliveries.append(
                (
                    "wecom",
                    self.config.wecom_webhook_url,
                    {"msgtype": "markdown", "markdown": {"content": content}},
                )
            )
        if self.config.dingtalk_webhook_url:
            deliveries.append(
                (
                    "dingtalk",
                    dingtalk_webhook_url(
                        self.config.dingtalk_webhook_url,
                        self.config.dingtalk_secret,
                    ),
                    {"msgtype": "markdown", "markdown": {"title": title, "text": content}},
                )
            )
        if not deliveries:
            if allow_skip:
                LOGGER.warning("no enterprise notification channel is configured; skipping governance notification")
                return
            raise RelayError("no enterprise notification channel is configured")

        failures = []
        for channel, url, body in deliveries:
            try:
                send_json(url, body, self.config.request_timeout_seconds)
            except Exception as exc:
                LOGGER.error("alert delivery failed", extra={"channel": channel, "error": str(exc)})
                failures.append(f"{channel}: {exc}")
        if failures:
            raise RelayError("; ".join(failures))


def alert_title(payload: dict[str, Any]) -> str:
    alerts = payload.get("alerts") or []
    if not alerts:
        return "AI ETL Platform Alert"
    labels = alerts[0].get("labels") or {}
    return str(labels.get("alertname") or "AI ETL Platform Alert")


def format_alerts(payload: dict[str, Any]) -> str:
    status = str(payload.get("status") or "unknown").upper()
    lines = [f"### [{status}] AI ETL Platform"]
    for alert in (payload.get("alerts") or [])[:10]:
        labels = alert.get("labels") or {}
        annotations = alert.get("annotations") or {}
        lines.extend(
            [
                f"**{labels.get('alertname', 'UnknownAlert')}**",
                f"- Severity: {labels.get('severity', 'unknown')}",
                f"- Summary: {annotations.get('summary', 'No summary')}",
                f"- Instance: {labels.get('instance', 'unknown')}",
                f"- Started: {alert.get('startsAt', 'unknown')}",
            ]
        )
    return "\n\n".join(lines)[:3500]



NOTIFICATION_PAYLOAD_KEYS = (
    "document_id",
    "request_id",
    "state",
    "risk_level",
    "recommendation",
    "required_approvals",
    "approved_decisions",
    "approver_group_id",
    "decision",
    "decided_by",
    "public_path",
)


def notification_title(payload: dict[str, Any]) -> str:
    event_type = str(payload.get("event_type") or "governance.notification")
    return f"AI ETL {event_type}"


def format_notification(payload: dict[str, Any]) -> str:
    event_type = str(payload.get("event_type") or "governance.notification")
    tenant_id = str(payload.get("tenant_id") or "unknown")
    fields = payload.get("payload") if isinstance(payload.get("payload"), dict) else {}
    lines = [
        f"### [GOVERNANCE] {event_type}",
        f"- Tenant: {tenant_id}",
        f"- Event: {payload.get('event_id', 'unknown')}",
    ]
    for key in NOTIFICATION_PAYLOAD_KEYS:
        value = fields.get(key)
        if value:
            lines.append(f"- {key}: {value}")
    return "\n".join(lines)[:3500]




def dingtalk_webhook_url(url: str, secret: str, timestamp_ms: int | None = None) -> str:
    if not secret:
        return url
    timestamp_ms = timestamp_ms or int(time.time() * 1000)
    string_to_sign = f"{timestamp_ms}\n{secret}".encode()
    signature = base64.b64encode(
        hmac.new(secret.encode(), string_to_sign, digestmod=hashlib.sha256).digest()
    ).decode()
    separator = "&" if "?" in url else "?"
    return f"{url}{separator}{urllib.parse.urlencode({'timestamp': timestamp_ms, 'sign': signature})}"


def send_json(url: str, body: dict[str, Any], timeout_seconds: float) -> None:
    request = urllib.request.Request(
        url,
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "User-Agent": "ai-etl-alert-relay/1.0"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=timeout_seconds) as response:
        response_body = response.read(64 * 1024)
    if not response_body:
        return
    try:
        result = json.loads(response_body)
    except json.JSONDecodeError:
        return
    if result.get("errcode", 0) != 0:
        raise RelayError(f"downstream rejected alert: {result.get('errmsg', 'unknown error')}")


def build_handler(relay: AlertRelay):
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path != "/healthz":
                self.send_error(404)
                return
            self._write_json(200, {"status": "ok"})

        def do_POST(self):
            if self.path not in {"/alerts", "/notifications"}:
                self.send_error(404)
                return
            if not authorized(self.headers.get("Authorization", ""), relay.config.token):
                self._write_json(401, {"error": "unauthorized"})
                return
            try:
                content_length = int(self.headers.get("Content-Length", "0"))
            except ValueError:
                self._write_json(400, {"error": "invalid content length"})
                return
            if content_length <= 0 or content_length > MAX_BODY_BYTES:
                self._write_json(413, {"error": "invalid body size"})
                return
            try:
                payload = json.loads(self.rfile.read(content_length))
            except (json.JSONDecodeError, UnicodeDecodeError):
                self._write_json(400, {"error": "invalid JSON"})
                return
            if self.path == "/notifications":
                if not isinstance(payload, dict):
                    self._write_json(400, {"error": "invalid notification payload"})
                    return
                try:
                    relay.deliver_notification(payload)
                except RelayError as exc:
                    LOGGER.error("notification relay failed", extra={"error": str(exc)})
                    self._write_json(503, {"error": "notification delivery failed"})
                    return
                self._write_json(202, {"status": "accepted"})
                return
            if not isinstance(payload, dict) or not isinstance(payload.get("alerts"), list):
                self._write_json(400, {"error": "invalid Alertmanager payload"})
                return
            try:
                relay.deliver(payload)
            except RelayError as exc:
                LOGGER.error("alert relay failed", extra={"error": str(exc)})
                self._write_json(503, {"error": "alert delivery failed"})
                return
            self._write_json(202, {"status": "accepted"})

        def _write_json(self, status: int, body: dict[str, Any]):
            data = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)

        def log_message(self, message, *args):
            LOGGER.info(message, *args)

    return Handler


def authorized(header: str, expected_token: str) -> bool:
    if not expected_token or not header.startswith("Bearer "):
        return False
    return hmac.compare_digest(header.removeprefix("Bearer ").strip(), expected_token)


def main() -> None:
    logging.basicConfig(level=os.getenv("LOG_LEVEL", "INFO"))
    config = RelayConfig.from_env()
    if not config.token:
        raise SystemExit("ALERT_WEBHOOK_TOKEN or ALERT_WEBHOOK_TOKEN_FILE is required")
    host = os.getenv("HOST", "0.0.0.0")
    port = int(os.getenv("PORT", "8092"))
    server = ThreadingHTTPServer((host, port), build_handler(AlertRelay(config)))
    LOGGER.info("alert webhook service listening on %s:%d", host, port)
    server.serve_forever()


if __name__ == "__main__":
    main()
