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
import math
import mimetypes
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
from urllib.parse import quote

from judge_eval import JudgeClient, JudgeConfig, JudgeError, JudgeInput


ROOT = Path(__file__).resolve().parent.parent
DEFAULT_GOLDEN_SET = ROOT / "docs" / "evals" / "golden-set.json"
DEFAULT_REPORT_DIR = ROOT / "docs" / "evals" / "reports"
EVAL_COMPOSE_FILE = ROOT / "docker-compose.eval.yml"
QUALITY_LATEST_NAME = "latest.json"
BAKED_QUALITY_LATEST = ROOT / "web" / "public" / "evals" / "latest.json"
QUALITY_RECALL_NOTES = {
    1: "目标文档排第 1 的比例",
    3: "目标在前 3 名",
    5: "目标在前 5 名（可找到）",
}
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
class EvalDocument:
    document_id: str
    filename: str
    permission: str
    content: str
    metadata: Dict[str, str] = field(default_factory=dict)
    source_path: Path | None = None


@dataclass
class EvalCase:
    case_id: str
    query: str
    document_id: str = ""
    filename: str = ""
    permission: str = "internal"
    content: str = ""
    metadata: Dict[str, str] = field(default_factory=dict)
    query_permission: str = ""
    acceptable_doc_ids: List[str] = field(default_factory=list)
    required_doc_ids: List[str] = field(default_factory=list)
    expect_hit: bool = True
    max_strict_rank: int = 0
    query_top_k: int = 0
    must_not_hit_doc_ids: List[str] = field(default_factory=list)
    require_source_citation: bool = True
    answer_must_include: List[str] = field(default_factory=list)
    answer_must_not_include: List[str] = field(default_factory=list)
    reference_answer: str = ""


@dataclass
class EvalDataset:
    version: str
    name: str
    dataset_type: str
    evaluation_scope: str
    provenance: Dict[str, Any]
    documents: List[EvalDocument]
    cases: List[EvalCase]


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
            "BOOTSTRAP_ADMIN_TENANT": tenant_id,
            "BOOTSTRAP_ADMIN_USERNAME": "eval-admin",
            "BOOTSTRAP_ADMIN_PASSWORD": "eval-admin-password-2026",
            # Isolated eval services expose only aggregate stage diagnostics;
            # this mirrors docker-compose.eval.yml for provenance hashing.
            "RETRIEVAL_DIAGNOSTICS_ENABLED": "true",
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


def resolved_eval_configuration(
    env: Dict[str, str],
    *,
    query_top_k: int,
    query_timeout_seconds: float = 30.0,
    required_consecutive_hits: int = 2,
    poll_interval_seconds: float = 2.0,
    negative_max_wait_seconds: int = 30,
) -> Dict[str, Any]:
    """Return the non-secret resolved settings that make A/B runs comparable."""

    def env_bool(name: str, default: bool) -> bool:
        value = str(env.get(name, str(default))).strip().lower()
        return value in {"1", "true", "yes", "on"}

    def env_int(name: str, default: int) -> int:
        try:
            return int(str(env.get(name, default)).strip())
        except ValueError as exc:
            raise EvalRunnerError(f"{name} must be an integer") from exc

    profiles = sorted(
        value.strip()
        for value in str(env.get("COMPOSE_PROFILES", "")).split(",")
        if value.strip()
    )
    config: Dict[str, Any] = {
        "retrieval_enable_es": env_bool("RETRIEVAL_ENABLE_ES", True),
        "retrieval_enable_rerank": env_bool("RETRIEVAL_ENABLE_RERANK", False),
        "retrieval_diagnostics_enabled": env_bool("RETRIEVAL_DIAGNOSTICS_ENABLED", True),
        "retrieval_rerank_policy": str(env.get("RETRIEVAL_RERANK_POLICY", "auto")).strip().lower(),
        "retrieval_candidate_k": env_int("RETRIEVAL_CANDIDATE_K", 50),
        "retrieval_final_top_k": env_int("RETRIEVAL_FINAL_TOP_K", 5),
        "retrieval_timeout": str(env.get("RETRIEVAL_TIMEOUT", "300ms")).strip(),
        "retrieval_min_relevance": str(env.get("RETRIEVAL_MIN_RELEVANCE", "0")).strip(),
        "retrieval_exact_schema_fields": sorted(
            value.strip()
            for value in str(
                env.get(
                    "RETRIEVAL_EXACT_SCHEMA_FIELDS",
                    "doc_id,chunk_id,order_id,order_no,contract_id,contract_no,"
                    "ticket_id,invoice_no,trace_id,request_id,customer_ref,email,"
                    "phone,sku,user_id",
                )
            ).split(",")
            if value.strip()
        ),
        "query_top_k": int(query_top_k),
        "query_timeout_seconds": float(query_timeout_seconds),
        "required_consecutive_hits": int(required_consecutive_hits),
        "poll_interval_seconds": float(poll_interval_seconds),
        "negative_max_wait_seconds": int(negative_max_wait_seconds),
        "retrieval_grounding_check": env_bool("RETRIEVAL_GROUNDING_CHECK", True),
        "retrieval_grounding_low_bound": str(
            env.get("RETRIEVAL_GROUNDING_LOW_BOUND", "0.45")
        ).strip(),
        "retrieval_grounding_high_bound": str(
            env.get("RETRIEVAL_GROUNDING_HIGH_BOUND", "0.70")
        ).strip(),
        "semantic_cache_enabled": env_bool("SEMANTIC_CACHE_ENABLED", True),
        "semantic_cache_threshold": str(env.get("SEMANTIC_CACHE_THRESHOLD", "0.92")).strip(),
        "semantic_cache_ttl": str(env.get("SEMANTIC_CACHE_TTL", "10m")).strip(),
        "llm_timeout": str(env.get("LLM_TIMEOUT", "30s")).strip(),
        "llm_max_tokens": env_int("LLM_MAX_TOKENS", 1024),
        "llm_max_context_chars": env_int("LLM_MAX_CONTEXT_CHARS", 20000),
        "http_handler_timeout": str(env.get("HTTP_HANDLER_TIMEOUT", "60s")).strip(),
        "http_write_timeout": str(env.get("HTTP_WRITE_TIMEOUT", "60s")).strip(),
        "prompt_version": str(env.get("PROMPT_VERSION", "v1")).strip(),
        "pipeline_max_workers": env_int("PIPELINE_MAX_WORKERS", 10),
        "pipeline_batch_size": env_int("PIPELINE_BATCH_SIZE", 10),
        "pipeline_stage_timeout": str(env.get("PIPELINE_STAGE_TIMEOUT", "180s")).strip(),
        "pipeline_timeout": str(env.get("PIPELINE_TIMEOUT", "15m")).strip(),
        "embed_timeout": str(env.get("EMBED_TIMEOUT", "30s")).strip(),
        "embed_max_retries": env_int("EMBED_MAX_RETRIES", 5),
        "rerank_request_model": str(env.get("RERANK_MODEL", "bge-reranker-base")).strip(),
        "reranker_backend": str(env.get("RERANKER_BACKEND", "cross-encoder")).strip(),
        "reranker_model": str(
            env.get("RERANKER_MODEL", "cross-encoder/ms-marco-MiniLM-L6-v2")
        ).strip(),
        "reranker_device": str(env.get("RERANKER_DEVICE", "cpu")).strip(),
        "reranker_batch_size": env_int("RERANKER_BATCH_SIZE", 16),
        "reranker_max_documents": env_int("RERANKER_MAX_DOCUMENTS", 100),
        "reranker_max_document_chars": env_int("RERANKER_MAX_DOCUMENT_CHARS", 12000),
        "compose_profiles": profiles,
    }
    serialized = json.dumps(config, ensure_ascii=True, sort_keys=True, separators=(",", ":"))
    config["sha256"] = hashlib.sha256(serialized.encode("utf-8")).hexdigest()
    return config


def estimate_llm_cost(prompt_tokens: int, completion_tokens: int) -> float:
    """Estimated LLM spend for a run from LLM_PRICE_* env vars (USD per 1K)."""
    prompt_price = float(os.getenv("LLM_PRICE_PROMPT_PER_1K", "0") or 0)
    completion_price = float(os.getenv("LLM_PRICE_COMPLETION_PER_1K", "0") or 0)
    if prompt_price <= 0 and completion_price <= 0:
        return 0.0
    return prompt_tokens / 1000 * prompt_price + completion_tokens / 1000 * completion_price


def dataset_digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_upload_map(
    path: Path,
    *,
    tenant_id: str,
    dataset_path: Path,
    uploaded_doc_ids: Dict[str, str],
) -> None:
    """Persist stable source-to-registry IDs so a comparison run can reuse vectors."""
    path.parent.mkdir(parents=True, exist_ok=True)
    payload = {
        "version": 1,
        "tenant_id": tenant_id,
        "dataset_sha256": dataset_digest(dataset_path),
        "documents": dict(sorted(uploaded_doc_ids.items())),
    }
    path.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def load_upload_map(
    path: Path,
    *,
    dataset_path: Path,
    expected_document_ids: Sequence[str],
) -> Tuple[str, Dict[str, str]]:
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise EvalRunnerError(f"invalid upload map {path}: {exc}") from exc
    if payload.get("version") != 1:
        raise EvalRunnerError(f"unsupported upload map version in {path}")
    if payload.get("dataset_sha256") != dataset_digest(dataset_path):
        raise EvalRunnerError("upload map dataset digest does not match --golden-set")
    tenant_id = str(payload.get("tenant_id", "")).strip()
    raw_documents = payload.get("documents")
    if not tenant_id or not isinstance(raw_documents, dict):
        raise EvalRunnerError(f"upload map is missing tenant_id or documents: {path}")
    documents = {
        str(source_id).strip(): str(doc_id).strip()
        for source_id, doc_id in raw_documents.items()
        if str(source_id).strip() and str(doc_id).strip()
    }
    missing = sorted(set(expected_document_ids) - set(documents))
    if missing:
        raise EvalRunnerError(f"upload map is missing dataset documents: {missing[:10]}")
    return tenant_id, documents


def elasticsearch_count_url(es_url: str, es_index: str) -> str:
    """Build the Elasticsearch _count URL for a host-exposed eval stack."""
    return f"{str(es_url).strip().rstrip('/')}/{str(es_index).strip().strip('/')}/_count"


def wait_for_es_sync(
    env: Dict[str, str],
    expected_docs: int,
    timeout_sec: int = 120,
    poll_sec: int = 2,
    es_url: str = "",
) -> None:
    """Block until Elasticsearch has indexed all expected documents.

    The worker writes to Qdrant synchronously but feeds ES through an async
    retry queue. Exact-keyword queries route ES with 0.75 weight, so a document
    that is present in Qdrant but not yet in ES scores 0 on the BM25 side and is
    pushed out of the top-K by RRF fusion — the eval then reports a false
    retrieval timeout. Polling ES's document count closes that race.
    """
    es_index = env.get("ES_INDEX", "documents_text")
    host_url = str(es_url or "").strip()
    deadline = time.time() + timeout_sec
    last_count = -1
    while time.time() < deadline:
        # ES may still be starting up; a timed-out curl is "not ready yet",
        # not an error — keep polling until the deadline.
        try:
            if host_url:
                code, payload = http_json(
                    "GET",
                    elasticsearch_count_url(host_url, es_index),
                    timeout=10.0,
                )
                last_count = int(payload.get("count", 0)) if code == 200 else -1
            else:
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


def should_start_compose(api_base: str) -> bool:
    """Start an isolated eval stack unless the caller already provided an API."""
    return not bool(str(api_base or "").strip())


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
    media_type: str = "text/plain",
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
            f"Content-Type: {media_type}\r\n\r\n"
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


def upload_document(api_base: str, token: str, document: EvalDocument) -> str:
    if document.source_path is not None:
        content = document.source_path.read_bytes()
        media_type = mimetypes.guess_type(document.filename)[0] or "application/octet-stream"
    else:
        content = document.content.encode("utf-8")
        media_type = "text/plain"
    body, ctype = create_multipart_body(
        "file",
        document.filename,
        content,
        document.permission,
        document.metadata,
        media_type,
    )
    headers = {
        "Authorization": f"Bearer {token}",
        "Content-Type": ctype,
    }
    code, payload = http_json("POST", f"{api_base}/v1/upload", headers=headers, body=body, timeout=120.0)
    if code != 202:
        raise EvalRunnerError(f"upload failed for {document.document_id}, status={code}, payload={payload}")
    doc_id = str(payload.get("doc_id", ""))
    if not doc_id:
        raise EvalRunnerError(f"upload response missing doc_id for {document.document_id}: {payload}")
    return doc_id


def upload_case(api_base: str, token: str, case: EvalCase) -> str:
    """Backward-compatible upload helper for case-per-document callers."""
    return upload_document(
        api_base,
        token,
        EvalDocument(
            document_id=case.document_id or case.case_id,
            filename=case.filename,
            permission=case.permission,
            content=case.content,
            metadata=case.metadata,
        ),
    )


def upload_role_for_permission(permission: str) -> str:
    """Return the least-privileged eval identity allowed to create a fixture."""
    normalized = permission.strip().lower()
    if normalized in {"public", "internal"}:
        return "user"
    if normalized == "confidential":
        return "admin"
    raise EvalRunnerError(f"unsupported document permission: {permission}")


def login_eval_user(api_base: str, username: str, password: str) -> str:
    body = json.dumps({"username": username, "password": password}).encode("utf-8")
    code, payload = http_json("POST", f"{api_base}/v1/auth/login", headers={"Content-Type": "application/json"}, body=body)
    if code != 200 or not payload.get("token"):
        raise EvalRunnerError(f"login failed for {username}, status={code}, payload={payload}")
    return str(payload["token"])


def create_eval_user(api_base: str, admin_token: str, username: str, password: str, role: str) -> None:
    body = json.dumps({"username": username, "password": password, "role": role, "active": True}).encode("utf-8")
    code, payload = http_json("POST", f"{api_base}/v1/users", headers={
        "Authorization": f"Bearer {admin_token}", "Content-Type": "application/json",
    }, body=body)
    if code not in {201, 409}:
        raise EvalRunnerError(f"create user failed for {username}, status={code}, payload={payload}")


def verify_document_published(api_base: str, admin_token: str, doc_id: str) -> None:
    code, document = http_json(
        "GET",
        f"{api_base}/v1/documents/{quote(doc_id, safe='')}",
        headers={"Authorization": f"Bearer {admin_token}"},
    )
    if code != 200 or document.get("publication_status") != "published":
        raise EvalRunnerError(
            f"document was not auto-published after completed ETL: {doc_id}, "
            f"status={code}, payload={document}"
        )


def wait_for_document_tasks(
    api_base: str,
    token: str,
    doc_ids: Sequence[str],
    *,
    timeout_sec: int = 180,
    poll_sec: float = 2.0,
) -> None:
    """Wait until every uploaded document has completed ETL before publishing.

    ES document count is not a sufficient readiness signal: the registry can
    still be in ``queued``/``processing`` while only a subset of chunks has
    reached Elasticsearch. Publishing during that window returns 409 and can
    make a large real-model baseline fail before retrieval starts.
    """
    pending = {str(doc_id).strip() for doc_id in doc_ids if str(doc_id).strip()}
    if not pending:
        return
    headers = {"Authorization": f"Bearer {token}"}
    deadline = time.time() + max(timeout_sec, 1)
    backoff_sec = 0.05
    while pending and time.time() < deadline:
        rate_limited = False
        for doc_id in tuple(pending):
            code, payload = http_json(
                "GET",
                f"{api_base}/v1/tasks/{quote(doc_id, safe='')}",
                headers=headers,
                timeout=15.0,
            )
            if code == 200:
                status = str(payload.get("status", "")).strip().lower()
                if status == "completed":
                    pending.remove(doc_id)
                elif status == "failed":
                    stage = payload.get("stage", "")
                    detail = payload.get("error", "")
                    raise EvalRunnerError(
                        f"document processing failed for {doc_id}, stage={stage}, error={detail}"
                    )
            elif code == 429:
                rate_limited = True
                # The limiter is tenant-wide. Once its burst is exhausted,
                # scanning the rest of the batch only produces more 429s.
                break
            elif code not in (0, 404):
                raise EvalRunnerError(
                    f"document task status failed for {doc_id}, status={code}, payload={payload}"
                )
        if pending:
            if rate_limited:
                time.sleep(backoff_sec)
                backoff_sec = min(backoff_sec * 2, 2.0)
            else:
                backoff_sec = 0.05
                time.sleep(max(poll_sec, 0.05))
    if pending:
        raise EvalRunnerError(
            f"document processing timeout after {max(timeout_sec, 1)}s; pending={sorted(pending)[:10]}"
        )
    print(f"[eval] document processing complete: {len(doc_ids)} documents")


def query_case(
    api_base: str,
    token: str,
    query: str,
    top_k: int = 5,
    timeout_seconds: float = 30.0,
    diagnostic_required_doc_ids: Sequence[str] = (),
    retrieval_only: bool = False,
) -> Tuple[int, Dict[str, Any]]:
    request_payload: Dict[str, Any] = {"question": query, "top_k": top_k}
    diagnostic_ids = sorted({str(value).strip() for value in diagnostic_required_doc_ids if str(value).strip()})
    if diagnostic_ids:
        request_payload["diagnostic_required_doc_ids"] = diagnostic_ids
    if retrieval_only:
        request_payload["retrieval_only"] = True
    payload = json.dumps(request_payload, ensure_ascii=False).encode("utf-8")
    headers = {
        "Authorization": f"Bearer {token}",
        "Content-Type": "application/json",
    }
    return http_json(
        "POST",
        f"{api_base}/v1/query",
        headers=headers,
        body=payload,
        timeout=timeout_seconds,
    )


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


def resolve_required_doc_ids(
    case: EvalCase,
    uploaded_doc_ids: Dict[str, str],
) -> List[str]:
    return resolve_doc_refs(case.required_doc_ids, uploaded_doc_ids)


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
    query_timeout_seconds: float = 30.0,
    required_doc_ids: Sequence[str] = (),
    retrieval_only: bool = False,
) -> Tuple[Dict[str, Any], Dict[str, Any]]:
    deadline = time.time() + max_wait_seconds
    last_payload: Dict[str, Any] = {}
    acceptable_set = set(acceptable_doc_ids)
    required_set = set(required_doc_ids)
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
    required_docs_hit = not required_set
    required_doc_ranks: Dict[str, int] = {}
    query_attempts = 0
    query_latency_ms = 0.0
    query_successful = False
    last_query_status = 0

    while time.time() < deadline:
        request_started = time.perf_counter()
        code, payload = query_case(
            api_base,
            token,
            query,
            top_k=query_top_k,
            timeout_seconds=query_timeout_seconds,
            diagnostic_required_doc_ids=required_doc_ids,
            retrieval_only=retrieval_only,
        )
        request_latency_ms = (time.perf_counter() - request_started) * 1000
        query_attempts += 1
        last_query_status = code
        if code == 200:
            query_successful = True
            query_latency_ms = request_latency_ms
            last_payload = payload
            sources = payload.get("sources") or []
            s_rank, s_score = find_first_match(sources, strict_set)
            a_rank, a_score = find_first_match(sources, acceptable_set)
            f_rank, f_score = find_first_match(sources, forbidden_set)
            current_required_ranks = {
                str(src.get("doc_id", "")): index
                for index, src in enumerate(sources, start=1)
                if str(src.get("doc_id", "")) in required_set
            }
            current_required_hit = required_set.issubset(current_required_ranks)
            if current_required_hit:
                required_docs_hit = True
                required_doc_ranks = current_required_ranks

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
                        required_recall_hit = not required_set or (
                            current_required_hit
                            and all(rank <= k for rank in current_required_ranks.values())
                        )
                        if a_rank <= k and required_recall_hit:
                            recall_hits[k] = True
            if f_rank > 0:
                forbidden_hit = True
                if forbidden_rank == 0 or f_rank < forbidden_rank:
                    forbidden_rank = f_rank
                    forbidden_score = f_score

            required_rank_ok = current_required_hit and (
                max_strict_rank <= 0
                or all(rank <= max_strict_rank for rank in current_required_ranks.values())
            )
            rank_ok = (
                s_rank > 0
                and (max_strict_rank <= 0 or s_rank <= max_strict_rank)
                and (not required_set or required_rank_ok)
            )
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

            if retrieval_only:
                assertion_reason = "retrieval_only_observation"
                break

        time.sleep(poll_interval_seconds)

    if not expect_hit and not strict_hit and not forbidden_hit:
        assertion_pass = True
        assertion_reason = "no_forbidden_or_strict_hit_within_window"
    if expect_hit and strict_hit and max_strict_rank > 0 and not strict_rank_requirement_met:
        assertion_reason = f"strict_hit_but_rank_gt_{max_strict_rank}"
    if expect_hit and strict_hit and max_consecutive_hits < required_consecutive_hits:
        if required_set and not required_docs_hit:
            assertion_reason = "missing_required_documents"
        else:
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
        "required_docs_hit": required_docs_hit,
        "required_doc_ranks": required_doc_ranks,
        "max_consecutive_hits": max_consecutive_hits,
        "assertion_pass": assertion_pass,
        "assertion_reason": assertion_reason,
        "recall_at_1": recall_hits[1],
        "recall_at_3": recall_hits[3],
        "recall_at_5": recall_hits[5],
        "query_attempts": query_attempts,
        "query_latency_ms": round(query_latency_ms, 3),
        "query_successful": query_successful,
        "last_query_status": last_query_status,
        "retrieval_stage_diagnostics": (
            ((last_payload or {}).get("retrieval") or {}).get("stage_diagnostics") or {}
        ),
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
    required_doc_ids: Sequence[str] = (),
) -> Dict[str, Any]:
    answer = str((payload or {}).get("answer") or "").strip()
    answer_lower = answer.lower()
    key_facts = [
        token.strip()
        for token in case.answer_must_include
        if token.strip() and token.strip() not in CITATION_MARKER_TOKENS
    ]
    metric_details = {
        "key_fact_check_eligible": bool(
            enable_answer_assertions and case.expect_hit and key_facts
        ),
        "key_fact_assertion_pass": bool(
            key_facts and all(fact.lower() in answer_lower for fact in key_facts)
        ),
        "safety_refusal_eligible": bool(enable_answer_assertions and not case.expect_hit),
        "safety_refusal_pass": bool(
            not case.expect_hit
            and answer
            and any(marker in answer for marker in NEGATIVE_FALLBACK_MARKERS)
        ),
    }

    def with_metrics(result: Dict[str, Any]) -> Dict[str, Any]:
        result.update(metric_details)
        return result

    if not enable_answer_assertions:
        return with_metrics({
            "answer_assertion_pass": True,
            "answer_assertion_reason": "answer_assertions_disabled",
            "answer_len": 0,
            "answer_source_citation_ok": True,
            "answer_cited_doc_ids": [],
        })

    if not answer:
        return with_metrics({
            "answer_assertion_pass": False,
            "answer_assertion_reason": "answer_empty",
            "answer_len": 0,
            "answer_source_citation_ok": False,
            "answer_cited_doc_ids": [],
        })

    sources = (payload or {}).get("sources") or []
    source_doc_ids = []
    for src in sources:
        doc_id = str(src.get("doc_id", "")).strip()
        if doc_id:
            source_doc_ids.append(doc_id)
    cited = sorted({doc_id for doc_id in set(source_doc_ids).union(acceptable_doc_ids) if doc_id and doc_id in answer})
    required_citations = set(required_doc_ids)
    source_citation_ok = (not case.require_source_citation) or (
        required_citations.issubset(cited) if required_citations else bool(cited)
    )

    if case.expect_hit:
        anchor = extract_anchor_token(case.query)
        if anchor and anchor not in answer_lower:
            return with_metrics({
                "answer_assertion_pass": False,
                "answer_assertion_reason": "missing_anchor_token",
                "answer_len": len(answer),
                "answer_source_citation_ok": source_citation_ok,
                "answer_cited_doc_ids": cited,
            })
        if not source_citation_ok:
            return with_metrics({
                "answer_assertion_pass": False,
                "answer_assertion_reason": (
                    "missing_required_source_citation"
                    if required_citations
                    else "missing_source_citation"
                ),
                "answer_len": len(answer),
                "answer_source_citation_ok": source_citation_ok,
                "answer_cited_doc_ids": cited,
            })
    else:
        if not any(marker in answer for marker in NEGATIVE_FALLBACK_MARKERS):
            return with_metrics({
                "answer_assertion_pass": False,
                "answer_assertion_reason": "negative_case_missing_not_found_fallback",
                "answer_len": len(answer),
                "answer_source_citation_ok": source_citation_ok,
                "answer_cited_doc_ids": cited,
            })

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
                return with_metrics({
                    "answer_assertion_pass": False,
                    "answer_assertion_reason": "missing_source_citation",
                    "answer_len": len(answer),
                    "answer_source_citation_ok": source_citation_ok,
                    "answer_cited_doc_ids": cited,
                })
            continue
        if normalized.lower() not in answer_lower:
            return with_metrics({
                "answer_assertion_pass": False,
                "answer_assertion_reason": f"answer_missing_phrase:{normalized}",
                "answer_len": len(answer),
                "answer_source_citation_ok": source_citation_ok,
                "answer_cited_doc_ids": cited,
            })

    for token in case.answer_must_not_include:
        normalized = token.strip()
        if normalized and normalized.lower() in answer_lower:
            return with_metrics({
                "answer_assertion_pass": False,
                "answer_assertion_reason": f"answer_contains_forbidden_phrase:{normalized}",
                "answer_len": len(answer),
                "answer_source_citation_ok": source_citation_ok,
                "answer_cited_doc_ids": cited,
            })

    return with_metrics({
        "answer_assertion_pass": True,
        "answer_assertion_reason": "answer_assertions_passed",
        "answer_len": len(answer),
        "answer_source_citation_ok": source_citation_ok,
        "answer_cited_doc_ids": cited,
    })


def eval_case_from_raw(raw: Dict[str, Any], *, document_id: str = "") -> EvalCase:
    return EvalCase(
        case_id=str(raw["id"]),
        query=str(raw["query"]),
        document_id=document_id,
        filename=str(raw.get("filename", "")),
        permission=str(raw.get("permission", "internal")),
        content=str(raw.get("content", "")),
        metadata={str(k): str(v) for k, v in raw.get("metadata", {}).items()},
        query_permission=str(raw.get("query_permission", "")),
        acceptable_doc_ids=[str(v) for v in raw.get("acceptable_doc_ids", [])],
        required_doc_ids=[str(v) for v in raw.get("required_doc_ids", [])],
        expect_hit=bool(raw.get("expect_hit", True)),
        max_strict_rank=int(raw.get("max_strict_rank", 0)),
        query_top_k=int(raw.get("query_top_k", 0)),
        must_not_hit_doc_ids=[str(v) for v in raw.get("must_not_hit_doc_ids", [])],
        require_source_citation=bool(raw.get("require_source_citation", raw.get("expect_hit", True))),
        answer_must_include=[str(v) for v in raw.get("answer_must_include", [])],
        answer_must_not_include=[str(v) for v in raw.get("answer_must_not_include", [])],
        reference_answer=str(raw.get("reference_answer", "")),
    )


def resolve_document_source_path(raw_path: str, document_id: str) -> Path | None:
    value = raw_path.strip()
    if not value:
        return None
    candidate = Path(value)
    if candidate.is_absolute():
        raise EvalRunnerError(f"eval document {document_id} source_path must stay within repository")
    resolved_root = ROOT.resolve()
    resolved = (resolved_root / candidate).resolve()
    try:
        resolved.relative_to(resolved_root)
    except ValueError as exc:
        raise EvalRunnerError(
            f"eval document {document_id} source_path must stay within repository"
        ) from exc
    if not resolved.is_file():
        raise EvalRunnerError(f"eval document {document_id} source_path does not exist: {value}")
    return resolved


def verify_document_source_hash(
    source_path: Path | None, metadata: Dict[str, str], document_id: str
) -> None:
    expected = metadata.get("source_sha256", "").strip().lower()
    if source_path is None or not expected:
        return
    actual = hashlib.sha256(source_path.read_bytes()).hexdigest()
    if actual != expected:
        raise EvalRunnerError(f"eval document {document_id} source_sha256 does not match source_path")


def load_eval_dataset(path: Path) -> EvalDataset:
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict):
        raise EvalRunnerError("golden set root must be an object")
    raw_cases = data.get("cases", [])
    if not isinstance(raw_cases, list) or not raw_cases:
        raise EvalRunnerError("golden set is empty")

    if str(data.get("version", "")) == "2.0" or "documents" in data:
        raw_documents = data.get("documents")
        if not isinstance(raw_documents, list) or not raw_documents:
            raise EvalRunnerError("eval protocol v2 requires documents")
        documents: List[EvalDocument] = []
        document_ids: Set[str] = set()
        for raw in raw_documents:
            if not isinstance(raw, dict):
                raise EvalRunnerError("eval protocol v2 document must be an object")
            document_id = str(raw.get("id", "")).strip()
            if not document_id or document_id in document_ids:
                raise EvalRunnerError("eval protocol v2 document ids must be non-empty and unique")
            filename = str(raw.get("filename", "")).strip()
            content = str(raw.get("content", "")).strip()
            if not filename or not content:
                raise EvalRunnerError(f"eval document {document_id} requires filename and content")
            metadata = {str(k): str(v) for k, v in raw.get("metadata", {}).items()}
            source_path = resolve_document_source_path(str(raw.get("source_path", "")), document_id)
            verify_document_source_hash(source_path, metadata, document_id)
            document_ids.add(document_id)
            documents.append(
                EvalDocument(
                    document_id=document_id,
                    filename=filename,
                    permission=str(raw.get("permission", "internal")),
                    content=content,
                    metadata=metadata,
                    source_path=source_path,
                )
            )
        cases: List[EvalCase] = []
        for raw in raw_cases:
            if not isinstance(raw, dict):
                raise EvalRunnerError("eval protocol v2 case must be an object")
            document_id = str(raw.get("document_id", "")).strip()
            if document_id not in document_ids:
                raise EvalRunnerError(f"eval case {raw.get('id', '')} references unknown document_id")
            for field_name in ("acceptable_doc_ids", "required_doc_ids"):
                refs = [str(value) for value in raw.get(field_name, [])]
                unknown_refs = sorted(set(refs).difference(document_ids))
                if unknown_refs:
                    raise EvalRunnerError(
                        f"eval case {raw.get('id', '')} has unknown {field_name}"
                    )
            cases.append(eval_case_from_raw(raw, document_id=document_id))
        provenance = data.get("provenance", {})
        if not isinstance(provenance, dict):
            raise EvalRunnerError("eval protocol v2 provenance must be an object")
        return EvalDataset(
            version="2.0",
            name=str(data.get("name", path.stem)),
            dataset_type=str(data.get("dataset_type", "unspecified")),
            evaluation_scope=str(data.get("evaluation_scope", "answer_and_retrieval")),
            provenance=dict(provenance),
            documents=documents,
            cases=cases,
        )

    legacy_cases = [eval_case_from_raw(raw, document_id=str(raw["id"])) for raw in raw_cases]
    legacy_documents = [
        EvalDocument(
            document_id=case.case_id,
            filename=case.filename,
            permission=case.permission,
            content=case.content,
            metadata=case.metadata,
        )
        for case in legacy_cases
    ]
    return EvalDataset(
        version=str(data.get("version", "1.0")),
        name=str(data.get("name", path.stem)),
        dataset_type=str(data.get("dataset_type", "synthetic_regression")),
        evaluation_scope=str(data.get("evaluation_scope", "answer_and_retrieval")),
        provenance=dict(data.get("provenance", {})) if isinstance(data.get("provenance", {}), dict) else {},
        documents=legacy_documents,
        cases=legacy_cases,
    )


def load_cases(path: Path) -> List[EvalCase]:
    """Backward-compatible case loader used by existing tests and callers."""
    return load_eval_dataset(path).cases


def filter_eval_cases(cases: Sequence[EvalCase], cohort: str = "") -> List[EvalCase]:
    """Select one evaluation cohort while retaining the complete document corpus."""
    normalized = str(cohort).strip()
    if not normalized:
        return list(cases)
    selected = [
        case for case in cases
        if str(case.metadata.get("evaluation_cohort", "")).strip() == normalized
    ]
    if not selected:
        raise EvalRunnerError(f"evaluation cohort has no cases: {normalized}")
    return selected


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


def summarize_cohorts(items: Sequence[Dict[str, Any]]) -> Dict[str, Dict[str, Any]]:
    """Aggregate evaluator observations without exposing private case content."""

    def rate(group: Sequence[Dict[str, Any]], eligible_key: str, passed_key: str) -> Tuple[int, float]:
        eligible = [item for item in group if bool(item.get(eligible_key))]
        passed = sum(bool(item.get(passed_key)) for item in eligible)
        return len(eligible), (passed / len(eligible) if eligible else 0.0)

    def latency(values: Sequence[float]) -> Dict[str, Any]:
        ordered = sorted(float(value) for value in values if float(value) >= 0)
        if not ordered:
            return {"count": 0}

        def nearest_rank(percentile: float) -> float:
            index = max(0, math.ceil(percentile * len(ordered)) - 1)
            return round(ordered[index], 3)

        return {
            "count": len(ordered),
            "p50": nearest_rank(0.50),
            "p95": nearest_rank(0.95),
            "mean": round(sum(ordered) / len(ordered), 3),
        }

    def stage_diagnostics(group: Sequence[Dict[str, Any]]) -> Dict[str, Any]:
        observed = [
            item.get("retrieval_stage_diagnostics") or {}
            for item in group
            if item.get("retrieval_stage_diagnostics")
        ]
        if not observed:
            return {}

        def coverage_rate(values: Sequence[Dict[str, Any]]) -> Dict[str, Any]:
            eligible = [value for value in values if int(value.get("required_document_count", 0) or 0) > 0]
            return {
                "eligible_cases": len(eligible),
                "all_required_hit_rate": (
                    sum(bool(value.get("all_required_hit")) for value in eligible) / len(eligible)
                    if eligible
                    else 0.0
                ),
            }

        backend_names = sorted(
            {
                str(name)
                for diagnostic in observed
                for name in (diagnostic.get("backend") or {})
            }
        )
        return {
            "eligible_cases": len(observed),
            "backend": {
                name: coverage_rate([
                    (diagnostic.get("backend") or {}).get(name) or {}
                    for diagnostic in observed
                ])
                for name in backend_names
            },
            "fused": coverage_rate([diagnostic.get("fused") or {} for diagnostic in observed]),
            "selected": coverage_rate([diagnostic.get("selected") or {} for diagnostic in observed]),
        }

    grouped: Dict[str, List[Dict[str, Any]]] = {}
    for item in items:
        cohort = str(item.get("evaluation_cohort", "")).strip()
        if cohort:
            grouped.setdefault(cohort, []).append(item)

    summaries: Dict[str, Dict[str, Any]] = {}
    for cohort, group in sorted(grouped.items()):
        positive = [item for item in group if bool(item.get("expect_hit"))]
        required = [item for item in group if item.get("required_doc_ids")]
        citation_count, citation_rate = rate(
            group, "citation_check_eligible", "answer_source_citation_ok"
        )
        key_fact_count, key_fact_rate = rate(
            group, "key_fact_check_eligible", "key_fact_assertion_pass"
        )
        safety_count, safety_rate = rate(
            group, "safety_refusal_eligible", "safety_refusal_pass"
        )
        grounding_count, grounding_rate = rate(
            group, "grounding_checked", "grounding_passed"
        )
        summaries[cohort] = {
            "total_cases": len(group),
            "positive_cases": len(positive),
            "recall_at_5_eligible_cases": len(positive),
            "recall_at_5": (
                sum(bool(item.get("recall_at_5")) for item in positive) / len(positive)
                if positive
                else 0.0
            ),
            "all_required_docs_eligible_cases": len(required),
            "all_required_docs_hit_rate": (
                sum(bool(item.get("required_docs_hit")) for item in required) / len(required)
                if required
                else 0.0
            ),
            "citation_eligible_cases": citation_count,
            "citation_complete_rate": citation_rate,
            "key_fact_eligible_cases": key_fact_count,
            "key_fact_pass_rate": key_fact_rate,
            "safety_refusal_eligible_cases": safety_count,
            "safety_refusal_rate": safety_rate,
            "grounding_eligible_cases": grounding_count,
            "grounding_pass_rate": grounding_rate,
            "latency_ms": latency(
                [float(item["query_latency_ms"]) for item in group if "query_latency_ms" in item]
            ),
            "total_tokens": sum(
                int(item.get("prompt_tokens", 0) or 0)
                + int(item.get("completion_tokens", 0) or 0)
                for item in group
            ),
            "avg_tokens_per_case": (
                sum(
                    int(item.get("prompt_tokens", 0) or 0)
                    + int(item.get("completion_tokens", 0) or 0)
                    for item in group
                )
                / len(group)
                if group
                else 0.0
            ),
        }
        diagnostics = stage_diagnostics(group)
        if diagnostics:
            summaries[cohort]["retrieval_stage_diagnostics"] = diagnostics
    return summaries


def write_report(report_dir: Path, result: Dict[str, Any]) -> Tuple[Path, Path]:
    report_dir.mkdir(parents=True, exist_ok=True)
    ts = time.strftime("%Y%m%d-%H%M%S", time.localtime())
    json_path = report_dir / f"eval-{ts}.json"
    md_path = report_dir / f"eval-{ts}.md"

    cohorts = summarize_cohorts(result.get("cases", []))
    if cohorts:
        result["cohorts"] = cohorts
    cases = result.get("cases", [])
    if cases and all("query_successful" in item for item in cases):
        successful_queries = sum(bool(item.get("query_successful")) for item in cases)
        grounding_unavailable = sum(bool(item.get("grounding_unavailable")) for item in cases)
        result["summary"]["successful_query_cases"] = successful_queries
        result["summary"]["grounding_unavailable_cases"] = grounding_unavailable
        result["summary"]["run_valid"] = (
            successful_queries == len(cases) and grounding_unavailable == 0
        )
    json_path.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")

    mode = str(result.get("model_mode", "mock"))
    models = result.get("models", {})
    configuration = result.get("configuration", {})

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

    dataset = result.get("dataset") or {}
    provenance = dataset.get("provenance") or {}
    if dataset:
        comparable = provenance.get("benchmark_comparable")
        comparable_label = "YES" if comparable is True else "NO (sampled)" if comparable is False else "unspecified"
        lines.extend(
            [
                f"- Dataset: {dataset.get('name', '')}",
                f"- Dataset type: {dataset.get('dataset_type', '')}",
                f"- Evaluation scope: {dataset.get('evaluation_scope', '')}",
                f"- Selected cohort: {dataset.get('selected_cohort', '') or 'all'}",
                f"- Retrieval only: {str(bool(dataset.get('retrieval_only', False))).lower()}",
                f"- Dataset version: {provenance.get('dataset_version', '')}",
                f"- Dataset split: {provenance.get('split', '')}",
                f"- License: {provenance.get('license', '')}",
                f"- Source URL: {provenance.get('source_url', '')}",
                f"- Benchmark comparable: {comparable_label}",
                f"- Dataset documents: {dataset.get('document_count', '')}",
                f"- Dataset cases: {dataset.get('case_count', '')}",
            ]
        )
        if dataset.get("sha256"):
            lines.append(f"- Dataset SHA-256: {dataset['sha256']}")
        if dataset.get("dataset_type") == "public_benchmark":
            lines.extend(
                [
                    "",
                    "**This public retrieval score is not enterprise-domain acceptance evidence.**",
                    "",
                ]
            )

    lines.extend(
        [
            f"- Timestamp: {result['timestamp']}",
            f"- Tenant ID: {result.get('tenant_id', '')}",
            f"- Model Mode: {mode}",
            f"- Embed Model: {models.get('embed_model', '')} (dim={models.get('embed_dimension', '')})",
            f"- LLM Model: {models.get('llm_model', '')}",
            f"- Reranker Model: {models.get('reranker_model', '')}",
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
            f"- Negative pass rate: {result['summary'].get('negative_pass_rate', 1.0):.2%}",
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

    if configuration:
        lines.extend(
            [
                f"- Rerank enabled: {str(bool(configuration.get('retrieval_enable_rerank'))).lower()}",
                f"- Rerank policy: {configuration.get('retrieval_rerank_policy', '')}",
                f"- Retrieval candidate K: {configuration.get('retrieval_candidate_k', '')}",
                f"- Retrieval final Top-K: {configuration.get('retrieval_final_top_k', '')}",
                f"- Eval query Top-K: {configuration.get('query_top_k', '')}",
                f"- Grounding check: {str(bool(configuration.get('retrieval_grounding_check'))).lower()}",
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

    if cohorts:
        def display_rate(summary: Dict[str, Any], count_key: str, rate_key: str) -> str:
            if int(summary.get(count_key, 0) or 0) == 0:
                return "N/A"
            return f"{float(summary.get(rate_key, 0.0)):.2%}"

        lines.extend(
            [
                "",
                "## Cohort Metrics",
                "",
                "| Cohort | Cases | Recall@5 | All required docs | Citation complete | Key facts | Safety refusal | Grounding | p50 ms | p95 ms | Tokens |",
                "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|",
            ]
        )
        for cohort, summary in cohorts.items():
            latency = summary.get("latency_ms", {})
            lines.append(
                f"| {cohort} | {summary['total_cases']} | "
                f"{display_rate(summary, 'recall_at_5_eligible_cases', 'recall_at_5')} | "
                f"{display_rate(summary, 'all_required_docs_eligible_cases', 'all_required_docs_hit_rate')} | "
                f"{display_rate(summary, 'citation_eligible_cases', 'citation_complete_rate')} | "
                f"{display_rate(summary, 'key_fact_eligible_cases', 'key_fact_pass_rate')} | "
                f"{display_rate(summary, 'safety_refusal_eligible_cases', 'safety_refusal_rate')} | "
                f"{display_rate(summary, 'grounding_eligible_cases', 'grounding_pass_rate')} | "
                f"{latency.get('p50', '-')} | {latency.get('p95', '-')} | {summary['total_tokens']} |"
            )
        diagnostic_cohorts = {
            cohort: summary.get("retrieval_stage_diagnostics")
            for cohort, summary in cohorts.items()
            if summary.get("retrieval_stage_diagnostics")
        }
        if diagnostic_cohorts:
            lines.extend(
                [
                    "",
                    "### Required-Document Stage Diagnostics (aggregate only)",
                    "",
                    "| Cohort | Qdrant all-required | Elasticsearch all-required | Fused all-required | Selected all-required | Eligible cases |",
                    "|---|---:|---:|---:|---:|---:|",
                ]
            )
            for cohort, diagnostic in diagnostic_cohorts.items():
                backend = diagnostic.get("backend") or {}
                qdrant_rate = float((backend.get("qdrant") or {}).get("all_required_hit_rate", 0.0))
                elastic_rate = float((backend.get("elasticsearch") or {}).get("all_required_hit_rate", 0.0))
                lines.append(
                    f"| {cohort} | {qdrant_rate:.2%} | {elastic_rate:.2%} | "
                    f"{float((diagnostic.get('fused') or {}).get('all_required_hit_rate', 0.0)):.2%} | "
                    f"{float((diagnostic.get('selected') or {}).get('all_required_hit_rate', 0.0)):.2%} | "
                    f"{int(diagnostic.get('eligible_cases', 0) or 0)} |"
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
    write_quality_latest(report_dir, result, json_path.name)
    return json_path, md_path


def _load_quality_latest_template(report_dir: Path) -> Dict[str, Any]:
    for candidate in (report_dir / QUALITY_LATEST_NAME, BAKED_QUALITY_LATEST):
        if not candidate.is_file():
            continue
        try:
            payload = json.loads(candidate.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            continue
        if isinstance(payload, dict):
            return payload
    return {}


def write_quality_latest(report_dir: Path, result: Dict[str, Any], source_name: str) -> Path | None:
    if str(result.get("model_mode", "")).lower() != "real":
        return None
    summary = result.get("summary") or {}
    if summary.get("run_valid") is False:
        return None

    existing = _load_quality_latest_template(report_dir)
    models = result.get("models") or {}
    dataset = result.get("dataset") or {}

    def pct(key: str) -> int:
        return int(round(float(summary.get(key, 0) or 0) * 100))

    dataset_label = str(existing.get("dataset") or "")
    name = str(dataset.get("name") or "").strip()
    case_count = dataset.get("case_count")
    if name:
        dataset_label = f"{name} ({case_count} cases)" if case_count not in (None, "") else name

    payload = {
        "source_report": source_name,
        "timestamp": result.get("timestamp") or existing.get("timestamp"),
        "model_mode": "real",
        "embed_model": models.get("embed_model") or existing.get("embed_model"),
        "embed_dimension": models.get("embed_dimension") or existing.get("embed_dimension"),
        "llm_model": models.get("llm_model") or existing.get("llm_model"),
        "dataset": dataset_label,
        "recall": [
            {"k": 1, "value": pct("recall_at_1"), "note": QUALITY_RECALL_NOTES[1]},
            {"k": 3, "value": pct("recall_at_3"), "note": QUALITY_RECALL_NOTES[3]},
            {"k": 5, "value": pct("recall_at_5"), "note": QUALITY_RECALL_NOTES[5]},
        ],
        "noise_floor": existing.get("noise_floor", 4.26),
        "experiments": existing.get("experiments") or [],
    }
    latest_path = report_dir / QUALITY_LATEST_NAME
    latest_path.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return latest_path


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
    parser.add_argument(
        "--processing-timeout",
        type=int,
        default=1800,
        help="maximum seconds to wait for the complete uploaded ETL batch; "
        "independent from --max-wait, which applies to each retrieval case",
    )
    parser.add_argument("--negative-max-wait", type=int, default=30)
    parser.add_argument(
        "--query-timeout",
        type=float,
        default=30.0,
        help="HTTP timeout in seconds for each /v1/query request",
    )
    parser.add_argument("--top-k", type=int, default=5)
    parser.add_argument(
        "--cohort",
        default="",
        help="run only one evaluation cohort; all documents remain uploaded",
    )
    parser.add_argument("--poll-interval", type=float, default=2.0)
    parser.add_argument("--required-consecutive-hits", type=int, default=2)
    parser.add_argument(
        "--api-base",
        default="",
        help="Query API base URL. When set, reuse that stack and do not start Compose.",
    )
    parser.add_argument(
        "--admin-username",
        default="",
        help="existing-stack admin username; defaults to BOOTSTRAP_ADMIN_USERNAME or eval-admin",
    )
    parser.add_argument(
        "--admin-password",
        default="",
        help="existing-stack admin password; defaults to BOOTSTRAP_ADMIN_PASSWORD or eval-admin password",
    )
    parser.add_argument(
        "--compose-project",
        default="",
        help="optional isolated Docker Compose project name",
    )
    parser.add_argument("--min-hit-rate", type=float, default=0.9)
    parser.add_argument("--min-answer-pass-rate", type=float, default=1.0)
    parser.add_argument("--min-pass-rate", type=float, default=1.0)
    parser.add_argument("--min-negative-pass-rate", type=float, default=1.0)
    parser.add_argument("--disable-answer-assertions", action="store_true")
    parser.add_argument(
        "--retrieval-only",
        action="store_true",
        help="request retrieval-only diagnostics and skip answer generation/assertions",
    )
    parser.add_argument("--keep-services", action="store_true")
    parser.add_argument(
        "--reuse-upload-map",
        default="",
        help="reuse a prior run's published document IDs and vectors; the dataset digest "
        "and tenant must match",
    )
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
    # web service mounts docs/evals/reports at /eval-reports, and Docker creates
    # a missing mount source as root — which would make this directory unwritable
    # by the non-root CI user later.
    report_dir.mkdir(parents=True, exist_ok=True)
    if not golden_set_path.exists():
        raise EvalRunnerError(f"golden set not found: {golden_set_path}")

    dataset = load_eval_dataset(golden_set_path)
    cases = filter_eval_cases(dataset.cases, args.cohort)
    documents = dataset.documents
    reused_tenant_id = ""
    reused_doc_ids: Dict[str, str] = {}
    if args.reuse_upload_map:
        reused_tenant_id, reused_doc_ids = load_upload_map(
            Path(args.reuse_upload_map),
            dataset_path=golden_set_path,
            expected_document_ids=[document.document_id for document in documents],
        )
    judge_client = None
    if args.judge:
        if dataset.evaluation_scope == "retrieval":
            raise EvalRunnerError("--judge cannot score a retrieval-only dataset without reference answers")
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
    if reused_tenant_id and tenant_id and tenant_id != reused_tenant_id:
        raise EvalRunnerError("--tenant-id does not match the reused upload map")
    if reused_tenant_id:
        tenant_id = reused_tenant_id
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
        api_base = args.api_base.strip().rstrip("/")
        env = compose_env(
            profile,
            tenant_id,
            args.jwt_secret,
            compose_project,
        )
        if should_start_compose(api_base):
            print(f"[eval] compose project: {compose_project}")
            print("[eval] starting docker compose stack")
            run_cmd(["docker", "compose", "up", "-d", "--build"], env=env, timeout_sec=900)
            started_services = True
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
            admin_username = "eval-admin"
            admin_password = "eval-admin-password-2026"
        else:
            print(f"[eval] reusing existing Query API {api_base} (no compose)")
            print("[eval] waiting query-api healthz")
            wait_health(f"{api_base}/healthz", timeout_sec=180)
            admin_username = (
                args.admin_username.strip()
                or str(os.environ.get("BOOTSTRAP_ADMIN_USERNAME", "")).strip()
                or "admin"
            )
            admin_password = (
                args.admin_password
                or str(os.environ.get("BOOTSTRAP_ADMIN_PASSWORD", ""))
                or "admin"
            )
            if not str(admin_password).strip():
                raise EvalRunnerError("existing API eval requires --admin-password or BOOTSTRAP_ADMIN_PASSWORD")

        print("[eval] provisioning catalog-member test users")
        admin_token = login_eval_user(api_base, admin_username, admin_password)
        # User identities live in the isolated project's database, so they must
        # be provisioned even when document ids are reused from another run.
        # 409 is idempotent when --keep-services is used across retries.
        create_eval_user(api_base, admin_token, "eval-user", "eval-user-password-2026", "user")
        create_eval_user(api_base, admin_token, "eval-readonly", "eval-readonly-password-2026", "readonly")
        upload_token = login_eval_user(api_base, "eval-user", "eval-user-password-2026")
        query_tokens: Dict[str, str] = {
            "user": upload_token,
            "readonly": login_eval_user(api_base, "eval-readonly", "eval-readonly-password-2026"),
            "admin": admin_token,
        }
        if args.required_consecutive_hits <= 0:
            raise EvalRunnerError("--required-consecutive-hits must be >= 1")
        if args.poll_interval <= 0:
            raise EvalRunnerError("--poll-interval must be > 0")
        if args.negative_max_wait <= 0:
            raise EvalRunnerError("--negative-max-wait must be > 0")
        if args.query_timeout <= 0:
            raise EvalRunnerError("--query-timeout must be > 0")
        if args.processing_timeout <= 0:
            raise EvalRunnerError("--processing-timeout must be > 0")
        if args.min_answer_pass_rate < 0 or args.min_answer_pass_rate > 1:
            raise EvalRunnerError("--min-answer-pass-rate must be in [0,1]")
        if args.min_negative_pass_rate < 0 or args.min_negative_pass_rate > 1:
            raise EvalRunnerError("--min-negative-pass-rate must be in [0,1]")

        eval_items: List[Dict[str, Any]] = []
        uploaded_doc_ids: Dict[str, str] = dict(reused_doc_ids)
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

        if reused_doc_ids:
            print(f"[eval] reusing {len(uploaded_doc_ids)} published documents")
        else:
            for idx, document in enumerate(documents, start=1):
                print(f"[eval] {idx}/{len(documents)} upload {document.document_id}")
                fixture_role = upload_role_for_permission(document.permission)
                doc_id = upload_document(api_base, query_tokens[fixture_role], document)
                uploaded_doc_ids[document.document_id] = doc_id

            # Wait for the async full-text sink to finish indexing before querying.
            # Exact-keyword queries route ES with 0.75 weight; if ES has not caught
            # up, a document that is present in Qdrant scores 0 on the BM25 side and
            # gets pushed out of the top-K by the RRF fusion — the eval then reports
            # a false retrieval timeout. The documents are always uploaded and stored;
            # the race is purely the eval's, not the pipeline's.
            wait_for_es_sync(
                env,
                len(uploaded_doc_ids),
                timeout_sec=120,
                poll_sec=2,
                es_url="" if started_services else "http://127.0.0.1:9200",
            )
            wait_for_document_tasks(
                api_base,
                admin_token,
                list(uploaded_doc_ids.values()),
                timeout_sec=args.processing_timeout,
                poll_sec=2,
            )
            print("[eval] verifying completed documents were auto-published")
            for doc_id in uploaded_doc_ids.values():
                verify_document_published(api_base, admin_token, doc_id)
            upload_map_path = report_dir / "upload-map.json"
            write_upload_map(
                upload_map_path,
                tenant_id=tenant_id,
                dataset_path=golden_set_path,
                uploaded_doc_ids=uploaded_doc_ids,
            )
            print(f"[eval] upload map: {upload_map_path}")

        for idx, case in enumerate(cases, start=1):
            expected_doc_id = uploaded_doc_ids[case.document_id or case.case_id]
            acceptable_doc_ids = resolve_acceptable_doc_ids(case, uploaded_doc_ids, expected_doc_id)
            required_doc_ids = resolve_required_doc_ids(case, uploaded_doc_ids)
            forbidden_doc_ids = resolve_forbidden_doc_ids(case, uploaded_doc_ids, expected_doc_id)
            query_top_k = max(case.query_top_k if case.query_top_k > 0 else args.top_k, 5)
            max_strict_rank = max(case.max_strict_rank, 0)
            case_max_wait = args.max_wait if case.expect_hit else args.negative_max_wait
            query_permission = (case.query_permission or "user").strip().lower() or "user"
            token = query_tokens.get(query_permission, "")
            if not token:
                raise EvalRunnerError(f"unsupported eval permission role: {query_permission}")
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
                args.query_timeout,
                required_doc_ids=required_doc_ids,
                retrieval_only=args.retrieval_only,
            )
            answer_assertions_enabled = (
                not args.disable_answer_assertions
                and dataset.evaluation_scope != "retrieval"
                and not args.retrieval_only
            )
            answer_details = evaluate_answer_assertions(
                case,
                payload,
                acceptable_doc_ids,
                enable_answer_assertions=answer_assertions_enabled,
                required_doc_ids=required_doc_ids,
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
                    "evaluation_cohort": case.metadata.get("evaluation_cohort", ""),
                    "expect_hit": case.expect_hit,
                    "expected_doc_id": expected_doc_id,
                    "acceptable_doc_ids": acceptable_doc_ids,
                    "required_doc_ids": required_doc_ids,
                    "forbidden_doc_ids": forbidden_doc_ids,
                    "prompt_tokens": prompt_tokens,
                    "completion_tokens": completion_tokens,
                    "query_attempts": details["query_attempts"],
                    "query_latency_ms": details["query_latency_ms"],
                    "query_successful": details["query_successful"],
                    "last_query_status": details["last_query_status"],
                    "query_top_k": query_top_k,
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
                    "required_docs_hit": details["required_docs_hit"],
                    "required_doc_ranks": details["required_doc_ranks"],
                    "max_consecutive_hits": details["max_consecutive_hits"],
                    "retrieval_assertion_pass": retrieval_pass,
                    "retrieval_assertion_reason": details["assertion_reason"],
                    "answer_assertion_pass": answer_pass,
                    "answer_assertion_reason": answer_details["answer_assertion_reason"],
                    "answer_len": answer_details["answer_len"],
                    "citation_check_eligible": bool(
                        answer_assertions_enabled
                        and case.expect_hit
                        and case.require_source_citation
                    ),
                    "answer_source_citation_ok": answer_details["answer_source_citation_ok"],
                    "answer_cited_doc_ids": answer_details["answer_cited_doc_ids"],
                    "key_fact_check_eligible": answer_details["key_fact_check_eligible"],
                    "key_fact_assertion_pass": answer_details["key_fact_assertion_pass"],
                    "safety_refusal_eligible": answer_details["safety_refusal_eligible"],
                    "safety_refusal_pass": answer_details["safety_refusal_pass"],
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
                    "grounding_unavailable": bool(
                        retrieval_info.get("grounding_unavailable", False)
                    ),
                    "retrieval_stage_diagnostics": details["retrieval_stage_diagnostics"],
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
        negative_pass_rate = negative_passed / negative_total if negative_total else 1.0
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
        resolved_configuration = resolved_eval_configuration(
            env,
            query_top_k=args.top_k,
            query_timeout_seconds=args.query_timeout,
            required_consecutive_hits=args.required_consecutive_hits,
            poll_interval_seconds=args.poll_interval,
            negative_max_wait_seconds=args.negative_max_wait,
        )

        result = {
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "tenant_id": tenant_id,
            "mock_port": resolved_mock_port if not profile.is_real else "",
            "compose_project": compose_project,
            "api_base": api_base,
            "model_mode": profile.mode,
            "dataset": {
                "version": dataset.version,
                "name": dataset.name,
                "dataset_type": dataset.dataset_type,
                "evaluation_scope": dataset.evaluation_scope,
                "selected_cohort": args.cohort.strip(),
                "retrieval_only": bool(args.retrieval_only),
                "document_count": len(documents),
                "case_count": len(cases),
                "sha256": dataset_digest(golden_set_path),
                "provenance": dataset.provenance,
            },
            "models": {
                "embed_model": profile.embed_model,
                "embed_endpoint": profile.embed_endpoint,
                "embed_dimension": profile.embed_dim,
                "llm_model": profile.llm_model,
                "llm_endpoint": profile.llm_endpoint,
                "reranker_model": resolved_configuration["reranker_model"],
                "reranker_backend": resolved_configuration["reranker_backend"],
                "store_collection": profile.store_collection,
            },
            "configuration": resolved_configuration,
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
                "negative_pass_rate": negative_pass_rate,
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
                "threshold_negative_pass_rate": args.min_negative_pass_rate,
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
        if negative_pass_rate < args.min_negative_pass_rate:
            print(
                f"[eval] FAILED: negative_pass_rate={negative_pass_rate:.2%} "
                f"< min_negative_pass_rate={args.min_negative_pass_rate:.2%}",
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
            f"negative_pass_rate={negative_pass_rate:.2%}, "
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
