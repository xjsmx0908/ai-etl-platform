#!/usr/bin/env python3
"""Lightweight load test and query performance gate for the query API.

This is intentionally dependency-free and keeps the target narrow:
- seed one document
- issue concurrent retrieval queries
- report latency percentiles and error rate
- optionally fail when engineering-budget thresholds are missed
"""

from __future__ import annotations

import argparse
import concurrent.futures as futures
import hashlib
import json
import os
import shutil
import statistics
import subprocess
import sys
import tempfile
import time
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any, Dict, List, Mapping, Optional, Tuple
from urllib import error as urllib_error
from urllib import request


ROOT = Path(__file__).resolve().parent.parent
DEFAULT_REPORT_DIR = ROOT / "docs" / "evals" / "reports"
DEFAULT_MOCK_PORT = 18080
ETL_WORKER_DIR = ROOT / "services" / "etl-worker"

PROFILES: Dict[str, Dict[str, Any]] = {
    "cold-retrieval": {
        "retrieval_only": True,
        "unique_questions": False,
        "warmup": 2,
        "min_success_rate": 1.0,
        "min_hit_rate": 1.0,
        "min_top_hit_rate": 1.0,
        "max_p95_ms": 800.0,
        "max_p99_ms": 1500.0,
        "min_qps": 3.0,
    },
    "cached-e2e": {
        "retrieval_only": False,
        "unique_questions": False,
        "warmup": 5,
        "min_success_rate": 1.0,
        "min_hit_rate": 1.0,
        "min_top_hit_rate": 1.0,
        "max_p95_ms": 400.0,
        "max_p99_ms": 800.0,
        "min_qps": 5.0,
    },
}


@dataclass
class Result:
    ok: bool
    hit: bool
    top_hit: bool
    status: int
    latency_ms: float
    top_doc_id: str
    cache_hit: bool = False


@dataclass
class GateThresholds:
    min_success_rate: Optional[float] = None
    min_hit_rate: Optional[float] = None
    min_top_hit_rate: Optional[float] = None
    min_qps: Optional[float] = None
    max_p50_ms: Optional[float] = None
    max_p95_ms: Optional[float] = None
    max_p99_ms: Optional[float] = None

    def as_dict(self) -> Dict[str, float]:
        return {key: value for key, value in asdict(self).items() if value is not None}


class LoadTestError(RuntimeError):
    pass


class GateFailed(RuntimeError):
    def __init__(self, failures: List[str]) -> None:
        self.failures = failures
        super().__init__("; ".join(failures))


def run_cmd(
    cmd: List[str],
    check: bool = True,
    cwd: Optional[Path] = None,
) -> subprocess.CompletedProcess[str]:
    proc = subprocess.run(cmd, cwd=str(cwd or ROOT), text=True, capture_output=True)
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
                parsed = json.loads(payload)
                if isinstance(parsed, dict):
                    return resp.status, parsed, elapsed
                return resp.status, {"raw": parsed}, elapsed
            return resp.status, {"raw": payload}, elapsed
    except urllib_error.HTTPError as e:
        elapsed = (time.perf_counter() - started) * 1000.0
        text = e.read().decode("utf-8", errors="ignore")
        try:
            stripped = text.lstrip()
            payload = json.loads(text) if stripped.startswith("{") or stripped.startswith("[") else {"raw": text}
        except json.JSONDecodeError:
            payload = {"error": text}
        if not isinstance(payload, dict):
            payload = {"raw": payload}
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


def login(api_base: str, username: str, password: str) -> str:
    body = json.dumps({"username": username, "password": password}).encode("utf-8")
    code, payload, _ = http_json(
        "POST",
        f"{api_base}/v1/auth/login",
        headers={"Content-Type": "application/json"},
        body=body,
        timeout=15.0,
    )
    token = str(payload.get("token", "")).strip()
    if code != 200 or not token:
        raise LoadTestError(f"login failed: status={code}")
    return token


def generate_token(jwt_secret: str, tenant_id: str) -> str:
    go_file = ETL_WORKER_DIR / "tmp_loadtest_gen_token.go"
    go_src = [
        "package main",
        "",
        "import (",
        '\t"fmt"',
        '\t"os"',
        "",
        '\t"ai-etl-pipeline/internal/auth"',
        ")",
        "",
        "func main() {",
        '\ttoken, err := auth.GenerateTestToken(os.Args[1], os.Args[2], "loadtest-user", []string{"upload", "query"})',
        "\tif err != nil {",
        "\t\tpanic(err)",
        "\t}",
        "\tfmt.Println(token)",
        "}",
        "",
    ]
    go_file.write_text("\n".join(go_src), encoding="utf-8")
    try:
        if shutil.which("go"):
            proc = run_cmd(["go", "run", go_file.name, jwt_secret, tenant_id], cwd=ETL_WORKER_DIR)
            return proc.stdout.strip()
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
                f'go run /workspace/services/etl-worker/{go_file.name} "$JWT_SECRET_VALUE" "$TENANT_ID_VALUE"',
            ]
        )
        return proc.stdout.strip()
    finally:
        try:
            go_file.unlink()
        except FileNotFoundError:
            pass


def resolve_token(args: argparse.Namespace, api_base: str) -> str:
    token = args.token.strip()
    if token:
        return token
    username = args.username.strip()
    password = args.password
    if username:
        return login(api_base, username, password)
    token = generate_token(args.jwt_secret, args.tenant_id)
    if not token:
        raise LoadTestError("missing token")
    return token


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
        parts.append(f'Content-Disposition: form-data; name="{name}"\r\n\r\n'.encode())
        parts.append(value.encode())
        parts.append(b"\r\n")

    parts.append(f"--{boundary}\r\n".encode())
    parts.append(
        (
            f'Content-Disposition: form-data; name="file"; filename="{filename}"\r\n'
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


def wait_task_completed(api_base: str, token: str, doc_id: str, timeout_sec: int = 120) -> Optional[float]:
    headers = {"Authorization": f"Bearer {token}"}
    started = time.perf_counter()
    deadline = time.time() + timeout_sec
    seen = False
    while time.time() < deadline:
        code, payload, _ = http_json("GET", f"{api_base}/v1/tasks/{doc_id}", headers=headers, timeout=5.0)
        if code == 404 or code == 0:
            time.sleep(1)
            continue
        seen = True
        if code != 200:
            raise LoadTestError(f"task status failed: status={code}, payload={payload}")
        status = str(payload.get("status", ""))
        if status == "completed":
            return (time.perf_counter() - started) * 1000.0
        if status == "failed":
            raise LoadTestError(f"ingestion failed: {payload.get('error') or payload}")
        time.sleep(1)
    if not seen:
        return None
    raise LoadTestError(f"ingestion timed out for doc_id={doc_id}")


def build_query_payload(
    question: str,
    top_k: int,
    retrieval_only: bool = False,
    knowledge_space_id: str = "",
) -> Dict[str, Any]:
    payload: Dict[str, Any] = {"question": question, "top_k": top_k}
    if retrieval_only:
        payload["retrieval_only"] = True
    space_id = knowledge_space_id.strip()
    if space_id:
        payload["knowledge_space_id"] = space_id
    return payload


def question_for_index(base: str, index: int, unique: bool) -> str:
    if not unique:
        return base
    return f"{base} [loadtest-{index}]"


def query_once(
    api_base: str,
    token: str,
    question: str,
    top_k: int,
    expected_doc_id: str = "",
    retrieval_only: bool = False,
    knowledge_space_id: str = "",
) -> Result:
    body = json.dumps(
        build_query_payload(question, top_k, retrieval_only, knowledge_space_id),
        ensure_ascii=False,
    ).encode("utf-8")
    headers = {
        "Authorization": f"Bearer {token}",
        "Content-Type": "application/json",
    }
    status, payload, latency_ms = http_json("POST", f"{api_base}/v1/query", headers=headers, body=body, timeout=30.0)
    sources = payload.get("sources") or payload.get("retrieved_sources") or []
    hit = any(isinstance(src, dict) and src.get("doc_id") for src in sources)
    top_doc_id = ""
    if sources and isinstance(sources[0], dict):
        top_doc_id = str(sources[0].get("doc_id", ""))
    top_hit = bool(expected_doc_id and top_doc_id == expected_doc_id)
    retrieval = payload.get("retrieval") if isinstance(payload.get("retrieval"), dict) else {}
    cache_hit = bool(retrieval.get("cache_hit"))
    return Result(
        ok=status == 200,
        hit=hit,
        top_hit=top_hit,
        status=status,
        latency_ms=latency_ms,
        top_doc_id=top_doc_id,
        cache_hit=cache_hit,
    )


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


def summarize_results(
    results: List[Result],
    duration_ms: float,
    seed_doc_id: str,
    question: str,
    ingestion_ready_ms: Optional[float] = None,
) -> Dict[str, Any]:
    latencies = [r.latency_ms for r in results]
    status_counts: Dict[str, int] = {}
    top_doc_counts: Dict[str, int] = {}
    for item in results:
        status_counts[str(item.status)] = status_counts.get(str(item.status), 0) + 1
        if item.top_doc_id:
            top_doc_counts[item.top_doc_id] = top_doc_counts.get(item.top_doc_id, 0) + 1
    success_count = sum(1 for item in results if item.ok)
    hit_count = sum(1 for item in results if item.hit)
    top_hit_count = sum(1 for item in results if item.top_hit)
    cache_hit_count = sum(1 for item in results if item.cache_hit)
    duration_sec = duration_ms / 1000.0 if duration_ms > 0 else 0.0
    summary: Dict[str, Any] = {
        "total_requests": len(results),
        "success_count": success_count,
        "hit_count": hit_count,
        "top_hit_count": top_hit_count,
        "error_count": len(results) - success_count,
        "success_rate": success_count / len(results) if results else 0.0,
        "hit_rate": hit_count / len(results) if results else 0.0,
        "top_hit_rate": top_hit_count / len(results) if results else 0.0,
        "cache_hit_rate": cache_hit_count / len(results) if results else 0.0,
        "throughput_qps": len(results) / duration_sec if duration_sec > 0 else 0.0,
        "avg_ms": statistics.mean(latencies) if latencies else 0.0,
        "p50_ms": percentile(latencies, 0.50),
        "p95_ms": percentile(latencies, 0.95),
        "p99_ms": percentile(latencies, 0.99),
        "max_ms": max(latencies) if latencies else 0.0,
        "duration_ms": duration_ms,
        "status_counts": status_counts,
        "seed_doc_id": seed_doc_id,
        "question": question,
        "top_doc_counts": top_doc_counts,
        "ingestion_ready_ms": ingestion_ready_ms,
    }
    return summary


def evaluate_gate(summary: Mapping[str, Any], thresholds: GateThresholds) -> List[str]:
    failures: List[str] = []

    def require_min(name: str, actual_key: str, minimum: Optional[float]) -> None:
        if minimum is None:
            return
        actual = float(summary.get(actual_key, 0.0) or 0.0)
        if actual + 1e-12 < minimum:
            failures.append(f"{actual_key} {actual:.4f} < {name} {minimum:.4f}")

    def require_max(name: str, actual_key: str, maximum: Optional[float]) -> None:
        if maximum is None:
            return
        actual = float(summary.get(actual_key, 0.0) or 0.0)
        if actual > maximum + 1e-12:
            failures.append(f"{actual_key} {actual:.2f} > {name} {maximum:.2f}")

    require_min("min_success_rate", "success_rate", thresholds.min_success_rate)
    require_min("min_hit_rate", "hit_rate", thresholds.min_hit_rate)
    require_min("min_top_hit_rate", "top_hit_rate", thresholds.min_top_hit_rate)
    require_min("min_qps", "throughput_qps", thresholds.min_qps)
    require_max("max_p50_ms", "p50_ms", thresholds.max_p50_ms)
    require_max("max_p95_ms", "p95_ms", thresholds.max_p95_ms)
    require_max("max_p99_ms", "p99_ms", thresholds.max_p99_ms)
    return failures


def thresholds_from_args(args: argparse.Namespace) -> GateThresholds:
    return GateThresholds(
        min_success_rate=args.min_success_rate,
        min_hit_rate=args.min_hit_rate,
        min_top_hit_rate=args.min_top_hit_rate,
        min_qps=args.min_qps,
        max_p50_ms=args.max_p50_ms,
        max_p95_ms=args.max_p95_ms,
        max_p99_ms=args.max_p99_ms,
    )


def apply_profile(args: argparse.Namespace) -> argparse.Namespace:
    profile_name = (args.profile or "").strip()
    if not profile_name:
        return args
    profile = PROFILES.get(profile_name)
    if profile is None:
        raise LoadTestError(f"unknown profile: {profile_name}")
    if not args.retrieval_only:
        args.retrieval_only = bool(profile.get("retrieval_only"))
    if not args.unique_questions:
        args.unique_questions = bool(profile.get("unique_questions"))
    if int(args.warmup) == 0 and "warmup" in profile:
        args.warmup = int(profile["warmup"])
    for name in (
        "min_success_rate",
        "min_hit_rate",
        "min_top_hit_rate",
        "min_qps",
        "max_p50_ms",
        "max_p95_ms",
        "max_p99_ms",
    ):
        if getattr(args, name) is None and name in profile:
            setattr(args, name, profile[name])
    if args.scenario == "default":
        args.scenario = profile_name
    return args


def write_report(report_dir: Path, result: Dict[str, Any]) -> Tuple[Path, Path]:
    report_dir.mkdir(parents=True, exist_ok=True)
    ts = time.strftime("%Y%m%d-%H%M%S", time.localtime())
    json_path = report_dir / f"loadtest-{ts}.json"
    md_path = report_dir / f"loadtest-{ts}.md"
    json_path.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")

    summary = result["summary"]
    gate = result.get("gate") or {}
    lines = [
        "# Load Test Report",
        "",
        f"- Scenario: {result['scenario']}",
        f"- Tenant ID: {result['tenant_id']}",
        f"- Concurrency: {result['config']['concurrency']}",
        f"- Total requests: {summary['total_requests']}",
        f"- Success rate: {summary['success_rate']:.2%}",
        f"- Hit rate: {summary['hit_rate']:.2%}",
        f"- Top hit rate: {summary['top_hit_rate']:.2%}",
        f"- Cache hit rate: {summary.get('cache_hit_rate', 0.0):.2%}",
        f"- Throughput: {summary['throughput_qps']:.2f} qps",
        f"- p50: {summary['p50_ms']:.2f} ms",
        f"- p95: {summary['p95_ms']:.2f} ms",
        f"- p99: {summary['p99_ms']:.2f} ms",
        f"- Avg: {summary['avg_ms']:.2f} ms",
    ]
    if summary.get("ingestion_ready_ms") is not None:
        lines.append(f"- Ingestion ready: {summary['ingestion_ready_ms']:.2f} ms")
    lines.extend(["", "## Gate", ""])
    if gate:
        lines.append(f"- Profile: {gate.get('profile') or 'none'}")
        lines.append(f"- Passed: {'yes' if gate.get('passed') else 'no'}")
        thresholds = gate.get("thresholds") or {}
        if thresholds:
            lines.append(f"- Thresholds: `{json.dumps(thresholds, sort_keys=True)}`")
        for failure in gate.get("failures") or []:
            lines.append(f"- Failed: {failure}")
    else:
        lines.append("- Passed: n/a")
    lines.extend(["", "| status | count |", "|---|---:|"])

    status_counts = summary["status_counts"]
    for status, count in sorted(status_counts.items(), key=lambda item: int(item[0])):
        lines.append(f"| {status} | {count} |")

    md_path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    return json_path, md_path


def parse_args(argv: Optional[List[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Lightweight load test for query API")
    parser.add_argument("--api-base", default="http://127.0.0.1:8080")
    parser.add_argument("--scenario", default="default")
    parser.add_argument("--profile", choices=["", *sorted(PROFILES)], default="")
    parser.add_argument("--tenant-id", default="tenant-loadtest")
    parser.add_argument("--jwt-secret", default="change-me-in-production-please-use-32-plus-chars")
    parser.add_argument("--token", default="")
    parser.add_argument("--username", default=os.getenv("LOADTEST_USERNAME", ""))
    parser.add_argument("--password", default=os.getenv("LOADTEST_PASSWORD", ""))
    parser.add_argument("--permission", default="internal")
    parser.add_argument("--knowledge-space-id", default="")
    parser.add_argument("--concurrency", type=int, default=5)
    parser.add_argument("--requests", type=int, default=40)
    parser.add_argument("--warmup", type=int, default=0)
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
    parser.add_argument("--retrieval-only", action="store_true")
    parser.add_argument("--unique-questions", action="store_true")
    parser.add_argument("--min-success-rate", type=float, default=None)
    parser.add_argument("--min-hit-rate", type=float, default=None)
    parser.add_argument("--min-top-hit-rate", type=float, default=None)
    parser.add_argument("--min-qps", type=float, default=None)
    parser.add_argument("--max-p50-ms", type=float, default=None)
    parser.add_argument("--max-p95-ms", type=float, default=None)
    parser.add_argument("--max-p99-ms", type=float, default=None)
    args = parser.parse_args(argv)
    if args.requests <= 0 or args.concurrency <= 0:
        raise LoadTestError("--requests and --concurrency must be positive")
    if args.warmup < 0:
        raise LoadTestError("--warmup must be >= 0")
    return apply_profile(args)


def run_load_test(args: argparse.Namespace) -> Dict[str, Any]:
    api_base = args.api_base.rstrip("/")
    wait_health(f"{api_base}/healthz", timeout_sec=60)
    token = resolve_token(args, api_base)

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
        space_id = args.knowledge_space_id.strip()
        if space_id:
            metadata["knowledge_space_id"] = space_id

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
        ingestion_ready_ms = wait_task_completed(api_base, token, doc_id)
        for i in range(args.noise_docs):
            noise_name = f"noise-{i + 1}-{filename}"
            noise_content = (
                f"Load test distractor document {i + 1}. "
                "This document discusses query throughput, latency, cache behavior, "
                "concurrent retrieval, and reranker tradeoffs for comparison."
            ).encode("utf-8")
            upload_document(args.api_base, token, noise_name, noise_content, args.permission, metadata)

        deadline = time.time() + 120
        while time.time() < deadline:
            ready = query_once(
                api_base,
                token,
                question,
                args.top_k,
                doc_id,
                retrieval_only=args.retrieval_only,
                knowledge_space_id=space_id,
            )
            if ready.ok and ready.top_hit:
                break
            time.sleep(2)
        else:
            raise LoadTestError("seed document did not become queryable as top result in time")

        if args.warmup > 0:
            print(f"[loadtest] warming up {args.warmup} queries")
            for index in range(args.warmup):
                query_once(
                    api_base,
                    token,
                    question_for_index(question, -(index + 1), args.unique_questions),
                    args.top_k,
                    doc_id,
                    retrieval_only=args.retrieval_only,
                    knowledge_space_id=space_id,
                )

        print("[loadtest] starting concurrent queries")
        results: List[Result] = []
        started = time.perf_counter()
        with futures.ThreadPoolExecutor(max_workers=args.concurrency) as pool:
            futs = [
                pool.submit(
                    query_once,
                    api_base,
                    token,
                    question_for_index(question, index, args.unique_questions),
                    args.top_k,
                    doc_id,
                    args.retrieval_only,
                    space_id,
                )
                for index in range(args.requests)
            ]
            for fut in futures.as_completed(futs):
                results.append(fut.result())
        total_ms = (time.perf_counter() - started) * 1000.0
        summary = summarize_results(results, total_ms, doc_id, question, ingestion_ready_ms)
        thresholds = thresholds_from_args(args)
        failures = evaluate_gate(summary, thresholds)
        result = {
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "scenario": args.scenario,
            "tenant_id": args.tenant_id,
            "config": {
                "concurrency": args.concurrency,
                "requests": args.requests,
                "warmup": args.warmup,
                "top_k": args.top_k,
                "noise_docs": args.noise_docs,
                "retrieval_only": bool(args.retrieval_only),
                "unique_questions": bool(args.unique_questions),
                "knowledge_space_id": space_id,
                "metadata_keys": sorted(metadata.keys()),
            },
            "summary": summary,
            "gate": {
                "profile": args.profile or "",
                "thresholds": thresholds.as_dict(),
                "passed": not failures,
                "failures": failures,
            },
        }
        return result
    finally:
        stop_process(mock_proc)
        if tmp_dir is not None:
            tmp_dir.cleanup()


def main(argv: Optional[List[str]] = None) -> int:
    args = parse_args(argv)
    result = run_load_test(args)
    json_path, md_path = write_report(Path(args.report_dir), result)
    summary = result["summary"]
    gate = result["gate"]
    print(f"[loadtest] report json: {json_path}")
    print(f"[loadtest] report md:   {md_path}")
    print(
        f"[loadtest] scenario={result['scenario']} success_rate={summary['success_rate']:.2%} "
        f"hit_rate={summary['hit_rate']:.2%} top_hit_rate={summary['top_hit_rate']:.2%} "
        f"qps={summary['throughput_qps']:.2f} p95={summary['p95_ms']:.2f}ms "
        f"gate={'pass' if gate['passed'] else 'fail'}"
    )
    if not gate["passed"]:
        for failure in gate["failures"]:
            print(f"[loadtest] GATE FAIL: {failure}", file=sys.stderr)
        raise GateFailed(gate["failures"])
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except KeyboardInterrupt:
        raise SystemExit(130)
    except GateFailed:
        raise SystemExit(1)
    except LoadTestError as exc:
        print(f"[loadtest] ERROR: {exc}", file=sys.stderr)
        raise SystemExit(2)
