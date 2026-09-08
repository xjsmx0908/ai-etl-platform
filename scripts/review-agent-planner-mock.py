#!/usr/bin/env python3
"""Deterministic Review Agent planner mock with an explicit hold window."""

from __future__ import annotations

import argparse
import json
import os
import re
import socketserver
import time
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from typing import Any


REVIEW_TOOLS = (
    "get_review_context",
    "get_exact_candidate_chunks",
    "scan_sensitive_data",
    "scan_prompt_injection",
)
TOOL_NAME_RE = re.compile(
    r'"tool_name"\s*:\s*"(' + "|".join(REVIEW_TOOLS) + r')"'
)


def completed_review_tools(prompt: str) -> list[str]:
    found: list[str] = []
    seen: set[str] = set()
    for match in TOOL_NAME_RE.finditer(prompt or ""):
        name = match.group(1)
        if name in seen:
            continue
        seen.add(name)
        found.append(name)
    return found


def next_review_decision(prompt: str) -> str:
    completed = set(completed_review_tools(prompt))
    for name in REVIEW_TOOLS:
        if name in completed:
            continue
        arguments: dict[str, Any] = {}
        if name == "get_exact_candidate_chunks":
            arguments = {"offset": 0, "limit": 20}
        return json.dumps({"type": "tool_call", "tool_name": name, "arguments": arguments}, ensure_ascii=False)
    report = {
        "status": "completed",
        "recommendation": "publish",
        "risk_level": "low",
        "summary": "reviewed",
        "findings": [],
    }
    return json.dumps({"type": "final", "final": json.dumps(report, ensure_ascii=False, separators=(",", ":"))}, ensure_ascii=False)


def wait_for_hold_release(hold_path: Path | None, timeout: float) -> None:
    if hold_path is None:
        return
    deadline = time.monotonic() + timeout
    while hold_path.exists():
        if time.monotonic() >= deadline:
            raise TimeoutError(f"planner hold {hold_path} was not released")
        time.sleep(0.2)


def extract_user_prompt(body: dict[str, Any]) -> str:
    messages = body.get("messages")
    if isinstance(messages, list):
        for message in reversed(messages):
            if isinstance(message, dict) and str(message.get("role", "")) == "user":
                return str(message.get("content", ""))
    return json.dumps(body, ensure_ascii=False)


class PlannerHandler(BaseHTTPRequestHandler):
    server: "PlannerServer"

    def log_message(self, fmt: str, *args: Any) -> None:  # noqa: A003
        return

    def _write_json(self, status: int, payload: dict[str, Any]) -> None:
        raw = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self) -> None:  # noqa: N802
        if self.path in {"/healthz", "/resume"}:
            if self.path == "/resume" and self.server.hold_path is not None:
                try:
                    self.server.hold_path.unlink(missing_ok=True)
                except OSError:
                    pass
            self._write_json(200, {"status": "ok"})
            return
        self._write_json(404, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802
        length = int(self.headers.get("Content-Length", "0"))
        raw = self.rfile.read(length) if length > 0 else b"{}"
        try:
            body = json.loads(raw.decode("utf-8"))
        except json.JSONDecodeError:
            self._write_json(400, {"error": "invalid json"})
            return
        if not self.path.endswith("/chat/completions"):
            self._write_json(404, {"error": "not found"})
            return
        prompt = extract_user_prompt(body if isinstance(body, dict) else {})
        completed = completed_review_tools(prompt)
        if len(completed) >= self.server.hold_after_tools:
            try:
                wait_for_hold_release(self.server.hold_path, self.server.hold_timeout)
            except TimeoutError as exc:
                self._write_json(503, {"error": str(exc)})
                return
        content = next_review_decision(prompt)
        self._write_json(
            200,
            {
                "choices": [{"message": {"content": content}}],
                "usage": {"prompt_tokens": 8, "completion_tokens": 8, "total_tokens": 16},
            },
        )


class PlannerServer(socketserver.ThreadingMixIn, HTTPServer):
    daemon_threads = True
    hold_path: Path | None = None
    hold_after_tools: int = 1
    hold_timeout: float = 300.0


def main() -> None:
    parser = argparse.ArgumentParser(description="Review Agent planner mock")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=18081)
    parser.add_argument("--hold-path", default=os.environ.get("REVIEW_PLANNER_HOLD_PATH", ""))
    parser.add_argument("--hold-after-tools", type=int, default=1)
    parser.add_argument("--hold-timeout", type=float, default=300)
    args = parser.parse_args()
    server = PlannerServer((args.host, args.port), PlannerHandler)
    hold = Path(args.hold_path) if str(args.hold_path).strip() else None
    if hold is not None:
        hold.parent.mkdir(parents=True, exist_ok=True)
    server.hold_path = hold
    server.hold_after_tools = max(0, args.hold_after_tools)
    server.hold_timeout = args.hold_timeout
    print(f"review-agent-planner-mock listening on {args.host}:{args.port}")
    server.serve_forever()


if __name__ == "__main__":
    main()
