#!/usr/bin/env python3
"""Tiny OpenAI-compatible mock server for local/CI smoke tests."""

from __future__ import annotations

import argparse
import json
import time
from http.server import BaseHTTPRequestHandler, HTTPServer
from typing import Any


class Handler(BaseHTTPRequestHandler):
    server: "MockServer"

    def _write_json(self, status: int, payload: dict[str, Any]) -> None:
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/healthz":
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

        if self.path.endswith("/v1/embeddings"):
            text = str(body.get("input", body.get("prompt", "")))
            tokens = max(1, len(text) // 2)
            embedding = [float(i + 1) / 1000.0 for i in range(self.server.embed_dim)]
            self._write_json(
                200,
                {
                    "data": [{"embedding": embedding}],
                    "usage": {"total_tokens": tokens},
                },
            )
            return

        if self.path.endswith("/v1/chat/completions"):
            self._write_json(
                200,
                {
                    "choices": [
                        {
                            "message": {
                                "content": f"mock answer at {int(time.time())}",
                            }
                        }
                    ]
                },
            )
            return

        self._write_json(404, {"error": "not found"})

    def log_message(self, fmt: str, *args: Any) -> None:  # noqa: A003
        # Keep CI logs clean.
        return


class MockServer(HTTPServer):
    embed_dim: int


def main() -> None:
    parser = argparse.ArgumentParser(description="Mock OpenAI-compatible server")
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=18080)
    parser.add_argument("--dim", type=int, default=8, help="embedding dimension")
    args = parser.parse_args()

    server = MockServer((args.host, args.port), Handler)
    server.embed_dim = args.dim
    print(f"mock-openai-server listening on {args.host}:{args.port}, dim={args.dim}")
    server.serve_forever()


if __name__ == "__main__":
    main()
