#!/usr/bin/env python3
"""Measure accepted-to-ready ingestion capacity on a running Query API.

This is a hardware capacity envelope for parse/embed/store, not the L1 query
gate and not an ADR 0010 production SLO. Uploads are serial. The script never
starts Compose and never calls /v1/query.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import subprocess
import sys
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple
from urllib import error as urllib_error
from urllib import request

ROOT = Path(__file__).resolve().parents[1]
ETL_WORKER_DIR = ROOT / "services" / "etl-worker"
DEFAULT_REPORT_DIR = ROOT / "docs" / "evals" / "reports"
PROFILES = ("short-text", "typical-doc")
SHORT_TEXT = (
    "Ingestion capacity fixture short-text. Marker ICAP-SHORT. "
    "This document exists only to measure accepted-to-ready latency for "
    "parsing, embedding, and index write on the current embedding hardware."
)
TYPICAL_PARAGRAPH = (
    "入库容量夹具 typical-doc。Marker ICAP-TYPICAL。本段用于测量真实 embedding "
    "下的 accepted-to-ready 时延，内容没有业务含义。平台应解析、切分、生成向量并写入索引。"
)


class CapacityError(Exception):
    """Setup or runtime failure. CLI maps this to exit 2."""


@dataclass
class GateThresholds:
    max_accepted_to_ready_ms: Optional[float] = None
    min_chunks_per_min: Optional[float] = None
    min_success_rate: float = 1.0


@dataclass
class Sample:
    t_ms: float
    status: str
    stage: str
    chunks_done: int
    total_chunks: int


@dataclass
class DocResult:
    fixture: str
    filename: str
    doc_id: str
    ok: bool
    accepted_to_ready_ms: float = 0.0
    chunks_done: int = 0
    total_chunks: int = 0
    error: str = ""
    samples: List[Dict[str, Any]] = field(default_factory=list)
    stage_ms: Dict[str, float] = field(default_factory=dict)


def percentile(values: List[float], q: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    if len(ordered) == 1:
        return float(ordered[0])
    rank = q * (len(ordered) - 1)
    low = int(rank)
    high = min(low + 1, len(ordered) - 1)
    weight = rank - low
    return float(ordered[low] * (1.0 - weight) + ordered[high] * weight)


def stage_durations(samples: List[Sample], completed_ms: float) -> Dict[str, float]:
    if not samples:
        return {}
    durations: Dict[str, float] = {}
    current = samples[0].stage or samples[0].status
    started = samples[0].t_ms
    for sample in samples[1:]:
        stage = sample.stage or sample.status
        if stage == current:
            continue
        durations[current] = durations.get(current, 0.0) + (sample.t_ms - started)
        current = stage
        started = sample.t_ms
    durations[current] = durations.get(current, 0.0) + (completed_ms - started)
    return {key: round(value, 2) for key, value in durations.items() if value > 0}


def typical_doc_text() -> str:
    return "\n\n".join(f"{TYPICAL_PARAGRAPH} #{index:02d}." for index in range(1, 13))


def fixture_payloads(profile: str, fixture_file: str) -> List[Tuple[str, str, bytes]]:
    if fixture_file.strip():
        path = Path(fixture_file).expanduser().resolve()
        if not path.is_file():
            raise CapacityError(f"fixture file not found: {path}")
        return [("custom", path.name, path.read_bytes())]
    selected = PROFILES if profile == "all" else (profile,)
    payloads: List[Tuple[str, str, bytes]] = []
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S")
    for name in selected:
        if name == "short-text":
            payloads.append(
                ("short-text", f"ingestion-capacity-short-{stamp}.txt", SHORT_TEXT.encode("utf-8"))
            )
        elif name == "typical-doc":
            payloads.append(
                (
                    "typical-doc",
                    f"ingestion-capacity-typical-{stamp}.txt",
                    typical_doc_text().encode("utf-8"),
                )
            )
        else:
            raise CapacityError(f"unknown profile: {name}")
    return payloads


def http_json(
    method: str,
    url: str,
    headers: Optional[Dict[str, str]] = None,
    body: Optional[bytes] = None,
    timeout: float = 15.0,
) -> Tuple[int, Dict[str, Any], float]:
    req = request.Request(url, data=body, method=method)
    for key, value in (headers or {}).items():
        req.add_header(key, value)
    started = time.perf_counter()
    try:
        with request.urlopen(req, timeout=timeout) as resp:
            elapsed = (time.perf_counter() - started) * 1000.0
            raw = resp.read().decode("utf-8", errors="ignore")
            if not raw.strip():
                return resp.status, {}, elapsed
            stripped = raw.lstrip()
            if stripped.startswith("{") or stripped.startswith("["):
                parsed = json.loads(raw)
                if isinstance(parsed, dict):
                    return resp.status, parsed, elapsed
                return resp.status, {"raw": parsed}, elapsed
            return resp.status, {"raw": raw}, elapsed
    except urllib_error.HTTPError as exc:
        elapsed = (time.perf_counter() - started) * 1000.0
        text = exc.read().decode("utf-8", errors="ignore")
        try:
            stripped = text.lstrip()
            payload = json.loads(text) if stripped.startswith("{") or stripped.startswith("[") else {"raw": text}
        except json.JSONDecodeError:
            payload = {"error": text}
        if not isinstance(payload, dict):
            payload = {"raw": payload}
        return exc.code, payload, elapsed
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
    raise CapacityError(f"health check timeout: {url}")


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
        raise CapacityError(f"login failed: status={code}")
    return token


def generate_token(jwt_secret: str, tenant_id: str) -> str:
    go_file = ETL_WORKER_DIR / "tmp_ingestion_capacity_gen_token.go"
    go_src = """package main

import (
	"fmt"
	"os"

	"ai-etl-pipeline/internal/auth"
)

func main() {
	token, err := auth.GenerateTestTokenWithPermission(os.Args[1], os.Args[2], "ingestion-capacity-user", "admin", []string{"upload", "query"})
	if err != nil {
		panic(err)
	}
	fmt.Println(token)
}
"""
    go_file.write_text(go_src, encoding="utf-8")
    try:
        go_bin = shutil.which("go") or "/usr/local/go/bin/go"
        if not Path(go_bin).exists():
            raise CapacityError("go toolchain required to mint a JWT")
        proc = subprocess.run(
            [go_bin, "run", go_file.name, jwt_secret, tenant_id],
            cwd=ETL_WORKER_DIR,
            check=False,
            capture_output=True,
            text=True,
        )
        token = proc.stdout.strip()
        if proc.returncode != 0 or not token:
            raise CapacityError(f"jwt mint failed: {proc.stderr.strip() or proc.stdout.strip()}")
        return token
    finally:
        if go_file.exists():
            go_file.unlink()


def resolve_token(args: argparse.Namespace) -> str:
    token = args.token.strip()
    if token:
        return token
    username = args.username.strip()
    if username:
        return login(args.api_base.rstrip("/"), username, args.password)
    if args.jwt_secret.strip():
        return generate_token(args.jwt_secret, args.tenant_id)
    raise CapacityError("provide --token, --username/--password, or --jwt-secret")


def create_multipart_body(
    filename: str,
    content: bytes,
    permission: str,
    metadata: Dict[str, str] | None = None,
    knowledge_space_id: str = "",
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
    if knowledge_space_id.strip():
        add_field("knowledge_space_id", knowledge_space_id.strip())
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
    knowledge_space_id: str = "",
) -> str:
    body, content_type = create_multipart_body(
        filename, content, permission, metadata, knowledge_space_id
    )
    headers = {"Authorization": f"Bearer {token}", "Content-Type": content_type}
    code, payload, _ = http_json(
        "POST",
        f"{api_base}/v1/upload",
        headers=headers,
        body=body,
        timeout=60.0,
    )
    if code != 202:
        raise CapacityError(f"upload failed: status={code}, payload={payload}")
    doc_id = str(payload.get("doc_id", "")).strip()
    if not doc_id:
        raise CapacityError(f"upload response missing doc_id: {payload}")
    return doc_id


def wait_task(
    api_base: str,
    token: str,
    doc_id: str,
    timeout_sec: int,
    poll_interval_sec: float,
) -> Tuple[bool, float, List[Sample], str]:
    headers = {"Authorization": f"Bearer {token}"}
    started = time.perf_counter()
    deadline = time.time() + timeout_sec
    samples: List[Sample] = []
    last_error = ""
    while time.time() < deadline:
        code, payload, _ = http_json(
            "GET", f"{api_base}/v1/tasks/{doc_id}", headers=headers, timeout=5.0
        )
        now_ms = (time.perf_counter() - started) * 1000.0
        if code in (0, 404):
            time.sleep(poll_interval_sec)
            continue
        if code != 200:
            raise CapacityError(f"task status failed: status={code}, payload={payload}")
        status = str(payload.get("status", "")).strip()
        stage = str(payload.get("stage", "")).strip()
        chunks_done = int(payload.get("chunks_done") or 0)
        total_chunks = int(payload.get("total_chunks") or 0)
        samples.append(
            Sample(
                t_ms=now_ms,
                status=status,
                stage=stage,
                chunks_done=chunks_done,
                total_chunks=total_chunks,
            )
        )
        if status == "completed":
            return True, now_ms, samples, ""
        if status == "failed":
            last_error = str(payload.get("error") or payload)
            return False, now_ms, samples, last_error
        if status == "cancelled":
            return False, now_ms, samples, "task cancelled"
        time.sleep(poll_interval_sec)
    return False, (time.perf_counter() - started) * 1000.0, samples, f"ingestion timed out for doc_id={doc_id}"


def sample_dicts(samples: List[Sample]) -> List[Dict[str, Any]]:
    return [
        {
            "t_ms": round(sample.t_ms, 2),
            "status": sample.status,
            "stage": sample.stage,
            "chunks_done": sample.chunks_done,
            "total_chunks": sample.total_chunks,
        }
        for sample in samples
    ]


def ingest_one(
    api_base: str,
    token: str,
    fixture: str,
    filename: str,
    content: bytes,
    permission: str,
    knowledge_space_id: str,
    timeout_sec: int,
    poll_interval_sec: float,
) -> DocResult:
    metadata = {"purpose": "ingestion-capacity", "fixture": fixture}
    doc_id = upload_document(
        api_base,
        token,
        filename,
        content,
        permission,
        metadata=metadata,
        knowledge_space_id=knowledge_space_id,
    )
    ok, ready_ms, samples, error = wait_task(
        api_base, token, doc_id, timeout_sec, poll_interval_sec
    )
    chunks_done = max((sample.chunks_done for sample in samples), default=0)
    total_chunks = max((sample.total_chunks for sample in samples), default=0)
    return DocResult(
        fixture=fixture,
        filename=filename,
        doc_id=doc_id,
        ok=ok,
        accepted_to_ready_ms=round(ready_ms, 2),
        chunks_done=chunks_done,
        total_chunks=total_chunks,
        error=error,
        samples=sample_dicts(samples),
        stage_ms=stage_durations(samples, ready_ms),
    )


def summarize(results: List[DocResult]) -> Dict[str, Any]:
    ok_results = [item for item in results if item.ok]
    ready = [item.accepted_to_ready_ms for item in ok_results]
    chunks = sum(max(item.total_chunks, item.chunks_done) for item in ok_results)
    total_ms = sum(item.accepted_to_ready_ms for item in ok_results)
    stage_values: Dict[str, List[float]] = {}
    for item in ok_results:
        for stage, value in item.stage_ms.items():
            stage_values.setdefault(stage, []).append(value)
    chunks_per_min = (chunks / total_ms) * 60_000.0 if total_ms > 0 else 0.0
    return {
        "doc_count": len(results),
        "success_count": len(ok_results),
        "success_rate": (len(ok_results) / len(results)) if results else 0.0,
        "chunk_count": chunks,
        "p50_accepted_to_ready_ms": round(percentile(ready, 0.50), 2),
        "p95_accepted_to_ready_ms": round(percentile(ready, 0.95), 2),
        "max_accepted_to_ready_ms": round(max(ready), 2) if ready else 0.0,
        "chunks_per_min": round(chunks_per_min, 3),
        "stage_p50_ms": {stage: round(percentile(values, 0.50), 2) for stage, values in stage_values.items()},
    }


def evaluate_gate(summary: Dict[str, Any], thresholds: GateThresholds) -> List[str]:
    failures: List[str] = []
    if summary.get("success_rate", 0.0) < thresholds.min_success_rate:
        failures.append(
            f"success_rate {summary.get('success_rate')} < {thresholds.min_success_rate}"
        )
    if (
        thresholds.max_accepted_to_ready_ms is not None
        and summary.get("p95_accepted_to_ready_ms", 0.0) > thresholds.max_accepted_to_ready_ms
    ):
        failures.append(
            "p95_accepted_to_ready_ms "
            f"{summary.get('p95_accepted_to_ready_ms')} > {thresholds.max_accepted_to_ready_ms}"
        )
    if (
        thresholds.min_chunks_per_min is not None
        and summary.get("chunks_per_min", 0.0) < thresholds.min_chunks_per_min
    ):
        failures.append(
            f"chunks_per_min {summary.get('chunks_per_min')} < {thresholds.min_chunks_per_min}"
        )
    return failures


def write_report(report_dir: Path, payload: Dict[str, Any]) -> Tuple[Path, Path]:
    report_dir.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    json_path = report_dir / f"ingestion-capacity-{stamp}.json"
    md_path = report_dir / f"ingestion-capacity-{stamp}.md"
    json_path.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    summary = payload["summary"]
    gate = payload["gate"]
    lines = [
        "# Ingestion Capacity",
        "",
        "This report is a hardware capacity envelope, not the L1 query gate and not an ADR 0010 SLO.",
        "",
        f"- Scenario: {payload.get('scenario', '')}",
        f"- Profile: {payload.get('profile', '')}",
        f"- Tenant ID: {payload.get('tenant_id', '')}",
        f"- Embed model hint: {payload.get('embed_model_hint', '')}",
        f"- Docs: {summary['doc_count']}",
        f"- Success rate: {summary['success_rate']:.2f}",
        f"- Chunks: {summary['chunk_count']}",
        f"- p50 accepted-to-ready: {summary['p50_accepted_to_ready_ms']:.2f} ms",
        f"- p95 accepted-to-ready: {summary['p95_accepted_to_ready_ms']:.2f} ms",
        f"- Chunks/min: {summary['chunks_per_min']:.3f}",
        f"- Stage p50: {json.dumps(summary.get('stage_p50_ms', {}), ensure_ascii=False)}",
        f"- Passed: {'yes' if gate.get('passed') else 'no'}",
    ]
    if gate.get("failures"):
        lines.append("- Failures: " + "; ".join(gate["failures"]))
    md_path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    return json_path, md_path


def parse_args(argv: Optional[List[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--api-base", default="http://127.0.0.1:8080")
    parser.add_argument("--scenario", default="ingestion-capacity")
    parser.add_argument("--profile", choices=["short-text", "typical-doc", "all"], default="short-text")
    parser.add_argument("--fixture-file", default="")
    parser.add_argument("--repeat", type=int, default=1)
    parser.add_argument("--tenant-id", default="tenant-ingestion-capacity")
    parser.add_argument("--jwt-secret", default=os.getenv("JWT_SECRET", ""))
    parser.add_argument("--token", default="")
    parser.add_argument("--username", default=os.getenv("INGESTION_CAPACITY_USERNAME", ""))
    parser.add_argument("--password", default=os.getenv("INGESTION_CAPACITY_PASSWORD", ""))
    parser.add_argument("--permission", default="internal")
    parser.add_argument("--knowledge-space-id", default="")
    parser.add_argument("--timeout-sec", type=int, default=3600)
    parser.add_argument("--poll-interval-sec", type=float, default=1.0)
    parser.add_argument("--report-dir", default=str(DEFAULT_REPORT_DIR))
    parser.add_argument("--embed-model-hint", default=os.getenv("EMBED_MODEL", ""))
    parser.add_argument("--max-accepted-to-ready-ms", type=float, default=None)
    parser.add_argument("--min-chunks-per-min", type=float, default=None)
    parser.add_argument("--min-success-rate", type=float, default=1.0)
    parser.add_argument("--skip-health", action="store_true")
    return parser.parse_args(argv)


def main(argv: Optional[List[str]] = None) -> int:
    args = parse_args(argv)
    if args.repeat < 1:
        raise CapacityError("--repeat must be >= 1")
    if args.timeout_sec < 1:
        raise CapacityError("--timeout-sec must be >= 1")
    api_base = args.api_base.rstrip("/")
    if not args.skip_health:
        wait_health(f"{api_base}/healthz")
    token = resolve_token(args)
    fixtures = fixture_payloads(args.profile, args.fixture_file)
    results: List[DocResult] = []
    run_stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S")
    for repeat_index in range(args.repeat):
        for fixture, filename, content in fixtures:
            stem, ext = Path(filename).stem, Path(filename).suffix
            unique_name = f"{stem}-r{repeat_index}{ext}"
            payload = content
            if ext.lower() == ".txt":
                payload = content + f"\n\nICAP-RUN {run_stamp}-{repeat_index}-{fixture}\n".encode("utf-8")
            print(f"[ingestion-capacity] uploading {unique_name} ({fixture})", file=sys.stderr)
            result = ingest_one(
                api_base,
                token,
                fixture,
                unique_name,
                payload,
                args.permission,
                args.knowledge_space_id,
                args.timeout_sec,
                args.poll_interval_sec,
            )
            results.append(result)
            status = "ok" if result.ok else "FAIL"
            print(
                f"[ingestion-capacity] {status} doc_id={result.doc_id} "
                f"ready={result.accepted_to_ready_ms:.0f}ms chunks={result.total_chunks} "
                f"stages={result.stage_ms}",
                file=sys.stderr,
            )
            if not result.ok:
                print(f"[ingestion-capacity] error: {result.error}", file=sys.stderr)
    summary = summarize(results)
    thresholds = GateThresholds(
        max_accepted_to_ready_ms=args.max_accepted_to_ready_ms,
        min_chunks_per_min=args.min_chunks_per_min,
        min_success_rate=args.min_success_rate,
    )
    failures = evaluate_gate(summary, thresholds)
    payload = {
        "kind": "ingestion-capacity",
        "not_slo": True,
        "not_query_gate": True,
        "scenario": args.scenario,
        "profile": args.profile,
        "tenant_id": args.tenant_id,
        "embed_model_hint": args.embed_model_hint,
        "config": {
            "api_base": api_base,
            "repeat": args.repeat,
            "timeout_sec": args.timeout_sec,
            "permission": args.permission,
            "knowledge_space_id": args.knowledge_space_id,
            "serial": True,
        },
        "summary": summary,
        "docs": [item.__dict__ for item in results],
        "gate": {
            "passed": not failures,
            "failures": failures,
            "thresholds": {
                "min_success_rate": thresholds.min_success_rate,
                "max_accepted_to_ready_ms": thresholds.max_accepted_to_ready_ms,
                "min_chunks_per_min": thresholds.min_chunks_per_min,
            },
        },
    }
    json_path, md_path = write_report(Path(args.report_dir), payload)
    print(f"[ingestion-capacity] report {json_path}", file=sys.stderr)
    print(f"[ingestion-capacity] report {md_path}", file=sys.stderr)
    if failures:
        print("GATE FAIL: " + "; ".join(failures), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except CapacityError as exc:
        print(f"[ingestion-capacity] SETUP FAIL: {exc}", file=sys.stderr)
        raise SystemExit(2) from exc
