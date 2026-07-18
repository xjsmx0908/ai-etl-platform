#!/usr/bin/env python3
"""Tiny OpenAI-compatible mock server for local/CI smoke tests."""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import re
from http.server import BaseHTTPRequestHandler, HTTPServer
from typing import Any


class Handler(BaseHTTPRequestHandler):
    server: "MockServer"
    anchor_re = re.compile(r"alpha[0-9a-z-]*", re.IGNORECASE)
    source_re = re.compile(
        r"\[文档\d+\]\s+\(来源:\s*([^)]+)\)\n(.*?)(?=\n\n\[文档\d+\]\s+\(来源:|\n\n用户问题：|$)",
        re.DOTALL,
    )

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
            embedding = self._embed(text)
            tokens = max(1, len(text) // 2)
            self._write_json(
                200,
                {
                    "data": [{"embedding": embedding}],
                    "usage": {"total_tokens": tokens},
                },
            )
            return

        if self.path.endswith("/v1/chat/completions"):
            prompt = self._extract_user_prompt(body)
            if self._is_judge_request(body):
                answer = self._mock_judgement(prompt)
            else:
                answer = self._mock_grounded_answer(prompt)
            self._write_json(
                200,
                {
                    "choices": [
                        {
                            "message": {
                                "content": answer,
                            }
                        }
                    ]
                },
            )
            return

        self._write_json(404, {"error": "not found"})

    def _embed(self, text: str) -> list[float]:
        dim = max(1, int(self.server.embed_dim))
        vector = [0.0] * dim
        tokens = re.findall(r"[A-Za-z0-9]+|[\u4e00-\u9fff]", text.lower())
        if not tokens:
            tokens = [text.lower()]

        for token in tokens:
            digest = hashlib.sha256(token.encode("utf-8")).digest()
            primary = int.from_bytes(digest[:4], "big") % dim
            secondary = int.from_bytes(digest[4:8], "big") % dim
            weight = 1.0 + (len(token) / 10.0)
            vector[primary] += weight
            if secondary != primary:
                vector[secondary] += weight / 2.0

        norm = math.sqrt(sum(v * v for v in vector))
        if norm == 0:
            return vector
        return [round(v / norm, 6) for v in vector]

    def _extract_user_prompt(self, body: dict[str, Any]) -> str:
        messages = body.get("messages")
        if isinstance(messages, list):
            for msg in reversed(messages):
                if not isinstance(msg, dict):
                    continue
                if str(msg.get("role", "")) != "user":
                    continue
                return str(msg.get("content", ""))
        return json.dumps(body, ensure_ascii=False, sort_keys=True)

    def _is_judge_request(self, body: dict[str, Any]) -> bool:
        response_format = body.get("response_format")
        if not isinstance(response_format, dict):
            return False
        schema = response_format.get("json_schema")
        return isinstance(schema, dict) and schema.get("name") == "rag_judgement"

    def _mock_judgement(self, prompt: str) -> str:
        try:
            payload = json.loads(prompt)
        except json.JSONDecodeError:
            payload = {}
        answer = str(payload.get("system_answer") or "").strip()
        contexts = payload.get("retrieved_contexts") or []
        expect_hit = bool(payload.get("expect_hit", True))
        refusal = any(
            marker in answer
            for marker in (
                "未找到相关文档",
                "无法回答",
                "未在参考文档中直接定位锚点",
            )
        )
        passed = bool(answer) and ((expect_hit and bool(contexts)) or (not expect_hit and refusal))
        score = 5 if passed else 2
        return json.dumps(
            {
                "faithfulness_score": score,
                "correctness_score": score,
                "relevance_score": score,
                "overall_pass": passed,
                "reason": "Deterministic mock judgement for protocol validation.",
                "unsupported_claims": [],
            },
            ensure_ascii=False,
        )

    def _parse_sources(self, prompt: str) -> list[tuple[str, str]]:
        out: list[tuple[str, str]] = []
        for match in self.source_re.finditer(prompt):
            doc_id = match.group(1).strip()
            content = match.group(2).strip()
            if doc_id:
                out.append((doc_id, content))
        return out

    def _extract_question(self, prompt: str) -> str:
        marker = "用户问题："
        if marker not in prompt:
            return prompt.strip()
        return prompt.split(marker, 1)[1].strip()

    def _mock_grounded_answer(self, prompt: str) -> str:
        sources = self._parse_sources(prompt)
        if not sources:
            return "未找到相关文档，无法回答该问题。"

        question = self._extract_question(prompt)
        anchor = ""
        match = self.anchor_re.search(question.lower())
        if match:
            anchor = match.group(0).lower()

        if anchor:
            for doc_id, content in sources:
                if anchor in content.lower():
                    snippet = " ".join(content.split())
                    snippet = snippet[:160]
                    return f"命中锚点: {anchor}\n来源: {doc_id}\n依据: {snippet}"

        doc_id, content = sources[0]
        snippet = " ".join(content.split())
        snippet = snippet[:160]
        if anchor:
            return f"未在参考文档中直接定位锚点 {anchor}。\n来源: {doc_id}\n依据: {snippet}"
        return f"基于参考文档回答。\n来源: {doc_id}\n依据: {snippet}"

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
