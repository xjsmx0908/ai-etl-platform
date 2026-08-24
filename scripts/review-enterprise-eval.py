#!/usr/bin/env python3
"""Locally review enterprise silver labels and build a technical gold draft.

This tool never treats model inference as business approval. It verifies source
integrity and evidence mechanically, uses a local Ollama model as an independent
semantic reviewer, and emits a separate list for authority-only decisions.
"""

from __future__ import annotations

import argparse
import copy
import csv
from dataclasses import dataclass, field
import hashlib
import json
from pathlib import Path
import re
import sys
from typing import Any
from urllib import request


ROOT = Path(__file__).resolve().parent.parent
DEFAULT_INPUT = ROOT / "docs" / "evals" / "private" / "p1.4-enterprise"
PROMPT_VERSION = 1
BUSINESS_DOMAINS = (
    "administration",
    "finance",
    "human_resources",
    "information_security",
    "legal_compliance",
    "operations",
    "procurement",
    "research_development",
    "sales_marketing",
    "unknown",
)
DOCUMENT_KINDS = (
    "contract",
    "form",
    "notice",
    "other",
    "policy",
    "procedure",
    "record",
    "reference",
    "report",
)
EFFECTIVENESS_SIGNALS = (
    "explicit_current",
    "explicit_historical",
    "not_applicable",
    "unclear",
)
AUTHORITY_REQUIRED_KINDS = frozenset({"contract", "form", "notice", "policy", "procedure", "other"})
MAX_QUERY_CONTENT_OVERLAP = 0.55
STOPWORDS = frozenset(
    "的 了 是 和 与 或 吗 呢 吧 啊 在 有 能 会 要 把 被 对 从 到 于 而 也 都 这 那 什么 怎么 可以 "
    "这个 那个 一个 一下 一些 哪里 多少 系统 文件 文档 内容 上传 检索 查询 支持 使用 知道 看到 "
    "搜索 知识库 请问 我们 你们 他 她 它 我 你 得 很 太 比较 超过 是否 因为 所以 然后 如果 应该 "
    "需要 不会 能不能 是不是 有没有".split()
)
PII_PATTERNS = (
    re.compile(r"(?<!\d)1[3-9]\d{9}(?!\d)"),
    re.compile(r"(?<!\d)\d{17}[\dXx](?![\dA-Za-z])"),
    re.compile(r"\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b", re.IGNORECASE),
    re.compile(r"(?:账号|密码|password|token)\s*[:：=]\s*\S{4,}", re.IGNORECASE),
)


@dataclass
class DocumentDecision:
    document_id: str
    final_permission: str
    permission_basis: str
    business_domain: str
    document_kind: str
    effectiveness_signal: str
    effective_status: str
    review_decision: str
    gold_eligible: bool
    reason_codes: list[str] = field(default_factory=list)


@dataclass
class CaseDecision:
    case_id: str
    technical_decision: str
    gold_eligible: bool
    reason_codes: list[str] = field(default_factory=list)
    retrieval_style: str = "unknown"


def parse_flags(value: Any) -> list[str]:
    return [item.strip() for item in str(value or "").split(",") if item.strip()]


def normalized_text(value: str) -> str:
    return re.sub(r"\s+", "", value or "")


def contains_sensitive_value(value: str) -> bool:
    return any(pattern.search(value or "") for pattern in PII_PATTERNS)


def content_tokens(value: str) -> set[str]:
    text = (value or "").lower()
    tokens = {
        char for char in text if "一" <= char <= "鿿" and char not in STOPWORDS
    }
    tokens.update(re.findall(r"[a-z0-9]{4,}", text))
    return tokens


def query_content_overlap(query: str, content: str) -> float:
    query_tokens = content_tokens(query)
    if not query_tokens:
        return 0.0
    return len(query_tokens & content_tokens(content)) / len(query_tokens)


def evidence_is_verbatim(content: str, evidence: str) -> bool:
    return bool(evidence.strip()) and normalized_text(evidence) in normalized_text(content)


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def resolve_source_path(value: str) -> Path:
    candidate = Path(value)
    if not candidate.is_absolute():
        candidate = ROOT / candidate
    resolved = candidate.resolve()
    try:
        resolved.relative_to(ROOT.resolve())
    except ValueError as exc:
        raise ValueError("source_path escapes repository root") from exc
    return resolved


def verify_source_integrity(document: dict[str, Any], review_row: dict[str, str]) -> tuple[bool, str]:
    try:
        source = resolve_source_path(str(document.get("source_path", "")))
        if not source.is_file():
            return False, "source_missing"
        actual = sha256_file(source)
    except (OSError, ValueError):
        return False, "source_unreadable"
    metadata = document.get("metadata") if isinstance(document.get("metadata"), dict) else {}
    expected = str(metadata.get("source_sha256") or review_row.get("sha256") or "").strip().lower()
    if not expected or actual.lower() != expected:
        return False, "source_hash_mismatch"
    row_digest = str(review_row.get("sha256") or "").strip().lower()
    if row_digest and row_digest != actual.lower():
        return False, "review_hash_mismatch"
    return True, "source_hash_verified"


def review_document(
    document: dict[str, Any],
    review_row: dict[str, str],
    semantic: dict[str, Any],
    *,
    conflict_ids: set[str],
    integrity_ok: bool,
) -> DocumentDecision:
    document_id = str(document.get("id", ""))
    sensitivity_flags = parse_flags(review_row.get("sensitivity_flags"))
    pii_flags = parse_flags(review_row.get("pii_flags"))
    quality_flags = parse_flags(review_row.get("quality_flags"))
    source_permission = str(document.get("permission") or review_row.get("permission") or "internal").lower()

    final_permission = "confidential" if source_permission == "confidential" else "internal"
    permission_basis = "source_label"
    if pii_flags:
        final_permission = "confidential"
        permission_basis = "pii_signal"
    elif sensitivity_flags:
        final_permission = "confidential"
        permission_basis = "sensitivity_signal"

    business_domain = str(semantic.get("business_domain", "unknown"))
    if business_domain not in BUSINESS_DOMAINS:
        business_domain = "unknown"
    document_kind = str(semantic.get("document_kind", "other"))
    if document_kind not in DOCUMENT_KINDS:
        document_kind = "other"
    effectiveness_signal = str(semantic.get("effectiveness_signal", "unclear"))
    if effectiveness_signal not in EFFECTIVENESS_SIGNALS:
        effectiveness_signal = "unclear"

    if not integrity_ok:
        return DocumentDecision(
            document_id,
            final_permission,
            permission_basis,
            business_domain,
            document_kind,
            effectiveness_signal,
            "unverified",
            "excluded_integrity_failure",
            False,
            ["source_integrity_failed"],
        )
    if quality_flags:
        return DocumentDecision(
            document_id,
            final_permission,
            permission_basis,
            business_domain,
            document_kind,
            effectiveness_signal,
            "excluded",
            "excluded_low_quality",
            False,
            [*quality_flags],
        )
    if document_id in conflict_ids:
        return DocumentDecision(
            document_id,
            final_permission,
            permission_basis,
            business_domain,
            document_kind,
            effectiveness_signal,
            "pending_version_confirmation",
            "pending_version_confirmation",
            False,
            ["unresolved_version_conflict"],
        )
    if document_kind in AUTHORITY_REQUIRED_KINDS and effectiveness_signal != "not_applicable":
        return DocumentDecision(
            document_id,
            final_permission,
            permission_basis,
            business_domain,
            document_kind,
            effectiveness_signal,
            "pending_business_confirmation",
            "pending_business_confirmation",
            False,
            ["effective_status_requires_authority"],
        )
    effective_status = "historical" if effectiveness_signal == "explicit_historical" else "not_applicable"
    return DocumentDecision(
        document_id,
        final_permission,
        permission_basis,
        business_domain,
        document_kind,
        effectiveness_signal,
        effective_status,
        "auto_approved_technical",
        True,
        ["source_and_classification_verified"],
    )


def review_case(
    case: dict[str, Any],
    review_row: dict[str, str],
    document: dict[str, Any],
    semantic: dict[str, Any] | None,
) -> CaseDecision:
    case_id = str(case.get("id", ""))
    metadata = case.get("metadata") if isinstance(case.get("metadata"), dict) else {}
    category = str(metadata.get("category", ""))
    expect_hit = bool(case.get("expect_hit", True))
    permission = str(document.get("permission", "internal")).lower()

    if category == "permission_negative":
        if expect_hit or permission != "confidential":
            return CaseDecision(case_id, "rejected_permission_mismatch", False, ["permission_case_mismatch"])
        return CaseDecision(
            case_id, "approved", True, ["conservative_permission_verified"], "safety_negative"
        )
    if category == "no_answer":
        if expect_hit:
            return CaseDecision(case_id, "rejected_no_answer_mismatch", False, ["no_answer_expect_hit"])
        return CaseDecision(case_id, "approved", True, ["synthetic_absence_verified"], "safety_negative")
    if not expect_hit:
        return CaseDecision(case_id, "pending_negative_case_review", False, ["unsupported_negative_category"])

    query = str(case.get("query", "")).strip()
    answer = str(case.get("reference_answer", "")).strip()
    evidence = str(review_row.get("evidence", "")).strip()
    content = str(document.get("content", ""))
    if not query or not answer:
        return CaseDecision(case_id, "rejected_missing_text", False, ["query_or_answer_missing"])
    if contains_sensitive_value(query) or contains_sensitive_value(answer):
        return CaseDecision(case_id, "rejected_sensitive_case", False, ["case_contains_sensitive_value"])
    lexical_overlap = query_content_overlap(query, content)
    if not evidence_is_verbatim(content, evidence):
        return CaseDecision(case_id, "rejected_evidence_not_found", False, ["evidence_not_verbatim"])
    phrases = case.get("answer_must_include")
    if not isinstance(phrases, list) or not phrases:
        return CaseDecision(case_id, "rejected_missing_key_fact", False, ["answer_key_missing"])
    normalized_evidence = normalized_text(evidence)
    normalized_answer = normalized_text(answer)
    if not all(
        normalized_text(str(phrase)) in normalized_evidence
        and normalized_text(str(phrase)) in normalized_answer
        for phrase in phrases
    ):
        return CaseDecision(case_id, "rejected_unsupported_key_fact", False, ["answer_key_not_supported"])
    if semantic is None:
        return CaseDecision(case_id, "pending_semantic_review", False, ["local_review_missing"])
    checks = ("evidence_supported", "answer_complete", "question_natural")
    failed = [name for name in checks if semantic.get(name) is not True]
    if failed:
        return CaseDecision(case_id, "rejected_semantic_review", False, failed)
    retrieval_style = "lexical" if lexical_overlap > MAX_QUERY_CONTENT_OVERLAP else "semantic"
    reason = (
        "approved_lexical_cohort"
        if retrieval_style == "lexical"
        else "approved_semantic_cohort"
    )
    return CaseDecision(case_id, "approved", True, [reason], retrieval_style)


def review_schema(case_ids: list[str]) -> dict[str, Any]:
    return {
        "type": "object",
        "properties": {
            "business_domain": {"type": "string", "enum": list(BUSINESS_DOMAINS)},
            "document_kind": {"type": "string", "enum": list(DOCUMENT_KINDS)},
            "effectiveness_signal": {"type": "string", "enum": list(EFFECTIVENESS_SIGNALS)},
            "case_reviews": {
                "type": "array",
                "minItems": len(case_ids),
                "maxItems": len(case_ids),
                "items": {
                    "type": "object",
                    "properties": {
                        "case_id": {"type": "string", "enum": case_ids or ["none"]},
                        "evidence_supported": {"type": "boolean"},
                        "answer_complete": {"type": "boolean"},
                        "question_natural": {"type": "boolean"},
                    },
                    "required": [
                        "case_id",
                        "evidence_supported",
                        "answer_complete",
                        "question_natural",
                    ],
                },
            },
        },
        "required": ["business_domain", "document_kind", "effectiveness_signal", "case_reviews"],
    }


def representative_content(content: str, max_chars: int = 3_000) -> str:
    if len(content) <= max_chars:
        return content
    segment = max_chars // 3
    middle = max((len(content) - segment) // 2, segment)
    return "\n[省略]\n".join((content[:segment], content[middle : middle + segment], content[-segment:]))


def ollama_review(endpoint: str, model: str, prompt: str, schema: dict[str, Any], timeout: int) -> dict[str, Any]:
    body = json.dumps(
        {
            "model": model,
            "stream": False,
            "think": False,
            "format": schema,
            "messages": [{"role": "user", "content": prompt}],
            "options": {"temperature": 0, "num_ctx": 4096, "num_predict": 300, "num_thread": 4},
        },
        ensure_ascii=False,
    ).encode("utf-8")
    req = request.Request(
        endpoint.rstrip("/") + "/api/chat",
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with request.urlopen(req, timeout=timeout) as response:
        payload = json.loads(response.read().decode("utf-8"))
    parsed = json.loads(str(payload.get("message", {}).get("content", "")))
    if not isinstance(parsed, dict):
        raise ValueError("local reviewer returned a non-object")
    return parsed


def semantic_prompt(document: dict[str, Any], case_inputs: list[dict[str, str]]) -> str:
    return """你是本机运行的企业 RAG 数据审核员。只做证据审查，不推测现实制度是否仍有效，也不输出文档原文。

任务：
1. 从枚举中判断业务领域、文档类型和文内是否存在效力信号。
2. 对每个案例判断：证据是否直接支持答案、答案是否完整回答问题、问题是否像自然业务提问。
3. 只能返回给定 JSON Schema；不得补充解释。`explicit_current` 仅表示文内自称当前有效，不等于获得企业授权。

文件名：{filename}
文档节选：
{content}

待审案例：
{cases}
""".format(
        filename=document.get("filename", ""),
        content=representative_content(str(document.get("content", ""))),
        cases=json.dumps(case_inputs, ensure_ascii=False),
    )


def cache_key(document: dict[str, Any], case_inputs: list[dict[str, str]], model: str) -> str:
    metadata = document.get("metadata") if isinstance(document.get("metadata"), dict) else {}
    value = {
        "source_sha256": metadata.get("source_sha256", ""),
        "cases": case_inputs,
        "model": model,
        "prompt_version": PROMPT_VERSION,
    }
    return hashlib.sha256(json.dumps(value, ensure_ascii=False, sort_keys=True).encode("utf-8")).hexdigest()


def build_gold_draft(
    source_dataset: dict[str, Any],
    documents: list[dict[str, Any]],
    cases: list[dict[str, Any]],
    document_decisions: dict[str, DocumentDecision],
    case_decisions: dict[str, CaseDecision],
    model: str,
) -> dict[str, Any]:
    eligible_ids = {doc_id for doc_id, decision in document_decisions.items() if decision.gold_eligible}
    output_documents: list[dict[str, Any]] = []
    for document in documents:
        document_id = str(document.get("id", ""))
        if document_id not in eligible_ids:
            continue
        decision = document_decisions[document_id]
        value = copy.deepcopy(document)
        value["permission"] = decision.final_permission
        metadata = value.setdefault("metadata", {})
        metadata.update(
            {
                "eval_dataset": "p1.5-enterprise-gold-draft",
                "review_status": "auto_approved_technical",
                "business_domain_suggestion": decision.business_domain,
                "document_kind": decision.document_kind,
                "effective_status": decision.effective_status,
            }
        )
        output_documents.append(value)

    output_cases: list[dict[str, Any]] = []
    for case in cases:
        case_id = str(case.get("id", ""))
        document_id = str(case.get("document_id", ""))
        decision = case_decisions.get(case_id)
        if document_id not in eligible_ids or decision is None or not decision.gold_eligible:
            continue
        value = copy.deepcopy(case)
        doc_decision = document_decisions[document_id]
        if bool(value.get("expect_hit", True)):
            value["query_permission"] = "admin" if doc_decision.final_permission == "confidential" else "user"
        metadata = value.setdefault("metadata", {})
        metadata["review_status"] = "auto_approved_technical"
        metadata["retrieval_style"] = decision.retrieval_style
        output_cases.append(value)

    provenance = copy.deepcopy(source_dataset.get("provenance", {}))
    provenance.update(
        {
            "label_status": "technical_gold_draft",
            "review_model": model,
            "review_runtime": "local_ollama",
            "review_prompt_version": PROMPT_VERSION,
            "business_approval_complete": False,
            "external_data_transfer": False,
        }
    )
    return {
        "version": "2.0",
        "name": "p1.5-enterprise-technical-gold-draft",
        "dataset_type": "enterprise_private_gold_draft",
        "evaluation_scope": "answer_and_retrieval",
        "provenance": provenance,
        "documents": output_documents,
        "cases": output_cases,
    }


def read_csv(path: Path) -> list[dict[str, str]]:
    with path.open(encoding="utf-8-sig", newline="") as handle:
        return list(csv.DictReader(handle))


def write_csv(path: Path, fieldnames: list[str], rows: list[dict[str, Any]]) -> None:
    with path.open("w", encoding="utf-8-sig", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fieldnames, extrasaction="ignore")
        writer.writeheader()
        writer.writerows(rows)


def write_json(path: Path, value: Any) -> None:
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Review a private enterprise silver dataset locally")
    parser.add_argument("--input-dir", default=str(DEFAULT_INPUT))
    parser.add_argument("--ollama-endpoint", default="http://127.0.0.1:11434")
    parser.add_argument("--model", default="qwen2.5:1.5b")
    parser.add_argument("--model-timeout", type=int, default=180)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    input_dir = Path(args.input_dir)
    source_dataset = json.loads((input_dir / "silver-candidates.json").read_text(encoding="utf-8"))
    documents = source_dataset.get("documents", [])
    cases = source_dataset.get("cases", [])
    if not isinstance(documents, list) or not isinstance(cases, list):
        print("ERROR: invalid silver-candidates.json", file=sys.stderr)
        return 2

    document_rows = {row["document_id"]: row for row in read_csv(input_dir / "document-review.csv")}
    case_rows = {row["case_id"]: row for row in read_csv(input_dir / "case-review.csv")}
    conflict_rows = read_csv(input_dir / "conflict-review.csv")
    conflict_ids: set[str] = set()
    for row in conflict_rows:
        effective = str(row.get("effective_document", "")).strip()
        decision = str(row.get("review_decision", "")).strip()
        pair = {str(row.get("document_id_a", "")), str(row.get("document_id_b", ""))}
        if decision == "approved" and effective in pair:
            conflict_ids.update(pair - {effective})
        else:
            conflict_ids.update(pair)

    cases_by_document: dict[str, list[dict[str, Any]]] = {}
    for case in cases:
        cases_by_document.setdefault(str(case.get("document_id", "")), []).append(case)

    cache_path = input_dir / "automated-review-cache.json"
    cache = json.loads(cache_path.read_text(encoding="utf-8")) if cache_path.exists() else {}
    semantic_reviews: dict[str, dict[str, Any]] = {}
    model_errors: dict[str, str] = {}
    for index, document in enumerate(documents, start=1):
        document_id = str(document.get("id", ""))
        case_inputs = []
        for case in cases_by_document.get(document_id, []):
            metadata = case.get("metadata") if isinstance(case.get("metadata"), dict) else {}
            if metadata.get("category") in {"permission_negative", "no_answer"}:
                continue
            row = case_rows.get(str(case.get("id", "")), {})
            case_inputs.append(
                {
                    "case_id": str(case.get("id", "")),
                    "question": str(case.get("query", "")),
                    "reference_answer": str(case.get("reference_answer", "")),
                    "evidence": str(row.get("evidence", "")),
                }
            )
        key = cache_key(document, case_inputs, args.model)
        cached = cache.get(key)
        print(f"[review] {index}/{len(documents)} document {document_id}")
        if isinstance(cached, dict):
            semantic_reviews[document_id] = cached
            continue
        try:
            value = ollama_review(
                args.ollama_endpoint,
                args.model,
                semantic_prompt(document, case_inputs),
                review_schema([item["case_id"] for item in case_inputs]),
                args.model_timeout,
            )
            semantic_reviews[document_id] = value
            cache[key] = value
            write_json(cache_path, cache)
        except Exception as exc:
            model_errors[document_id] = type(exc).__name__

    document_decisions: dict[str, DocumentDecision] = {}
    integrity_reasons: dict[str, str] = {}
    reviewed_documents: dict[str, dict[str, Any]] = {}
    for document in documents:
        document_id = str(document.get("id", ""))
        row = document_rows.get(document_id, {})
        integrity_ok, integrity_reason = verify_source_integrity(document, row)
        integrity_reasons[document_id] = integrity_reason
        semantic = semantic_reviews.get(document_id, {})
        decision = review_document(
            document,
            row,
            semantic,
            conflict_ids=conflict_ids,
            integrity_ok=integrity_ok,
        )
        document_decisions[document_id] = decision
        updated = copy.deepcopy(document)
        updated["permission"] = decision.final_permission
        reviewed_documents[document_id] = updated

    semantic_cases: dict[str, dict[str, Any]] = {}
    for review in semantic_reviews.values():
        values = review.get("case_reviews", []) if isinstance(review, dict) else []
        if isinstance(values, list):
            for value in values:
                if isinstance(value, dict) and value.get("case_id"):
                    semantic_cases[str(value["case_id"])] = value

    case_decisions: dict[str, CaseDecision] = {}
    for case in cases:
        case_id = str(case.get("id", ""))
        document_id = str(case.get("document_id", ""))
        case_decisions[case_id] = review_case(
            case,
            case_rows.get(case_id, {}),
            reviewed_documents.get(document_id, {}),
            semantic_cases.get(case_id),
        )

    draft = build_gold_draft(
        source_dataset,
        documents,
        cases,
        document_decisions,
        case_decisions,
        args.model,
    )
    write_json(input_dir / "gold-draft.json", draft)

    automated_document_rows = []
    for document in documents:
        document_id = str(document.get("id", ""))
        decision = document_decisions[document_id]
        row = document_rows.get(document_id, {})
        automated_document_rows.append(
            {
                "document_id": document_id,
                "filename": row.get("filename", document.get("filename", "")),
                "source_integrity": integrity_reasons[document_id],
                "original_permission": row.get("permission", document.get("permission", "")),
                "proposed_permission": decision.final_permission,
                "permission_basis": decision.permission_basis,
                "business_owner_suggestion": decision.business_domain,
                "knowledge_scope_suggestion": f"enterprise-p14/{decision.business_domain}",
                "document_kind": decision.document_kind,
                "effectiveness_signal": decision.effectiveness_signal,
                "effective_status": decision.effective_status,
                "review_decision": decision.review_decision,
                "gold_eligible": str(decision.gold_eligible).lower(),
                "reason_codes": ",".join(decision.reason_codes),
            }
        )
    write_csv(
        input_dir / "automated-document-review.csv",
        [
            "document_id", "filename", "source_integrity", "original_permission",
            "proposed_permission", "permission_basis", "business_owner_suggestion",
            "knowledge_scope_suggestion", "document_kind", "effectiveness_signal",
            "effective_status", "review_decision", "gold_eligible", "reason_codes",
        ],
        automated_document_rows,
    )

    automated_case_rows = []
    for case in cases:
        case_id = str(case.get("id", ""))
        decision = case_decisions[case_id]
        automated_case_rows.append(
            {
                "case_id": case_id,
                "document_id": case.get("document_id", ""),
                "original_review_status": (case.get("metadata") or {}).get("review_status", ""),
                "technical_decision": decision.technical_decision,
                "document_gold_eligible": str(document_decisions[str(case.get("document_id", ""))].gold_eligible).lower(),
                "gold_eligible": str(
                    decision.gold_eligible
                    and document_decisions[str(case.get("document_id", ""))].gold_eligible
                ).lower(),
                "retrieval_style": decision.retrieval_style,
                "reason_codes": ",".join(decision.reason_codes),
            }
        )
    write_csv(
        input_dir / "automated-case-review.csv",
        [
            "case_id", "document_id", "original_review_status", "technical_decision",
            "document_gold_eligible", "gold_eligible", "retrieval_style", "reason_codes",
        ],
        automated_case_rows,
    )

    confirmations: list[dict[str, Any]] = []
    for document in documents:
        document_id = str(document.get("id", ""))
        decision = document_decisions[document_id]
        if decision.review_decision != "pending_business_confirmation":
            continue
        confirmations.append(
            {
                "confirmation_type": "effective_status_and_owner",
                "document_id": document_id,
                "related_document_id": "",
                "filename": document.get("filename", ""),
                "suggested_owner": decision.business_domain,
                "suggested_value": decision.effectiveness_signal,
                "reason_code": "business_authority_required",
            }
        )
    for row in conflict_rows:
        if str(row.get("review_decision", "")) == "approved" and row.get("effective_document"):
            continue
        confirmations.append(
            {
                "confirmation_type": "effective_version",
                "document_id": row.get("document_id_a", ""),
                "related_document_id": row.get("document_id_b", ""),
                "filename": "",
                "suggested_owner": "human_resources",
                "suggested_value": "",
                "reason_code": "unresolved_version_conflict",
            }
        )
    for document_id, error_type in model_errors.items():
        confirmations.append(
            {
                "confirmation_type": "local_model_retry",
                "document_id": document_id,
                "related_document_id": "",
                "filename": "",
                "suggested_owner": "",
                "suggested_value": "",
                "reason_code": error_type,
            }
        )
    write_csv(
        input_dir / "human-confirmation.csv",
        [
            "confirmation_type", "document_id", "related_document_id", "filename",
            "suggested_owner", "suggested_value", "reason_code",
        ],
        confirmations,
    )

    document_counts: dict[str, int] = {}
    for decision in document_decisions.values():
        document_counts[decision.review_decision] = document_counts.get(decision.review_decision, 0) + 1
    case_counts: dict[str, int] = {}
    case_reason_counts: dict[str, int] = {}
    case_style_counts: dict[str, int] = {}
    for decision in case_decisions.values():
        case_counts[decision.technical_decision] = case_counts.get(decision.technical_decision, 0) + 1
        case_style_counts[decision.retrieval_style] = case_style_counts.get(decision.retrieval_style, 0) + 1
        for reason in decision.reason_codes:
            case_reason_counts[reason] = case_reason_counts.get(reason, 0) + 1
    summary = {
        "source_documents": len(documents),
        "source_cases": len(cases),
        "document_decisions": dict(sorted(document_counts.items())),
        "case_decisions": dict(sorted(case_counts.items())),
        "case_reason_counts": dict(sorted(case_reason_counts.items())),
        "case_retrieval_styles": dict(sorted(case_style_counts.items())),
        "gold_draft_documents": len(draft["documents"]),
        "gold_draft_cases": len(draft["cases"]),
        "human_confirmations": len(confirmations),
        "model_errors": len(model_errors),
        "business_approval_complete": False,
        "external_data_transfer": False,
    }
    write_json(input_dir / "automated-review-summary.json", summary)
    print(json.dumps(summary, ensure_ascii=False, sort_keys=True))
    return 0 if not model_errors and draft["cases"] else 3


if __name__ == "__main__":
    sys.exit(main())
