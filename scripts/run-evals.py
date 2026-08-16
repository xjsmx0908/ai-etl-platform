#!/usr/bin/env python3
"""Retrieval eval runner for AI-ETL pipeline.

Two model modes:

- mock (default): a local hash-based OpenAI-compatible server. Deterministic and
  free, suitable as a CI gate. It validates the integration path only — the
  embeddings carry no semantic structure, so its retrieval metrics are NOT a
  quality signal. See docs/adr/0002-deterministic-mock-models.md.
- real (--real-models): live embedding and LLM endpoints. The only mode whose
  Recall/MRR numbers describe actual retrieval quality.

Flow:
1) Start the mock model server (mock mode only).
2) Start docker compose stack (staging mode, single worker/api).
3) Upload golden documents through /v1/upload.
4) Poll /v1/query and verify each query retrieves expected doc_id.
5) Emit JSON + Markdown report for CI and local usage.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import socket
import subprocess
import sys
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, List, Sequence, Set, Tuple
from urllib import error as urllib_error
from urllib import request

from judge_eval import JudgeClient, JudgeConfig, JudgeError, JudgeInput


ROOT = Path(__file__).resolve().parent.parent
DEFAULT_GOLDEN_SET = ROOT / "docs" / "evals" / "golden-set.json"
DEFAULT_REPORT_DIR = ROOT / "docs" / "evals" / "reports"
EVAL_COMPOSE_FILE = ROOT / "docker-compose.eval.yml"
NOT_FOUND_ANSWER = "未找到相关文档，无法回答该问题。"

# Refusal detection must not depend on any single model's phrasing.
#
# An earlier version also accepted "未在参考文档中直接定位锚点" — that string is the
# *mock server's* wording, so the assertion silently encoded a mock implementation
# detail and could never match a real model. Real models phrase refusals freely,
# so match on refusal semantics instead: the canonical string the system prompt
# asks for, plus common paraphrases.
NEGATIVE_FALLBACK_MARKERS = (
    NOT_FOUND_ANSWER,
    "未找到相关文档",
    "无法回答",
    "没有相关文档",
    "不包含",
    "无法从参考文档",
    "参考文档不足",
    "未提供相关",
)

# Dataset tokens that express "cite the source" rather than a literal substring.
# They are validated against the response's sources[].doc_id, not the answer text.
CITATION_MARKER_TOKENS = frozenset({"来源:", "来源：", "来源"})


@dataclass
class EvalCase:
    case_id: str
    filename: str
    permission: str
    content: str
    query: str
    metadata: Dict[str, str] = field(default_factory=dict)
    query_permission: str = ""
    acceptable_doc_ids: List[str] = field(default_factory=list)
    expect_hit: bool = True
    max_strict_rank: int = 0
    query_top_k: int = 0
    must_not_hit_doc_ids: List[str] = field(default_factory=list)
    require_source_citation: bool = True
    answer_must_include: List[str] = field(default_factory=list)
    answer_must_not_include: List[str] = field(default_factory=list)
    reference_answer: str = ""


class EvalRunnerError(RuntimeError):
    pass


def run_cmd(
    cmd: List[str],
    env: Dict[str, str] | None = None,
    check: bool = True,
    timeout_sec: int | None = None,
) -> subprocess.CompletedProcess[str]:
    try:
        proc = subprocess.run(
            cmd,
            cwd=str(ROOT),
            env=env,
            text=True,
            capture_output=True,
            stdin=subprocess.DEVNULL,
            timeout=timeout_sec,
        )
    except subprocess.TimeoutExpired as exc:
        raise EvalRunnerError(
            f"command timed out after {timeout_sec}s: {' '.join(cmd)}\n"
            f"stdout:\n{exc.stdout or ''}\n"
            f"stderr:\n{exc.stderr or ''}"
        ) from exc
    if check and proc.returncode != 0:
        raise EvalRunnerError(
            f"command failed: {' '.join(cmd)}\nstdout:\n{proc.stdout}\nstderr:\n{proc.stderr}"
        )
    return proc


@dataclass(frozen=True)
class ModelProfile:
    """Resolved model endpoints for one eval run.

    mock mode  -> deterministic hash-based embeddings, integration-path validation only.
    real mode  -> live embedding/LLM services, the only mode whose retrieval metrics
                  say anything about quality.
    """

    mode: str  # "mock" | "real"
    embed_endpoint: str
    embed_model: str
    embed_dim: int
    embed_api_key: str
    llm_endpoint: str
    llm_model: str
    llm_api_key: str
    store_collection: str

    @property
    def is_real(self) -> bool:
        return self.mode == "real"


def read_secret_file(path: str) -> str:
    if not path:
        return ""
    try:
        return Path(path).read_text(encoding="utf-8").strip()
    except OSError:
        return ""


def resolve_model_profile(args: argparse.Namespace, mock_port: int) -> ModelProfile:
    """Build the model profile for this run.

    Real mode reads endpoints from CLI flags, then environment, then .env-style
    *_FILE_PATH secrets — matching the Go services' `KEY` > `KEY_FILE` precedence
    so the eval stack and the app resolve credentials the same way.
    """
    if not args.real_models:
        return ModelProfile(
            mode="mock",
            embed_endpoint=f"http://host.docker.internal:{mock_port}/v1/embeddings",
            embed_model="eval-embed",
            embed_dim=args.embed_dim,
            embed_api_key="",
            llm_endpoint=f"http://host.docker.internal:{mock_port}/v1/chat/completions",
            llm_model="eval-chat",
            llm_api_key="",
            store_collection=args.store_collection or "documents",
        )

    embed_endpoint = (args.embed_endpoint or os.getenv("EMBED_ENDPOINT", "")).strip()
    embed_model = (args.embed_model or os.getenv("EMBED_MODEL", "")).strip()
    llm_endpoint = (args.llm_endpoint or os.getenv("LLM_ENDPOINT", "")).strip()
    llm_model = (args.llm_model or os.getenv("LLM_MODEL", "")).strip()

    embed_api_key = (
        os.getenv("EMBED_API_KEY", "").strip()
        or read_secret_file(os.getenv("EMBED_API_KEY_FILE_PATH", ""))
    )
    llm_api_key = (
        os.getenv("LLM_API_KEY", "").strip()
        or read_secret_file(os.getenv("LLM_API_KEY_FILE_PATH", ""))
    )

    missing = [
        name
        for name, value in (
            ("EMBED_ENDPOINT/--embed-endpoint", embed_endpoint),
            ("EMBED_MODEL/--embed-model", embed_model),
            ("LLM_ENDPOINT/--llm-endpoint", llm_endpoint),
            ("LLM_MODEL/--llm-model", llm_model),
        )
        if not value
    ]
    if missing:
        raise EvalRunnerError(
            "--real-models requires: " + ", ".join(missing) + "\n"
            "Set them via flags or environment (a local .env is not auto-loaded by this script)."
        )

    # Qdrant collection dimension is immutable after creation. Mock runs use an
    # 8-dim hash vector while real runs use the model's native dimension, so they
    # must never share a collection.
    collection = (args.store_collection or "").strip()
    if not collection:
        safe_model = re.sub(r"[^a-z0-9]+", "-", embed_model.lower()).strip("-")
        collection = f"documents-real-{safe_model}-{args.embed_dim}"

    return ModelProfile(
        mode="real",
        embed_endpoint=embed_endpoint,
        embed_model=embed_model,
        embed_dim=args.embed_dim,
        embed_api_key=embed_api_key,
        llm_endpoint=llm_endpoint,
        llm_model=llm_model,
        llm_api_key=llm_api_key,
        store_collection=collection,
    )


def compose_env(
    profile: ModelProfile,
    tenant_id: str,
    jwt_secret: str,
    compose_project: str,
) -> Dict[str, str]:
    env = os.environ.copy()
    env.update(
        {
            "ENVIRONMENT": "staging",
            "WORKER_REPLICAS": "1",
            "API_REPLICAS": "1",
            "EMBED_DIMENSION": str(profile.embed_dim),
            "EMBED_MODEL": profile.embed_model,
            "LLM_MODEL": profile.llm_model,
            "EMBED_ENDPOINT": profile.embed_endpoint,
            "LLM_ENDPOINT": profile.llm_endpoint,
            "EMBED_API_KEY": profile.embed_api_key,
            "LLM_API_KEY": profile.llm_api_key,
            "STORE_COLLECTION": profile.store_collection,
            "JWT_SECRET": jwt_secret,
            "KAFKA_TOPIC": env.get("KAFKA_TOPIC", "doc-processing"),
            "REDIS_CACHE_ADDR": "redis-cache:6379",
            "REDIS_CACHE_DB": "0",
            "REDIS_STATE_ADDR": "redis-state:6379",
            "REDIS_STATE_DB": "0",
            "COMPOSE_PROJECT_NAME": compose_project,
            "COMPOSE_FILE": os.pathsep.join((str(ROOT / "docker-compose.yml"), str(EVAL_COMPOSE_FILE))),
            "QUERY_API_HOST_PORT": "0",
        }
    )
    # Secrets resolve as KEY > KEY_FILE in the Go services. Empty *_FILE paths
    # would otherwise let a stale mounted secret override the profile above.
    if profile.is_real:
        env.pop("EMBED_API_KEY_FILE", None)
        env.pop("LLM_API_KEY_FILE", None)
    else:
        # Mock mode: the verifier LLM is deterministic and never emits a grounded
        # verdict, so disable the post-generation check to keep CI deterministic.
        env["RETRIEVAL_GROUNDING_CHECK"] = "false"
    return env


def estimate_llm_cost(prompt_tokens: int, completion_tokens: int) -> float:
    """Estimated LLM spend for a run from LLM_PRICE_* env vars (USD per 1K)."""
    prompt_price = float(os.getenv("LLM_PRICE_PROMPT_PER_1K", "0") or 0)
    completion_price = float(os.getenv("LLM_PRICE_COMPLETION_PER_1K", "0") or 0)
    if prompt_price <= 0 and completion_price <= 0:
        return 0.0
    return prompt_tokens / 1000 * prompt_price + completion_tokens / 1000 * completion_price


def wait_for_es_sync(
    env: Dict[str, str],
    expected_docs: int,
    timeout_sec: int = 120,
    poll_sec: int = 2,
) -> None:
    """Block until Elasticsearch has indexed all expected documents.

    The worker writes to Qdrant synchronously but feeds ES through an async
    retry queue. Exact-keyword queries route ES with 0.75 weight, so a document
    that is present in Qdrant but not yet in ES scores 0 on the BM25 side and is
    pushed out of the top-K by RRF fusion — the eval then reports a false
    retrieval timeout. Polling ES's document count closes that race.
    """
    es_index = env.get("ES_INDEX", "documents_text")
    deadline = time.time() + timeout_sec
    last_count = -1
    while time.time() < deadline:
        # ES may still be starting up; a timed-out curl is "not ready yet",
        # not an error — keep polling until the deadline.
        try:
            proc = run_cmd(
                ["docker", "compose", "exec", "-T", "elasticsearch", "curl", "-s",
                 f"http://localhost:9200/{es_index}/_count"],
                env=env, check=False, timeout_sec=20,
            )
            last_count = int(json.loads(proc.stdout).get("count", 0))
        except (EvalRunnerError, ValueError, TypeError):
            last_count = -1
        if last_count >= expected_docs:
            print(f"[eval] es sync ready: {last_count}/{expected_docs}")
            return
        time.sleep(poll_sec)
    # Don't fail the whole eval over ES sync health: it only risks false
    # retrieval timeouts for exact-keyword cases, and the sync can lag when the
    # eval stack shares the host with the live demo. Warn and continue.
    print(
        f"[eval] WARN: ES sync check did not reach {expected_docs} after "
        f"{timeout_sec}s (last count {last_count}); continuing"
    )


def resolve_compose_project(raw: str) -> str:
    project = raw.strip().lower()
    if not project:
        project = f"ai-etl-eval-{int(time.time())}-{os.getpid()}"
    if not re.fullmatch(r"[a-z0-9][a-z0-9_-]*", project):
        raise EvalRunnerError("--compose-project must use lowercase letters, digits, hyphens, or underscores")
    return project


def api_base_from_compose_port(raw: str) -> str:
    match = re.search(r":(\d+)$", raw.strip())
    if not match:
        raise EvalRunnerError(f"could not parse query-api host port: {raw!r}")
    return f"http://127.0.0.1:{match.group(1)}"


def http_json(
    method: str,
    url: str,
    headers: Dict[str, str] | None = None,
    body: bytes | None = None,
    timeout: float = 15.0,
) -> Tuple[int, Dict[str, Any]]:
    req = request.Request(url=url, data=body, method=method)
    if headers:
        for k, v in headers.items():
            req.add_header(k, v)
    try:
        with request.urlopen(req, timeout=timeout) as resp:
            payload = resp.read().decode("utf-8")
            if not payload:
                return resp.status, {}
            stripped = payload.lstrip()
            if stripped.startswith("{") or stripped.startswith("["):
                return resp.status, json.loads(payload)
            return resp.status, {"raw": payload}
    except urllib_error.HTTPError as e:
        text = e.read().decode("utf-8", errors="ignore")
        try:
            stripped = text.lstrip()
            payload = json.loads(text) if stripped.startswith("{") or stripped.startswith("[") else {"raw": text}
        except json.JSONDecodeError:
            payload = {"error": text}
        return e.code, payload
    except (urllib_error.URLError, TimeoutError, ConnectionError, OSError):
        return 0, {}


def wait_health(url: str, timeout_sec: int) -> None:
    deadline = time.time() + timeout_sec
    while time.time() < deadline:
        code, _ = http_json("GET", url, timeout=3.0)
        if code == 200:
            return
        time.sleep(1)
    raise EvalRunnerError(f"health check timeout: {url}")


def start_mock_server(mock_port: int, embed_dim: int) -> subprocess.Popen[str]:
    cmd = [
        sys.executable,
        str(ROOT / "scripts" / "mock-openai-server.py"),
        "--port",
        str(mock_port),
        "--dim",
        str(embed_dim),
    ]
    proc = subprocess.Popen(
        cmd,
        cwd=str(ROOT),
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    return proc


def pick_mock_port(preferred: int) -> int:
    def is_port_free(port: int) -> bool:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
            sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            return sock.connect_ex(("127.0.0.1", port)) != 0

    if preferred > 0 and is_port_free(preferred):
        return preferred

    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


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


def remove_project_images(compose_project: str) -> None:
    """Delete images built for this eval project.

    Each isolated run builds its own tagged images (etl-worker, query-api,
    parser-service, alert-webhook-service). `compose down` does not remove them,
    so repeated runs accumulate gigabytes of dead tags.
    """
    project = compose_project.strip()
    if not project:
        return
    listed = run_cmd(
        ["docker", "images", "--format", "{{.Repository}}:{{.Tag}}"],
        check=False,
        timeout_sec=30,
    )
    tags = [
        line.strip()
        for line in listed.stdout.splitlines()
        if line.strip().startswith(f"{project}-")
    ]
    if not tags:
        return
    run_cmd(["docker", "rmi", *tags], check=False, timeout_sec=120)
    print(f"[eval] removed {len(tags)} eval image(s) for project {project}")


def generate_token(jwt_secret: str, tenant_id: str, permission: str = "user") -> str:
    go_file = ROOT / "services" / "etl-worker" / "tmp_eval_gen_token.go"
    go_file.write_text(
        """package main

import (
	\"fmt\"
	\"os\"

	\"ai-etl-pipeline/internal/auth\"
)

func main() {
	token, err := auth.GenerateTestTokenWithPermission(os.Args[1], os.Args[2], \"eval-user\", os.Args[3], []string{\"upload\", \"query\"})
	if err != nil {
		panic(err)
	}
	fmt.Println(token)
}
""",
        encoding="utf-8",
    )
    try:
        cmd = [
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
            "-e",
            f"PERMISSION_VALUE={permission}",
            "golang:1.25",
            "sh",
            "-c",
            f"go run /workspace/services/etl-worker/{go_file.name} \"$JWT_SECRET_VALUE\" \"$TENANT_ID_VALUE\" \"$PERMISSION_VALUE\"",
        ]
        proc = run_cmd(cmd, timeout_sec=120)
        return proc.stdout.strip()
    finally:
        try:
            go_file.unlink()
        except FileNotFoundError:
            pass


def create_multipart_body(
    field_name: str,
    filename: str,
    content: bytes,
    permission: str,
    metadata: Dict[str, str] | None = None,
) -> Tuple[bytes, str]:
    metadata_json = json.dumps(metadata or {}, ensure_ascii=False, sort_keys=True)
    boundary = f"----aietl{hashlib.sha256((filename + permission + metadata_json).encode()).hexdigest()[:24]}"
    lines: List[bytes] = []

    def add_part(name: str, value: str) -> None:
        lines.append(f"--{boundary}\r\n".encode())
        lines.append(f'Content-Disposition: form-data; name="{name}"\r\n\r\n'.encode())
        lines.append(value.encode())
        lines.append(b"\r\n")

    lines.append(f"--{boundary}\r\n".encode())
    lines.append(
        (
            f'Content-Disposition: form-data; name="{field_name}"; filename="{filename}"\r\n'
            "Content-Type: text/plain\r\n\r\n"
        ).encode()
    )
    lines.append(content)
    lines.append(b"\r\n")

    add_part("permission", permission)
    if metadata:
        add_part("metadata", metadata_json)

    lines.append(f"--{boundary}--\r\n".encode())
    body = b"".join(lines)
    content_type = f"multipart/form-data; boundary={boundary}"
    return body, content_type


def upload_case(api_base: str, token: str, case: EvalCase) -> str:
    body, ctype = create_multipart_body(
        "file",
        case.filename,
        case.content.encode("utf-8"),
        case.permission,
        case.metadata,
    )
    headers = {
        "Authorization": f"Bearer {token}",
        "Content-Type": ctype,
    }
    code, payload = http_json("POST", f"{api_base}/v1/upload", headers=headers, body=body, timeout=30.0)
    if code != 202:
        raise EvalRunnerError(f"upload failed for {case.case_id}, status={code}, payload={payload}")
    doc_id = str(payload.get("doc_id", ""))
    if not doc_id:
        raise EvalRunnerError(f"upload response missing doc_id for {case.case_id}: {payload}")
    return doc_id


def query_case(api_base: str, token: str, query: str, top_k: int = 5) -> Tuple[int, Dict[str, Any]]:
    payload = json.dumps({"question": query, "top_k": top_k}, ensure_ascii=False).encode("utf-8")
    headers = {
        "Authorization": f"Bearer {token}",
        "Content-Type": "application/json",
    }
    return http_json("POST", f"{api_base}/v1/query", headers=headers, body=payload, timeout=30.0)


def find_first_match(sources: Sequence[Dict[str, Any]], candidates: Set[str]) -> Tuple[int, float]:
    if not candidates:
        return 0, 0.0
    for idx, src in enumerate(sources, start=1):
        if str(src.get("doc_id", "")) in candidates:
            return idx, float(src.get("score", 0.0))
    return 0, 0.0


def resolve_doc_refs(refs: Sequence[str], uploaded_doc_ids: Dict[str, str]) -> List[str]:
    out: List[str] = []
    for ref in refs:
        value = uploaded_doc_ids.get(ref, ref)
        value = str(value).strip()
        if value:
            out.append(value)
    return sorted(set(out))


def resolve_acceptable_doc_ids(
    case: EvalCase,
    uploaded_doc_ids: Dict[str, str],
    expected_doc_id: str,
) -> List[str]:
    out = [expected_doc_id]
    out.extend(resolve_doc_refs(case.acceptable_doc_ids, uploaded_doc_ids))
    return sorted(set(out))


def resolve_forbidden_doc_ids(
    case: EvalCase,
    uploaded_doc_ids: Dict[str, str],
    expected_doc_id: str,
) -> List[str]:
    out = resolve_doc_refs(case.must_not_hit_doc_ids, uploaded_doc_ids)
    if not case.expect_hit:
        out.append(expected_doc_id)
    return sorted(set(out))


def extract_anchor_token(text: str) -> str:
    match = re.search(r"alpha[0-9a-z-]*", text.lower())
    if not match:
        return ""
    return match.group(0)


def evaluate_case_assertions(
    api_base: str,
    token: str,
    query: str,
    expected_doc_id: str,
    expect_hit: bool,
    acceptable_doc_ids: Sequence[str],
    forbidden_doc_ids: Sequence[str],
    query_top_k: int,
    max_strict_rank: int,
    required_consecutive_hits: int,
    poll_interval_seconds: float,
    max_wait_seconds: int,
) -> Tuple[Dict[str, Any], Dict[str, Any]]:
    deadline = time.time() + max_wait_seconds
    last_payload: Dict[str, Any] = {}
    acceptable_set = set(acceptable_doc_ids)
    forbidden_set = set(forbidden_doc_ids)
    strict_set = {expected_doc_id}
    strict_hit = False
    strict_score = 0.0
    strict_rank = 0
    acceptable_hit = False
    acceptable_score = 0.0
    acceptable_rank = 0
    forbidden_hit = False
    forbidden_score = 0.0
    forbidden_rank = 0
    consecutive_hits = 0
    max_consecutive_hits = 0
    strict_rank_requirement_met = False
    assertion_pass = False
    assertion_reason = "timeout"
    recall_hits = {1: False, 3: False, 5: False}

    while time.time() < deadline:
        code, payload = query_case(api_base, token, query, top_k=query_top_k)
        if code == 200:
            last_payload = payload
            sources = payload.get("sources") or []
            s_rank, s_score = find_first_match(sources, strict_set)
            a_rank, a_score = find_first_match(sources, acceptable_set)
            f_rank, f_score = find_first_match(sources, forbidden_set)

            if s_rank > 0:
                strict_hit = True
                if strict_rank == 0 or s_rank < strict_rank:
                    strict_rank = s_rank
                    strict_score = s_score
            if a_rank > 0:
                acceptable_hit = True
                if acceptable_rank == 0 or a_rank < acceptable_rank:
                    acceptable_rank = a_rank
                    acceptable_score = a_score
                if expect_hit:
                    for k in recall_hits:
                        if a_rank <= k:
                            recall_hits[k] = True
            if f_rank > 0:
                forbidden_hit = True
                if forbidden_rank == 0 or f_rank < forbidden_rank:
                    forbidden_rank = f_rank
                    forbidden_score = f_score

            rank_ok = s_rank > 0 and (max_strict_rank <= 0 or s_rank <= max_strict_rank)
            if rank_ok:
                strict_rank_requirement_met = True

            if expect_hit:
                if rank_ok and f_rank == 0:
                    consecutive_hits += 1
                else:
                    consecutive_hits = 0
                if consecutive_hits > max_consecutive_hits:
                    max_consecutive_hits = consecutive_hits
                if consecutive_hits >= required_consecutive_hits:
                    assertion_pass = True
                    assertion_reason = f"strict_hit_consecutive_{required_consecutive_hits}"
                    break
            else:
                if s_rank > 0:
                    assertion_pass = False
                    assertion_reason = "unexpected_strict_hit"
                    break
                if f_rank > 0:
                    assertion_pass = False
                    assertion_reason = "unexpected_forbidden_hit"
                    break

        time.sleep(poll_interval_seconds)

    if not expect_hit and not strict_hit and not forbidden_hit:
        assertion_pass = True
        assertion_reason = "no_forbidden_or_strict_hit_within_window"
    if expect_hit and strict_hit and max_strict_rank > 0 and not strict_rank_requirement_met:
        assertion_reason = f"strict_hit_but_rank_gt_{max_strict_rank}"
    if expect_hit and strict_hit and max_consecutive_hits < required_consecutive_hits:
        assertion_reason = f"strict_hit_but_not_consecutive_{required_consecutive_hits}"

    details = {
        "hit": strict_hit,
        "score": strict_score if strict_hit else 0.0,
        "strict_rank": strict_rank if strict_hit else 0,
        "strict_rank_requirement_met": strict_rank_requirement_met if max_strict_rank > 0 else True,
        "acceptable_hit": acceptable_hit,
        "acceptable_score": acceptable_score if acceptable_hit else 0.0,
        "acceptable_rank": acceptable_rank if acceptable_hit else 0,
        "forbidden_hit": forbidden_hit,
        "forbidden_score": forbidden_score if forbidden_hit else 0.0,
        "forbidden_rank": forbidden_rank if forbidden_hit else 0,
        "max_consecutive_hits": max_consecutive_hits,
        "assertion_pass": assertion_pass,
        "assertion_reason": assertion_reason,
        "recall_at_1": recall_hits[1],
        "recall_at_3": recall_hits[3],
        "recall_at_5": recall_hits[5],
    }
    # Token usage from the final successful response, so reports can price a run.
    usage = (last_payload or {}).get("token_usage") or {}
    details["prompt_tokens"] = int(usage.get("prompt_tokens", 0) or 0)
    details["completion_tokens"] = int(usage.get("completion_tokens", 0) or 0)
    return details, last_payload


def evaluate_answer_assertions(
    case: EvalCase,
    payload: Dict[str, Any],
    acceptable_doc_ids: Sequence[str],
    enable_answer_assertions: bool,
) -> Dict[str, Any]:
    if not enable_answer_assertions:
        return {
            "answer_assertion_pass": True,
            "answer_assertion_reason": "answer_assertions_disabled",
            "answer_len": 0,
            "answer_source_citation_ok": True,
            "answer_cited_doc_ids": [],
        }

    answer = str((payload or {}).get("answer") or "").strip()
    if not answer:
        return {
            "answer_assertion_pass": False,
            "answer_assertion_reason": "answer_empty",
            "answer_len": 0,
            "answer_source_citation_ok": False,
            "answer_cited_doc_ids": [],
        }

    answer_lower = answer.lower()
    sources = (payload or {}).get("sources") or []
    source_doc_ids = []
    for src in sources:
        doc_id = str(src.get("doc_id", "")).strip()
        if doc_id:
            source_doc_ids.append(doc_id)
    cited = sorted({doc_id for doc_id in set(source_doc_ids).union(acceptable_doc_ids) if doc_id and doc_id in answer})
    source_citation_ok = (not case.require_source_citation) or bool(cited)

    if case.expect_hit:
        anchor = extract_anchor_token(case.query)
        if anchor and anchor not in answer_lower:
            return {
                "answer_assertion_pass": False,
                "answer_assertion_reason": "missing_anchor_token",
                "answer_len": len(answer),
                "answer_source_citation_ok": source_citation_ok,
                "answer_cited_doc_ids": cited,
            }
        if not source_citation_ok:
            return {
                "answer_assertion_pass": False,
                "answer_assertion_reason": "missing_source_citation",
                "answer_len": len(answer),
                "answer_source_citation_ok": source_citation_ok,
                "answer_cited_doc_ids": cited,
            }
    else:
        if not any(marker in answer for marker in NEGATIVE_FALLBACK_MARKERS):
            return {
                "answer_assertion_pass": False,
                "answer_assertion_reason": "negative_case_missing_not_found_fallback",
                "answer_len": len(answer),
                "answer_source_citation_ok": source_citation_ok,
                "answer_cited_doc_ids": cited,
            }

    for token in case.answer_must_include:
        normalized = token.strip()
        if not normalized:
            continue
        # "来源:" in a dataset means "the answer must cite its source". Asserting it
        # as a literal encodes the mock server's output template ("来源: <doc_id>");
        # a real model writes 「根据文档1」or「参考 case-031」— semantically correct,
        # literally absent. Check the citation structurally instead.
        if normalized in CITATION_MARKER_TOKENS:
            if not source_citation_ok:
                return {
                    "answer_assertion_pass": False,
                    "answer_assertion_reason": "missing_source_citation",
                    "answer_len": len(answer),
                    "answer_source_citation_ok": source_citation_ok,
                    "answer_cited_doc_ids": cited,
                }
            continue
        if normalized.lower() not in answer_lower:
            return {
                "answer_assertion_pass": False,
                "answer_assertion_reason": f"answer_missing_phrase:{normalized}",
                "answer_len": len(answer),
                "answer_source_citation_ok": source_citation_ok,
                "answer_cited_doc_ids": cited,
            }

    for token in case.answer_must_not_include:
        normalized = token.strip()
        if normalized and normalized.lower() in answer_lower:
            return {
                "answer_assertion_pass": False,
                "answer_assertion_reason": f"answer_contains_forbidden_phrase:{normalized}",
                "answer_len": len(answer),
                "answer_source_citation_ok": source_citation_ok,
                "answer_cited_doc_ids": cited,
            }

    return {
        "answer_assertion_pass": True,
        "answer_assertion_reason": "answer_assertions_passed",
        "answer_len": len(answer),
        "answer_source_citation_ok": source_citation_ok,
        "answer_cited_doc_ids": cited,
    }


def load_cases(path: Path) -> List[EvalCase]:
    data = json.loads(path.read_text(encoding="utf-8"))
    out: List[EvalCase] = []
    for raw in data.get("cases", []):
        out.append(
            EvalCase(
                case_id=str(raw["id"]),
                filename=str(raw["filename"]),
                permission=str(raw.get("permission", "internal")),
                content=str(raw["content"]),
                query=str(raw["query"]),
                metadata={str(k): str(v) for k, v in raw.get("metadata", {}).items()},
                query_permission=str(raw.get("query_permission", "")),
                acceptable_doc_ids=[str(v) for v in raw.get("acceptable_doc_ids", [])],
                expect_hit=bool(raw.get("expect_hit", True)),
                max_strict_rank=int(raw.get("max_strict_rank", 0)),
                query_top_k=int(raw.get("query_top_k", 0)),
                must_not_hit_doc_ids=[str(v) for v in raw.get("must_not_hit_doc_ids", [])],
                require_source_citation=bool(raw.get("require_source_citation", raw.get("expect_hit", True))),
                answer_must_include=[str(v) for v in raw.get("answer_must_include", [])],
                answer_must_not_include=[str(v) for v in raw.get("answer_must_not_include", [])],
                reference_answer=str(raw.get("reference_answer", "")),
            )
        )
    if not out:
        raise EvalRunnerError("golden set is empty")
    return out


def summarize_dist(values: Sequence[float]) -> Dict[str, Any]:
    """Percentile summary of a score distribution for gate-separability analysis."""
    vals = sorted(float(v) for v in values)
    if not vals:
        return {"count": 0}
    n = len(vals)

    def pct(p: float) -> float:
        return vals[min(n - 1, max(0, int(p * n)))]

    return {
        "count": n,
        "min": round(vals[0], 4),
        "p25": round(pct(0.25), 4),
        "median": round(pct(0.5), 4),
        "p75": round(pct(0.75), 4),
        "max": round(vals[-1], 4),
        "mean": round(sum(vals) / n, 4),
    }


def write_report(report_dir: Path, result: Dict[str, Any]) -> Tuple[Path, Path]:
    report_dir.mkdir(parents=True, exist_ok=True)
    ts = time.strftime("%Y%m%d-%H%M%S", time.localtime())
    json_path = report_dir / f"eval-{ts}.json"
    md_path = report_dir / f"eval-{ts}.md"

    json_path.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")

    mode = str(result.get("model_mode", "mock"))
    models = result.get("models", {})

    lines = ["# RAG Eval Report", ""]

    if mode == "real":
        lines.append("**Model mode: REAL** — metrics below reflect actual retrieval quality.")
    else:
        lines.append(
            "**Model mode: MOCK — integration-path validation only, NOT a quality signal.** "
            "Embeddings are hash-derived and carry no semantic structure, so Recall/hit-rate "
            "here measure whether the pipeline is wired correctly, not whether retrieval is good. "
            "Use `--real-models` for quality claims."
        )
    lines.append("")

    lines.extend(
        [
            f"- Timestamp: {result['timestamp']}",
            f"- Tenant ID: {result.get('tenant_id', '')}",
            f"- Model Mode: {mode}",
            f"- Embed Model: {models.get('embed_model', '')} (dim={models.get('embed_dimension', '')})",
            f"- LLM Model: {models.get('llm_model', '')}",
            f"- Store Collection: {models.get('store_collection', '')}",
            f"- Mock Port: {result.get('mock_port', '')}",
            f"- Compose Project: {result.get('compose_project', '')}",
            f"- Query API Base: {result.get('api_base', '')}",
            f"- Total cases: {result['summary']['total_cases']}",
            f"- Assertion passed: {result['summary']['assertion_passed_cases']}",
            f"- Assertion failed: {result['summary']['assertion_failed_cases']}",
            f"- Positive cases: {result['summary']['positive_cases']}",
            f"- Positive retrieval passed: {result['summary']['positive_passed_cases']}",
            f"- Positive final passed: {result['summary']['positive_final_passed_cases']}",
            f"- Negative cases: {result['summary']['negative_cases']}",
            f"- Negative retrieval passed: {result['summary']['negative_passed_cases']}",
            f"- Negative final passed: {result['summary']['negative_final_passed_cases']}",
            f"- Retrieval pass rate: {result['summary']['retrieval_pass_rate']:.2%}",
            f"- Answer pass rate: {result['summary']['answer_pass_rate']:.2%}",
            f"- Pass rate: {result['summary']['pass_rate']:.2%}",
            f"- Hit rate: {result['summary']['hit_rate']:.2%}",
            f"- Acceptable hit rate: {result['summary']['acceptable_hit_rate']:.2%}",
            f"- Recall@1: {result['summary']['recall_at_1']:.2%}",
            f"- Recall@3: {result['summary']['recall_at_3']:.2%}",
            f"- Recall@5: {result['summary']['recall_at_5']:.2%}",
            f"- Avg score: {result['summary']['avg_score']:.4f}",
            f"- Avg acceptable score: {result['summary']['avg_acceptable_score']:.4f}",
            f"- Total tokens: {result['summary'].get('total_tokens', 0)}",
            f"- Avg tokens/query: {result['summary'].get('avg_tokens_per_query', 0):.0f}",
            f"- Est cost USD: {result['summary'].get('estimated_cost_usd', 0):.4f}",
        ]
    )

    judge_enabled = bool(result["summary"].get("judge_enabled"))
    if judge_enabled:
        lines.extend(
            [
                f"- Judge model: {result['summary']['judge_model']}",
                f"- Judge attempted: {result['summary']['judge_attempted_cases']}",
                f"- Judge errors: {result['summary']['judge_error_cases']}",
                f"- Judge pass rate: {result['summary']['judge_pass_rate']:.2%}",
                f"- Judge avg faithfulness: {result['summary']['judge_avg_faithfulness']:.2f}/5",
                f"- Judge avg correctness: {result['summary']['judge_avg_correctness']:.2f}/5",
                f"- Judge avg relevance: {result['summary']['judge_avg_relevance']:.2f}/5",
            ]
        )

    lines.append("")
    if judge_enabled:
        lines.extend(
            [
                "| Case | Retrieval | Answer | Final | Rank | Judge | Faithfulness | Correctness | Relevance | Judge Error |",
                "|---|---:|---:|---:|---:|---:|---:|---:|---:|---|",
            ]
        )
    else:
        lines.extend(
            [
                "| Case | Expect Hit | Retrieval Pass | Answer Pass | Final Pass | Strict Rank | Max Rank | Final Reason |",
                "|---|---:|---:|---:|---:|---:|---:|---|",
            ]
        )

    for item in result["cases"]:
        if judge_enabled:
            judgement = item.get("judge") or {}
            judge_error = str(item.get("judge_error") or "").replace("|", "/")
            lines.append(
                f"| {item['case_id']} | {'Y' if item['retrieval_assertion_pass'] else 'N'} | "
                f"{'Y' if item['answer_assertion_pass'] else 'N'} | {'Y' if item['assertion_pass'] else 'N'} | "
                f"{item['strict_rank']} | {'Y' if judgement.get('overall_pass') else 'N'} | "
                f"{judgement.get('faithfulness_score', '-')} | {judgement.get('correctness_score', '-')} | "
                f"{judgement.get('relevance_score', '-')} | {judge_error or '-'} |"
            )
        else:
            lines.append(
                f"| {item['case_id']} | {'Y' if item['expect_hit'] else 'N'} | "
                f"{'Y' if item['retrieval_assertion_pass'] else 'N'} | "
                f"{'Y' if item['answer_assertion_pass'] else 'N'} | "
                f"{'Y' if item['assertion_pass'] else 'N'} | {item['strict_rank']} | "
                f"{item['max_strict_rank']} | {item['assertion_reason']} |"
            )

    md_path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    return json_path, md_path


def main() -> int:
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(line_buffering=True)
    if hasattr(sys.stderr, "reconfigure"):
        sys.stderr.reconfigure(line_buffering=True)

    parser = argparse.ArgumentParser(description="Run deterministic retrieval eval for AI-ETL")
    parser.add_argument("--golden-set", default=str(DEFAULT_GOLDEN_SET))
    parser.add_argument(
        "--tenant-id",
        default="",
        help="tenant id for eval JWT; defaults to an auto-generated unique tenant per run",
    )
    parser.add_argument("--jwt-secret", default="change-me-in-production-please-use-32-plus-chars")
    parser.add_argument("--mock-port", type=int, default=18080)
    parser.add_argument("--embed-dim", type=int, default=768)
    parser.add_argument(
        "--real-models",
        action="store_true",
        help="use live embedding/LLM endpoints instead of the deterministic mock server; "
        "required for any run whose metrics are treated as a quality signal",
    )
    parser.add_argument(
        "--embed-endpoint",
        default="",
        help="real mode only; defaults to $EMBED_ENDPOINT",
    )
    parser.add_argument("--embed-model", default="", help="real mode only; defaults to $EMBED_MODEL")
    parser.add_argument(
        "--llm-endpoint",
        default="",
        help="real mode only; defaults to $LLM_ENDPOINT",
    )
    parser.add_argument("--llm-model", default="", help="real mode only; defaults to $LLM_MODEL")
    parser.add_argument(
        "--store-collection",
        default="",
        help="Qdrant collection override; real mode auto-derives a per-model name because "
        "collection dimension is immutable and differs from the mock's",
    )
    parser.add_argument("--max-wait", type=int, default=180)
    parser.add_argument("--negative-max-wait", type=int, default=30)
    parser.add_argument("--top-k", type=int, default=5)
    parser.add_argument("--poll-interval", type=float, default=2.0)
    parser.add_argument("--required-consecutive-hits", type=int, default=2)
    parser.add_argument(
        "--api-base",
        default="",
        help="optional Query API base URL override; defaults to the isolated Compose mapping",
    )
    parser.add_argument(
        "--compose-project",
        default="",
        help="optional isolated Docker Compose project name",
    )
    parser.add_argument("--min-hit-rate", type=float, default=0.9)
    parser.add_argument("--min-answer-pass-rate", type=float, default=1.0)
    parser.add_argument("--min-pass-rate", type=float, default=1.0)
    parser.add_argument("--disable-answer-assertions", action="store_true")
    parser.add_argument("--keep-services", action="store_true")
    parser.add_argument("--report-dir", default=str(DEFAULT_REPORT_DIR))
    parser.add_argument("--judge", action="store_true", help="enable optional LLM-as-a-Judge scoring")
    parser.add_argument(
        "--judge-endpoint",
        default=os.getenv("JUDGE_ENDPOINT", "https://api.openai.com/v1"),
    )
    parser.add_argument(
        "--judge-api-key",
        default=os.getenv("JUDGE_API_KEY", os.getenv("OPENAI_API_KEY", "")),
    )
    parser.add_argument("--judge-model", default=os.getenv("JUDGE_MODEL", "gpt-4o-mini"))
    parser.add_argument("--judge-timeout", type=float, default=30.0)
    parser.add_argument("--judge-max-retries", type=int, default=2)
    parser.add_argument(
        "--judge-max-cases",
        type=int,
        default=0,
        help="maximum cases sent to the judge; 0 evaluates all cases",
    )
    parser.add_argument("--judge-min-pass-rate", type=float, default=0.80)
    parser.add_argument("--judge-min-faithfulness", type=float, default=4.0)
    args = parser.parse_args()

    golden_set_path = Path(args.golden_set)
    report_dir = Path(args.report_dir)
    # Pre-create the report dir as the running user BEFORE docker compose up: the
    # web service mounts docs/evals/reports (ro) as a volume, and Docker creates
    # a missing mount source as root — which would make this directory unwritable
    # by the non-root CI user later.
    report_dir.mkdir(parents=True, exist_ok=True)
    if not golden_set_path.exists():
        raise EvalRunnerError(f"golden set not found: {golden_set_path}")

    cases = load_cases(golden_set_path)
    judge_client = None
    if args.judge:
        if args.judge_max_cases < 0:
            raise EvalRunnerError("--judge-max-cases must be >= 0")
        if args.judge_min_pass_rate < 0 or args.judge_min_pass_rate > 1:
            raise EvalRunnerError("--judge-min-pass-rate must be in [0,1]")
        if args.judge_min_faithfulness < 1 or args.judge_min_faithfulness > 5:
            raise EvalRunnerError("--judge-min-faithfulness must be in [1,5]")
        try:
            judge_client = JudgeClient(
                JudgeConfig(
                    endpoint=args.judge_endpoint,
                    api_key=args.judge_api_key,
                    model=args.judge_model,
                    timeout_seconds=args.judge_timeout,
                    max_retries=args.judge_max_retries,
                )
            )
        except JudgeError as exc:
            raise EvalRunnerError(str(exc)) from exc
    tenant_id = args.tenant_id.strip() if args.tenant_id else ""
    if not tenant_id:
        tenant_id = f"tenant-eval-{int(time.time() * 1000)}-{os.getpid()}"
    resolved_mock_port = pick_mock_port(args.mock_port)
    compose_project = resolve_compose_project(args.compose_project)
    profile = resolve_model_profile(args, resolved_mock_port)

    mock_proc: subprocess.Popen[str] | None = None
    started_services = False
    env: Dict[str, str] = {}

    def cleanup() -> None:
        stop_process(mock_proc)
        if started_services and not args.keep_services:
            run_cmd(
                ["docker", "compose", "down", "-v", "--remove-orphans"],
                env=env,
                check=False,
            )
            # `compose down` removes containers but leaves the images this
            # project built. Each run adds ~1GB of tagged images otherwise.
            remove_project_images(compose_project)

    try:
        if profile.is_real:
            print(f"[eval] model mode: REAL")
            print(f"[eval]   embed: {profile.embed_model} @ {profile.embed_endpoint} (dim={profile.embed_dim})")
            print(f"[eval]   llm:   {profile.llm_model} @ {profile.llm_endpoint}")
            print(f"[eval]   collection: {profile.store_collection}")
        else:
            print("[eval] model mode: MOCK (integration-path validation, not a quality signal)")
            print(f"[eval] starting mock model server on port {resolved_mock_port}")
            mock_proc = start_mock_server(resolved_mock_port, args.embed_dim)
            wait_health(f"http://127.0.0.1:{resolved_mock_port}/healthz", timeout_sec=30)

        print(f"[eval] tenant_id: {tenant_id}")
        print(f"[eval] compose project: {compose_project}")
        env = compose_env(
            profile,
            tenant_id,
            args.jwt_secret,
            compose_project,
        )

        print("[eval] starting docker compose stack")
        run_cmd(["docker", "compose", "up", "-d", "--build"], env=env, timeout_sec=900)
        started_services = True

        api_base = args.api_base.strip().rstrip("/")
        if not api_base:
            port_result = run_cmd(
                ["docker", "compose", "port", "query-api", "8080"],
                env=env,
                timeout_sec=30,
            )
            api_base = api_base_from_compose_port(port_result.stdout)

        print("[eval] waiting query-api healthz")
        wait_health(f"{api_base}/healthz", timeout_sec=180)

        print("[eval] ensuring kafka topic exists")
        run_cmd(
            [
                "docker",
                "compose",
                "exec",
                "-T",
                "kafka",
                "kafka-topics.sh",
                "--bootstrap-server",
                "localhost:9092",
                "--create",
                "--if-not-exists",
                "--topic",
                env.get("KAFKA_TOPIC", "doc-processing"),
                "--partitions",
                "1",
                "--replication-factor",
                "1",
            ],
            env=env,
            check=True,
            timeout_sec=30,
        )

        print("[eval] restarting etl-worker")
        run_cmd(["docker", "compose", "restart", "etl-worker"], env=env, timeout_sec=60)
        time.sleep(5)

        print("[eval] generating JWT token")
        upload_token = generate_token(args.jwt_secret, tenant_id, permission="user")
        if not upload_token:
            raise EvalRunnerError("failed to generate JWT token")
        query_tokens: Dict[str, str] = {"user": upload_token}
        if args.required_consecutive_hits <= 0:
            raise EvalRunnerError("--required-consecutive-hits must be >= 1")
        if args.poll_interval <= 0:
            raise EvalRunnerError("--poll-interval must be > 0")
        if args.negative_max_wait <= 0:
            raise EvalRunnerError("--negative-max-wait must be > 0")
        if args.min_answer_pass_rate < 0 or args.min_answer_pass_rate > 1:
            raise EvalRunnerError("--min-answer-pass-rate must be in [0,1]")

        eval_items: List[Dict[str, Any]] = []
        uploaded_doc_ids: Dict[str, str] = {}
        scores: List[float] = []
        acceptable_scores: List[float] = []
        total_prompt_tokens = 0
        total_completion_tokens = 0
        query_token_counts: List[int] = []
        positive_passed = 0
        negative_passed = 0
        positive_retrieval_passed = 0
        negative_retrieval_passed = 0
        assertion_passed = 0
        retrieval_assertion_passed = 0
        answer_assertion_passed = 0
        acceptable_passed = 0
        positive_total = 0
        negative_total = 0
        recall_hits = {1: 0, 3: 0, 5: 0}
        judge_attempted = 0
        judge_passed = 0
        judge_errors = 0
        judge_faithfulness_scores: List[int] = []
        judge_correctness_scores: List[int] = []
        judge_relevance_scores: List[int] = []
        positive_max_relevance: List[float] = []
        negative_max_relevance: List[float] = []

        for idx, case in enumerate(cases, start=1):
            print(f"[eval] {idx}/{len(cases)} upload {case.case_id}")
            doc_id = upload_case(api_base, upload_token, case)
            uploaded_doc_ids[case.case_id] = doc_id

        # Wait for the async full-text sink to finish indexing before querying.
        # Exact-keyword queries route ES with 0.75 weight; if ES has not caught
        # up, a document that is present in Qdrant scores 0 on the BM25 side and
        # gets pushed out of the top-K by the RRF fusion — the eval then reports
        # a false retrieval timeout. The documents are always uploaded and stored;
        # the race is purely the eval's, not the pipeline's.
        wait_for_es_sync(env, len(uploaded_doc_ids), timeout_sec=120, poll_sec=2)

        for idx, case in enumerate(cases, start=1):
            expected_doc_id = uploaded_doc_ids[case.case_id]
            acceptable_doc_ids = resolve_acceptable_doc_ids(case, uploaded_doc_ids, expected_doc_id)
            forbidden_doc_ids = resolve_forbidden_doc_ids(case, uploaded_doc_ids, expected_doc_id)
            query_top_k = max(case.query_top_k if case.query_top_k > 0 else args.top_k, 5)
            max_strict_rank = max(case.max_strict_rank, 0)
            case_max_wait = args.max_wait if case.expect_hit else args.negative_max_wait
            query_permission = (case.query_permission or "user").strip().lower() or "user"
            token = query_tokens.get(query_permission, "")
            if not token:
                token = generate_token(args.jwt_secret, tenant_id, permission=query_permission)
                if not token:
                    raise EvalRunnerError(f"failed to generate JWT token for permission role: {query_permission}")
                query_tokens[query_permission] = token
            print(f"[eval] {idx}/{len(cases)} query {case.case_id}")
            details, payload = evaluate_case_assertions(
                api_base,
                token,
                case.query,
                expected_doc_id,
                case.expect_hit,
                acceptable_doc_ids,
                forbidden_doc_ids,
                query_top_k,
                max_strict_rank,
                args.required_consecutive_hits,
                args.poll_interval,
                case_max_wait,
            )
            answer_details = evaluate_answer_assertions(
                case,
                payload,
                acceptable_doc_ids,
                enable_answer_assertions=not args.disable_answer_assertions,
            )
            judge_details = None
            judge_error = ""
            judge_selected = judge_client is not None and (
                args.judge_max_cases == 0 or judge_attempted < args.judge_max_cases
            )
            if judge_selected:
                judge_attempted += 1
                source_contexts = [
                    str(source.get("content") or "")
                    for source in (payload or {}).get("sources") or []
                ]
                try:
                    judgement = judge_client.judge(
                        JudgeInput(
                            case_id=case.case_id,
                            question=case.query,
                            reference_answer=case.reference_answer or case.content,
                            system_answer=str((payload or {}).get("answer") or ""),
                            retrieved_contexts=source_contexts,
                            expect_hit=case.expect_hit,
                        )
                    )
                    judge_details = judgement.to_dict()
                    judge_faithfulness_scores.append(judgement.faithfulness_score)
                    judge_correctness_scores.append(judgement.correctness_score)
                    judge_relevance_scores.append(judgement.relevance_score)
                    if judgement.overall_pass:
                        judge_passed += 1
                except JudgeError as exc:
                    judge_errors += 1
                    judge_error = str(exc)
            # Observational relevance signals for gate-separability analysis.
            retrieval_info = (payload or {}).get("retrieval") or {}
            max_relevance = float(retrieval_info.get("max_relevance", 0.0) or 0.0)
            candidate_count = int(retrieval_info.get("candidate_count", 0) or 0)
            if case.expect_hit:
                positive_max_relevance.append(max_relevance)
            else:
                negative_max_relevance.append(max_relevance)

            retrieval_pass = bool(details["assertion_pass"])
            answer_pass = bool(answer_details["answer_assertion_pass"])
            final_pass = retrieval_pass and answer_pass
            final_reason = "all_assertions_passed"
            if not retrieval_pass:
                final_reason = f"retrieval:{details['assertion_reason']}"
            elif not answer_pass:
                final_reason = f"answer:{answer_details['answer_assertion_reason']}"

            if retrieval_pass:
                retrieval_assertion_passed += 1
            if answer_pass:
                answer_assertion_passed += 1
            if case.expect_hit:
                positive_total += 1
                if retrieval_pass:
                    positive_retrieval_passed += 1
                if final_pass:
                    positive_passed += 1
                if details["hit"]:
                    scores.append(float(details["score"]))
                if details["acceptable_hit"]:
                    acceptable_passed += 1
                    acceptable_scores.append(float(details["acceptable_score"]))
                for k in recall_hits:
                    if details[f"recall_at_{k}"]:
                        recall_hits[k] += 1
            else:
                negative_total += 1
                if retrieval_pass:
                    negative_retrieval_passed += 1
                if final_pass:
                    negative_passed += 1
            if final_pass:
                assertion_passed += 1

            # Accumulate token usage for cost reporting in the summary.
            prompt_tokens = int(details.get("prompt_tokens", 0) or 0)
            completion_tokens = int(details.get("completion_tokens", 0) or 0)
            total_prompt_tokens += prompt_tokens
            total_completion_tokens += completion_tokens
            if prompt_tokens or completion_tokens:
                query_token_counts.append(prompt_tokens + completion_tokens)

            eval_items.append(
                {
                    "case_id": case.case_id,
                    "query": case.query,
                    "expect_hit": case.expect_hit,
                    "expected_doc_id": expected_doc_id,
                    "acceptable_doc_ids": acceptable_doc_ids,
                    "forbidden_doc_ids": forbidden_doc_ids,
                    "prompt_tokens": prompt_tokens,
                    "completion_tokens": completion_tokens,
                    "hit": details["hit"],
                    "score": float(details["score"]),
                    "strict_rank": details["strict_rank"],
                    "max_strict_rank": max_strict_rank,
                    "strict_rank_requirement_met": details["strict_rank_requirement_met"],
                    "acceptable_hit": details["acceptable_hit"],
                    "acceptable_score": float(details["acceptable_score"]),
                    "acceptable_rank": details["acceptable_rank"],
                    "forbidden_hit": details["forbidden_hit"],
                    "forbidden_score": float(details["forbidden_score"]),
                    "forbidden_rank": details["forbidden_rank"],
                    "max_consecutive_hits": details["max_consecutive_hits"],
                    "retrieval_assertion_pass": retrieval_pass,
                    "retrieval_assertion_reason": details["assertion_reason"],
                    "answer_assertion_pass": answer_pass,
                    "answer_assertion_reason": answer_details["answer_assertion_reason"],
                    "answer_len": answer_details["answer_len"],
                    "answer_source_citation_ok": answer_details["answer_source_citation_ok"],
                    "answer_cited_doc_ids": answer_details["answer_cited_doc_ids"],
                    "assertion_pass": final_pass,
                    "assertion_reason": final_reason,
                    "recall_at_1": details["recall_at_1"],
                    "recall_at_3": details["recall_at_3"],
                    "recall_at_5": details["recall_at_5"],
                    "source_count": len((payload or {}).get("sources") or []),
                    "max_relevance": max_relevance,
                    "candidate_count": candidate_count,
                    "grounding_checked": bool(retrieval_info.get("grounding_checked", False)),
                    "grounding_passed": bool(retrieval_info.get("grounding_passed", True)),
                    "answer": str((payload or {}).get("answer") or "")[:600],
                    "source_doc_ids": [str(s.get("doc_id", "")) for s in (payload or {}).get("sources") or []],
                    "judge": judge_details,
                    "judge_error": judge_error,
                }
            )

        total = len(cases)
        hit_rate = positive_retrieval_passed / positive_total if positive_total else 0.0
        acceptable_hit_rate = acceptable_passed / positive_total if positive_total else 0.0
        recall_at_1 = recall_hits[1] / positive_total if positive_total else 0.0
        recall_at_3 = recall_hits[3] / positive_total if positive_total else 0.0
        recall_at_5 = recall_hits[5] / positive_total if positive_total else 0.0
        retrieval_pass_rate = retrieval_assertion_passed / total if total else 0.0
        answer_pass_rate = answer_assertion_passed / total if total else 0.0
        pass_rate = assertion_passed / total if total else 0.0
        avg_score = sum(scores) / len(scores) if scores else 0.0
        avg_acceptable_score = sum(acceptable_scores) / len(acceptable_scores) if acceptable_scores else 0.0
        judge_pass_rate = judge_passed / judge_attempted if judge_attempted else 0.0
        avg_judge_faithfulness = (
            sum(judge_faithfulness_scores) / len(judge_faithfulness_scores)
            if judge_faithfulness_scores
            else 0.0
        )
        avg_judge_correctness = (
            sum(judge_correctness_scores) / len(judge_correctness_scores)
            if judge_correctness_scores
            else 0.0
        )
        avg_judge_relevance = (
            sum(judge_relevance_scores) / len(judge_relevance_scores)
            if judge_relevance_scores
            else 0.0
        )

        result = {
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "tenant_id": tenant_id,
            "mock_port": resolved_mock_port if not profile.is_real else "",
            "compose_project": compose_project,
            "api_base": api_base,
            "model_mode": profile.mode,
            "models": {
                "embed_model": profile.embed_model,
                "embed_endpoint": profile.embed_endpoint,
                "embed_dimension": profile.embed_dim,
                "llm_model": profile.llm_model,
                "llm_endpoint": profile.llm_endpoint,
                "store_collection": profile.store_collection,
            },
            "summary": {
                "total_cases": total,
                "assertion_passed_cases": assertion_passed,
                "assertion_failed_cases": total - assertion_passed,
                "positive_cases": positive_total,
                "positive_passed_cases": positive_retrieval_passed,
                "positive_final_passed_cases": positive_passed,
                "negative_cases": negative_total,
                "negative_passed_cases": negative_retrieval_passed,
                "negative_final_passed_cases": negative_passed,
                "retrieval_assertion_passed_cases": retrieval_assertion_passed,
                "answer_assertion_passed_cases": answer_assertion_passed,
                "retrieval_pass_rate": retrieval_pass_rate,
                "answer_pass_rate": answer_pass_rate,
                "pass_rate": pass_rate,
                "hit_rate": hit_rate,
                "acceptable_passed_cases": acceptable_passed,
                "acceptable_hit_rate": acceptable_hit_rate,
                "recall_at_1": recall_at_1,
                "recall_at_3": recall_at_3,
                "recall_at_5": recall_at_5,
                "avg_score": avg_score,
                "avg_acceptable_score": avg_acceptable_score,
                "total_tokens": total_prompt_tokens + total_completion_tokens,
                "prompt_tokens": total_prompt_tokens,
                "completion_tokens": total_completion_tokens,
                "avg_tokens_per_query": (
                    (sum(query_token_counts) / len(query_token_counts))
                    if query_token_counts
                    else 0.0
                ),
                "estimated_cost_usd": estimate_llm_cost(
                    total_prompt_tokens, total_completion_tokens
                ),
                "threshold_hit_rate": args.min_hit_rate,
                "threshold_answer_pass_rate": args.min_answer_pass_rate,
                "threshold_pass_rate": args.min_pass_rate,
                "required_consecutive_hits": args.required_consecutive_hits,
                "negative_max_wait_seconds": args.negative_max_wait,
                "judge_enabled": args.judge,
                "judge_model": args.judge_model if args.judge else "",
                "judge_attempted_cases": judge_attempted,
                "judge_passed_cases": judge_passed,
                "judge_error_cases": judge_errors,
                "judge_pass_rate": judge_pass_rate,
                "judge_avg_faithfulness": avg_judge_faithfulness,
                "judge_avg_correctness": avg_judge_correctness,
                "judge_avg_relevance": avg_judge_relevance,
                "judge_threshold_pass_rate": args.judge_min_pass_rate,
                "judge_threshold_faithfulness": args.judge_min_faithfulness,
                "positive_max_relevance_dist": summarize_dist(positive_max_relevance),
                "negative_max_relevance_dist": summarize_dist(negative_max_relevance),
            },
            "cases": eval_items,
        }

        json_path, md_path = write_report(report_dir, result)
        print(f"[eval] report json: {json_path}")
        print(f"[eval] report md:   {md_path}")

        if pass_rate < args.min_pass_rate:
            print(
                f"[eval] FAILED: pass_rate={pass_rate:.2%} < min_pass_rate={args.min_pass_rate:.2%}",
                file=sys.stderr,
            )
            return 1
        if answer_pass_rate < args.min_answer_pass_rate:
            print(
                f"[eval] FAILED: answer_pass_rate={answer_pass_rate:.2%} < min_answer_pass_rate={args.min_answer_pass_rate:.2%}",
                file=sys.stderr,
            )
            return 1
        if hit_rate < args.min_hit_rate:
            print(
                f"[eval] FAILED: hit_rate={hit_rate:.2%} < min_hit_rate={args.min_hit_rate:.2%}",
                file=sys.stderr,
            )
            return 1
        if args.judge and judge_errors > 0:
            print(f"[eval] FAILED: judge_error_cases={judge_errors}", file=sys.stderr)
            return 1
        if args.judge and judge_pass_rate < args.judge_min_pass_rate:
            print(
                f"[eval] FAILED: judge_pass_rate={judge_pass_rate:.2%} "
                f"< judge_min_pass_rate={args.judge_min_pass_rate:.2%}",
                file=sys.stderr,
            )
            return 1
        if args.judge and avg_judge_faithfulness < args.judge_min_faithfulness:
            print(
                f"[eval] FAILED: judge_avg_faithfulness={avg_judge_faithfulness:.2f} "
                f"< judge_min_faithfulness={args.judge_min_faithfulness:.2f}",
                file=sys.stderr,
            )
            return 1

        pass_message = (
            "[eval] PASS: "
            f"pass_rate={pass_rate:.2%}, "
            f"answer_pass_rate={answer_pass_rate:.2%}, "
            f"hit_rate={hit_rate:.2%}, "
            f"acceptable_hit_rate={acceptable_hit_rate:.2%}, "
            f"recall@5={recall_at_5:.2%}, "
            f"avg_score={avg_score:.4f}"
        )
        if args.judge:
            pass_message += (
                f", judge_pass_rate={judge_pass_rate:.2%}, "
                f"judge_faithfulness={avg_judge_faithfulness:.2f}/5"
            )
        print(pass_message)
        return 0

    finally:
        cleanup()


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except KeyboardInterrupt:
        raise SystemExit(130)
    except EvalRunnerError as exc:
        print(f"[eval] ERROR: {exc}", file=sys.stderr)
        raise SystemExit(2)
