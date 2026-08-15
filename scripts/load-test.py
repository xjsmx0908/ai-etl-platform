#!/usr/bin/env python3
"""Lightweight load test for the query API.

This is intentionally dependency-free and keeps the target narrow:
- seed one document
- issue concurrent retrieval queries
- report latency percentiles and error rate
"""

from __future__ import annotations

import argparse
import concurrent.futures as futures
import hashlib
import json
import os
import signal
import statistics
import subprocess
import sys
import tempfile
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Dict, List, Tuple
from urllib import error as urllib_error
from urllib import request


ROOT = Path(__file__).resolve().parent.parent
DEFAULT_REPORT_DIR = ROOT / "docs" / "evals" / "reports"
DEFAULT_MOCK_PORT = 18080


@dataclass
class Result:
    ok: bool
    hit: bool
    top_hit: bool
    status: int
    latency_ms: float
    top_doc_id: str


class LoadTestError(RuntimeError):
    pass


def run_cmd(cmd: List[str], check: bool = True) -> subprocess.CompletedProcess[str]:
    proc = subprocess.run(cmd, cwd=str(ROOT), text=True, capture_output=True)
    if check and proc.returncode != 0:
        raise LoadTestError(f"command failed: {' '.join(cmd)}\nstdout:\n{proc.stdout}\nstderr:\n{proc.stderr}")
    return proc


def start_mock_server(port: int, dim: int) -> subprocess.Popen[str]:
    cmd = [
        sys.executable,
        str(ROOT / "scripts" / "mock-openai-server.py"),
        "--host",
        "0.0.0.0",
        "--port",
        str(port),
        "--dim",
        str(dim),
    ]
    return subprocess.Popen(
        cmd,
        cwd=str(ROOT),
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )


def stop_process(proc: subprocess.Popen[str] | None) -> None:
    if proc is None:
        return
    if proc.poll() is not None:
        return
    proc.terminate()
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill()


def http_json(
    method: str,
    url: str,
    headers: Dict[str, str] | None = None,
    body: bytes | None = None,
    timeout: float = 20.0,
) -> Tuple[int, Dict[str, Any], float]:
    req = request.Request(url=url, data=body, method=method)
    if headers:
        for key, value in headers.items():
            req.add_header(key, value)

    started = time.perf_counter()
    try:
        with request.urlopen(req, timeout=timeout) as resp:
            payload = resp.read().decode("utf-8")
            elapsed = (time.perf_counter() - started) * 1000.0
            if not payload:
                return resp.status, {}, elapsed
            stripped = payload.lstrip()
            if stripped.startswith("{") or stripped.startswith("["):
                return resp.status, json.loads(payload), elapsed
            return resp.status, {"raw": payload}, elapsed
    except urllib_error.HTTPError as e:
        elapsed = (time.perf_counter() - started) * 1000.0
        text = e.read().decode("utf-8", errors="ignore")
        try:
            stripped = text.lstrip()
            payload = json.loads(text) if stripped.startswith("{") or stripped.startswith("[") else {"raw": text}
        except json.JSONDecodeError:
            payload = {"error": text}
        return e.code, payload, elapsed
    except urllib_error.URLError:
        elapsed = (time.perf_counter() - started) * 1000.0
        return 0, {}, elapsed


def wait_health(url: str, timeout_sec: int = 60) -> None:
    deadline = time.time() + timeout_sec
    while time.time() < deadline:
        code, _, _ = http_json("GET", url, timeout=3.0)
        if code == 200:
            return
        time.sleep(1)
    raise LoadTestError(f"health check timeout: {url}")


def generate_token(jwt_secret: str, tenant_id: str) -> str:
    go_file = ROOT / "services" / "etl-worker" / "tmp_loadtest_gen_token.go"
    go_file.write_text(
        """package main

import (
\t\"fmt\"
\t\"os\"

\t\"ai-etl-pipeline/internal/auth\"
)

func main() {
\ttoken, err := auth.GenerateTestToken(os.Args[1], os.Args[2], \"loadtest-user\", []string{\"upload\", \"query\"})
\tif err != nil {
\t\tpanic(err)
\t}
\tfmt.Println(token)
}
""",
        encoding="utf-8",
    )
    try:
        proc = run_cmd(
            [
                "docker",
                "run",
                "--rm",
                "-v",
                f"{ROOT}:/workspace",
                "-w",
                "/workspace/services/etl-worker",
                "-e",
                f"JWT_SECRET_VALUE={jwt_secret}",
                "-e",
                f"TENANT_ID_VALUE={tenant_id}",
                "golang:1.25",
                "sh",
                "-c",
                f"go run /workspace/services/etl-worker/{go_file.name} \"$JWT_SECRET_VALUE\" \"$TENANT_ID_VALUE\"",
            ]
        )
        return proc.stdout.strip()
    finally:
        try:
            go_file.unlink()
        except FileNotFoundError:
            pass


def create_multipart_body(
    filename: str,
    content: bytes,
    permission: str,
    metadata: Dict[str, str] | None = None,
) -> Tuple[bytes, str]:
    metadata_json = json.dumps(metadata or {}, ensure_ascii=False, sort_keys=True)
    boundary = f"----aietl{hashlib.sha256((filename + permission + metadata_json).encode()).hexdigest()[:24]}"
    parts: List[bytes] = []

    def add_field(name: str, value: str) -> None:
        parts.append(f"--{boundary}\r\n".encode())
        parts.append(f'Content-Disposition: form-data; name=\"{name}\"\r\n\r\n'.encode())
        parts.append(value.encode())
        parts.append(b"\r\n")

    parts.append(f"--{boundary}\r\n".encode())
    parts.append(
        (
            f'Content-Disposition: form-data; name=\"file\"; filename=\"{filename}\"\r\n'
            "Content-Type: text/plain\r\n\r\n"
        ).encode()
    )
    parts.append(content)
    parts.append(b"\r\n")
    add_field("permission", permission)
    if metadata:
        add_field("metadata", metadata_json)
    parts.append(f"--{boundary}--\r\n".encode())
    return b"".join(parts), f"multipart/form-data; boundary={boundary}"


def upload_document(
    api_base: str,
    token: str,
    filename: str,
    content: bytes,
    permission: str,
    metadata: Dict[str, str] | None = None,
) -> str:
    body, content_type = create_multipart_body(filename, content, permission, metadata)
    headers = {
        "Authorization": f"Bearer {token}",
        "Content-Type": content_type,
    }
    code, payload, _ = http_json("POST", f"{api_base}/v1/upload", headers=headers, body=body, timeout=30.0)
    if code != 202:
        raise LoadTestError(f"upload failed: status={code}, payload={payload}")
    doc_id = str(payload.get("doc_id", ""))
    if not doc_id:
        raise LoadTestError(f"upload response missing doc_id: {payload}")
    return doc_id


def query_once(api_base: str, token: str, question: str, top_k: int, expected_doc_id: str = "") -> Result:
    body = json.dumps({"question": question, "top_k": top_k}, ensure_ascii=False).encode("utf-8")
    headers = {
        "Authorization": f"Bearer {token}",
        "Content-Type": "application/json",
    }
    status, payload, latency_ms = http_json("POST", f"{api_base}/v1/query", headers=headers, body=body, timeout=30.0)
    sources = payload.get("sources") or []
    hit = any(isinstance(src, dict) and src.get("doc_id") for src in sources)
    top_doc_id = ""
    if sources and isinstance(sources[0], dict):
        top_doc_id = str(sources[0].get("doc_id", ""))
    top_hit = bool(expected_doc_id and top_doc_id == expected_doc_id)
    return Result(ok=status == 200, hit=hit, top_hit=top_hit, status=status, latency_ms=latency_ms, top_doc_id=top_doc_id)


def percentile(values: List[float], p: float) -> float:
    if not values:
        return 0.0
    if len(values) == 1:
        return values[0]
    ordered = sorted(values)
    k = (len(ordered) - 1) * p
    f = int(k)
    c = min(f + 1, len(ordered) - 1)
    if f == c:
        return ordered[f]
    d0 = ordered[f] * (c - k)
    d1 = ordered[c] * (k - f)
    return d0 + d1


def write_report(report_dir: Path, result: Dict[str, Any]) -> Tuple[Path, Path]:
    report_dir.mkdir(parents=True, exist_ok=True)
    ts = time.strftime("%Y%m%d-%H%M%S", time.localtime())
    json_path = report_dir / f"loadtest-{ts}.json"
    md_path = report_dir / f"loadtest-{ts}.md"
    json_path.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")

    lines = [
        "# Load Test Report",
        "",
        f"- Scenario: {result['scenario']}",
        f"- Tenant ID: {result['tenant_id']}",
        f"- Concurrency: {result['config']['concurrency']}",
        f"- Total requests: {result['summary']['total_requests']}",
        f"- Success rate: {result['summary']['success_rate']:.2%}",
        f"- Hit rate: {result['summary']['hit_rate']:.2%}",
        f"- Top hit rate: {result['summary']['top_hit_rate']:.2%}",
        f"- Throughput: {result['summary']['throughput_qps']:.2f} qps",
        f"- p50: {result['summary']['p50_ms']:.2f} ms",
        f"- p95: {result['summary']['p95_ms']:.2f} ms",
        f"- p99: {result['summary']['p99_ms']:.2f} ms",
        f"- Avg: {result['summary']['avg_ms']:.2f} ms",
        "",
        "| status | count |",
        "|---|---:|",
    ]

    status_counts = result["summary"]["status_counts"]
    for status, count in sorted(status_counts.items(), key=lambda item: int(item[0])):
        lines.append(f"| {status} | {count} |")

    md_path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    return json_path, md_path


def main() -> int:
    parser = argparse.ArgumentParser(description="Lightweight load test for query API")
    parser.add_argument("--api-base", default="http://127.0.0.1:8080")
    parser.add_argument("--scenario", default="default")
    parser.add_argument("--tenant-id", default="tenant-loadtest")
    parser.add_argument("--jwt-secret", default="change-me-in-production-please-use-32-plus-chars")
    parser.add_argument("--token", default="")
    parser.add_argument("--permission", default="internal")
    parser.add_argument("--concurrency", type=int, default=5)
    parser.add_argument("--requests", type=int, default=40)
    parser.add_argument("--top-k", type=int, default=5)
    parser.add_argument("--question", default="")
    parser.add_argument("--report-dir", default=str(DEFAULT_REPORT_DIR))
    parser.add_argument("--seed-file", default="")
    parser.add_argument("--seed-name", default="")
    parser.add_argument("--metadata-json", default="")
    parser.add_argument("--noise-docs", type=int, default=0)
    parser.add_argument("--mock-port", type=int, default=DEFAULT_MOCK_PORT)
    parser.add_argument("--embed-dim", type=int, default=768)
    parser.add_argument("--keep-mock-server", action="store_true")
    args = parser.parse_args()

    wait_health(f"{args.api_base}/healthz", timeout_sec=60)

    token = args.token.strip() or generate_token(args.jwt_secret, args.tenant_id)
    if not token:
        raise LoadTestError("missing token")

    tmp_dir: tempfile.TemporaryDirectory[str] | None = None
    mock_proc: subprocess.Popen[str] | None = None
    try:
        if not args.keep_mock_server:
            print("[loadtest] starting mock model server")
            mock_proc = start_mock_server(args.mock_port, args.embed_dim)
            wait_health(f"http://127.0.0.1:{args.mock_port}/healthz", timeout_sec=30)

        metadata: Dict[str, str] = {}
        if args.metadata_json.strip():
            raw_metadata = json.loads(args.metadata_json)
            if not isinstance(raw_metadata, dict) or not all(
                isinstance(k, str) and isinstance(v, str) for k, v in raw_metadata.items()
            ):
                raise LoadTestError("--metadata-json must be a JSON object with string keys and values")
            metadata = raw_metadata

        if args.seed_file:
            seed_path = Path(args.seed_file)
            if not seed_path.exists():
                raise LoadTestError(f"seed file not found: {seed_path}")
            filename = args.seed_name or seed_path.name
            content = seed_path.read_bytes()
            question = args.question or f"Which document contains {filename}?"
        else:
            tmp_dir = tempfile.TemporaryDirectory()
            anchor = hashlib.sha256(f"{time.time()}-{os.getpid()}".encode()).hexdigest()[:10]
            seed_path = Path(tmp_dir.name) / f"loadtest-{anchor}.txt"
            filename = seed_path.name
            content = (
                f"Load test anchor token {anchor}. "
                "This document validates query throughput and latency under concurrency."
            ).encode("utf-8")
            seed_path.write_bytes(content)
            question = args.question or f"Which document contains {anchor}?"

        print("[loadtest] uploading seed document")
        doc_id = upload_document(args.api_base, token, filename, content, args.permission, metadata)
        for i in range(args.noise_docs):
            noise_name = f"noise-{i + 1}-{filename}"
            noise_content = (
                f"Load test distractor document {i + 1}. "
                "This document discusses query throughput, latency, cache behavior, "
                "concurrent retrieval, and reranker tradeoffs for comparison."
            ).encode("utf-8")
            upload_document(args.api_base, token, noise_name, noise_content, args.permission)

        deadline = time.time() + 120
        while time.time() < deadline:
            result = query_once(args.api_base, token, question, args.top_k, doc_id)
            if result.ok and result.top_hit:
                break
            time.sleep(2)
        else:
            raise LoadTestError("seed document did not become queryable as top result in time")

        print("[loadtest] starting concurrent queries")
        results: List[Result] = []
        started = time.perf_counter()
        with futures.ThreadPoolExecutor(max_workers=args.concurrency) as pool:
            futs = [
                pool.submit(query_once, args.api_base, token, question, args.top_k, doc_id)
                for _ in range(args.requests)
            ]
            for fut in futures.as_completed(futs):
                results.append(fut.result())
        total_ms = (time.perf_counter() - started) * 1000.0

        latencies = [r.latency_ms for r in results]
        status_counts: Dict[str, int] = {}
        for r in results:
            status_counts[str(r.status)] = status_counts.get(str(r.status), 0) + 1

        success_count = sum(1 for r in results if r.ok)
        hit_count = sum(1 for r in results if r.hit)
        top_hit_count = sum(1 for r in results if r.top_hit)
        duration_sec = total_ms / 1000.0 if total_ms > 0 else 0.0
        summary = {
            "total_requests": len(results),
            "success_count": success_count,
            "hit_count": hit_count,
            "top_hit_count": top_hit_count,
            "error_count": len(results) - success_count,
            "success_rate": success_count / len(results) if results else 0.0,
            "hit_rate": hit_count / len(results) if results else 0.0,
            "top_hit_rate": top_hit_count / len(results) if results else 0.0,
            "throughput_qps": len(results) / duration_sec if duration_sec > 0 else 0.0,
            "avg_ms": statistics.mean(latencies) if latencies else 0.0,
            "p50_ms": percentile(latencies, 0.50),
            "p95_ms": percentile(latencies, 0.95),
            "p99_ms": percentile(latencies, 0.99),
            "max_ms": max(latencies) if latencies else 0.0,
            "duration_ms": total_ms,
            "status_counts": status_counts,
            "seed_doc_id": doc_id,
            "question": question,
            "top_doc_counts": {},
        }
        for r in results:
            if r.top_doc_id:
                summary["top_doc_counts"][r.top_doc_id] = summary["top_doc_counts"].get(r.top_doc_id, 0) + 1
        result = {
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "scenario": args.scenario,
            "tenant_id": args.tenant_id,
            "config": {
                "concurrency": args.concurrency,
                "requests": args.requests,
                "top_k": args.top_k,
                "noise_docs": args.noise_docs,
                "metadata_keys": sorted(metadata.keys()),
            },
            "summary": summary,
        }

        json_path, md_path = write_report(Path(args.report_dir), result)
        print(f"[loadtest] report json: {json_path}")
        print(f"[loadtest] report md:   {md_path}")
        print(
            f"[loadtest] scenario={args.scenario} success_rate={summary['success_rate']:.2%} "
            f"hit_rate={summary['hit_rate']:.2%} top_hit_rate={summary['top_hit_rate']:.2%} "
            f"qps={summary['throughput_qps']:.2f} p95={summary['p95_ms']:.2f}ms"
        )
        return 0
    finally:
        stop_process(mock_proc)
        if tmp_dir is not None:
            tmp_dir.cleanup()


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except KeyboardInterrupt:
        raise SystemExit(130)
    except LoadTestError as exc:
        print(f"[loadtest] ERROR: {exc}", file=sys.stderr)
        raise SystemExit(2)
