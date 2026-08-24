#!/usr/bin/env python3
"""Build a private, evidence-backed enterprise silver evaluation dataset.

Run this script inside the parser-service image so every source document uses
the same parser as production. Question drafting uses a local Ollama model; no
document content is sent to an external endpoint.
"""

from __future__ import annotations

import argparse
import copy
import csv
from difflib import SequenceMatcher
import hashlib
from itertools import combinations
import json
import re
import sys
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any, Iterable
from urllib import request


ROOT = Path(__file__).resolve().parent.parent
DEFAULT_INPUT = ROOT / "rag_datas"
DEFAULT_OUTPUT = ROOT / "docs" / "evals" / "private" / "p1.4-enterprise"
SILVER_CONFIDENCE = 0.85
MIN_CONTENT_CHARS = 200
PROMPT_VERSION = 4

SENSITIVE_FILENAME_RULES = (
    ("credentials", re.compile(r"账号|密码", re.IGNORECASE)),
    ("attendance", re.compile(r"打卡|考勤|年休假|年假")),
    ("expense_record", re.compile(r"报销|提交的出差")),
    ("personnel_list", re.compile(r"名单|即时激励")),
    ("tax_record", re.compile(r"个税|所得税")),
)

PII_PATTERNS = (
    ("china_mobile", re.compile(r"(?<!\d)1[3-9]\d{9}(?!\d)")),
    ("china_identity", re.compile(r"(?<!\d)\d{17}[\dXx](?![\dA-Za-z])")),
    ("email", re.compile(r"\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b", re.IGNORECASE)),
    ("credential_assignment", re.compile(r"(?:账号|密码|password|token)\s*[:：=]\s*\S{4,}", re.IGNORECASE)),
)

QUESTION_SCHEMA = {
    "type": "object",
    "properties": {
        "cases": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "question": {"type": "string"},
                    "reference_answer": {"type": "string"},
                    "evidence": {"type": "string"},
                    "answer_key_phrases": {"type": "array", "items": {"type": "string"}},
                    "category": {
                        "type": "string",
                        "enum": ["fact", "procedure", "policy", "time", "amount", "list", "definition"],
                    },
                    "confidence": {"type": "number"},
                },
                "required": [
                    "question",
                    "reference_answer",
                    "evidence",
                    "answer_key_phrases",
                    "category",
                    "confidence",
                ],
            },
        }
    },
    "required": ["cases"],
}


def question_schema(count: int, evidence_ids: list[str] | None = None) -> dict[str, Any]:
    if count <= 0:
        raise ValueError("question count must be positive")
    schema = copy.deepcopy(QUESTION_SCHEMA)
    cases = schema["properties"]["cases"]
    cases["minItems"] = count
    cases["maxItems"] = count
    properties = cases["items"]["properties"]
    properties["question"]["maxLength"] = 160
    properties["reference_answer"]["maxLength"] = 300
    properties["evidence"]["minLength"] = 15
    properties["evidence"]["maxLength"] = 120
    properties["answer_key_phrases"]["minItems"] = 1
    properties["answer_key_phrases"]["maxItems"] = 2
    properties["answer_key_phrases"]["items"]["maxLength"] = 40
    if evidence_ids is not None:
        properties.pop("evidence")
        properties["evidence_id"] = {"type": "string", "enum": evidence_ids}
        required = cases["items"]["required"]
        required[required.index("evidence")] = "evidence_id"
    return schema


def merge_candidates(
    existing: list[dict[str, Any]], drafted: list[dict[str, Any]], *, limit: int
) -> list[dict[str, Any]]:
    merged: list[dict[str, Any]] = []
    seen_questions: set[str] = set()
    for candidate in [*existing, *drafted]:
        if not isinstance(candidate, dict):
            continue
        question = str(candidate.get("question", "")).strip()
        if not question or question in seen_questions:
            continue
        seen_questions.add(question)
        merged.append(candidate)
        if len(merged) >= limit:
            break
    return merged


def timeout_document_ids(rejected: Iterable[dict[str, Any]]) -> set[str]:
    return {
        str(item.get("document_id", ""))
        for item in rejected
        if "timeout" in str(item.get("reason", "")).lower()
        and str(item.get("document_id", ""))
    }


def generation_profile(document_id: str, retry_ids: set[str]) -> tuple[int, int]:
    return (3_000, 600) if document_id in retry_ids else (5_000, 700)


@dataclass
class CandidateValidation:
    accepted: bool
    requires_review: bool
    reason: str
    evidence_start: int = -1
    evidence_locator: str = ""
    supported_key_phrases: list[str] = field(default_factory=list)


@dataclass
class ExtractedDocument:
    document_id: str
    filename: str
    source_path: str
    sha256: str
    file_size: int
    extension: str
    parser: str
    content: str
    permission: str
    sensitivity_flags: list[str]
    pii_flags: list[str]
    permission_requires_review: bool
    quality_flags: list[str]


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def stable_document_id(_filename: str, digest: str) -> str:
    return f"enterprise-{digest[:16].lower()}"


def classify_document(filename: str) -> tuple[str, list[str], bool]:
    flags = [name for name, pattern in SENSITIVE_FILENAME_RULES if pattern.search(filename)]
    if flags:
        return "confidential", flags, True
    return "internal", [], False


def scan_pii(text: str) -> list[str]:
    return [name for name, pattern in PII_PATTERNS if pattern.search(text)]


def extraction_quality_flags(text: str) -> list[str]:
    return ["low_content"] if len(text.strip()) < MIN_CONTENT_CHARS else []


def normalized_title(filename: str) -> str:
    title = Path(filename).stem.lower()
    title = re.sub(r"[（(]\d+[）)]", "", title)
    title = re.sub(r"\d{4,}", "", title)
    return re.sub(r"[^\w\u4e00-\u9fff]+", "", title)


def content_shingles(content: str, size: int = 5) -> set[str]:
    normalized = re.sub(r"\s+", "", content)
    return {normalized[index : index + size] for index in range(max(0, len(normalized) - size + 1))}


def find_potential_conflicts(documents: list[ExtractedDocument]) -> list[dict[str, Any]]:
    pairs: list[dict[str, Any]] = []
    shingle_cache = {document.document_id: content_shingles(document.content) for document in documents}
    for first, second in combinations(documents, 2):
        first_title = normalized_title(first.filename)
        second_title = normalized_title(second.filename)
        title_score = SequenceMatcher(None, first_title, second_title).ratio()
        first_shingles = shingle_cache[first.document_id]
        second_shingles = shingle_cache[second.document_id]
        union = first_shingles | second_shingles
        content_score = len(first_shingles & second_shingles) / len(union) if union else 0.0
        if title_score < 0.88 and content_score < 0.45:
            continue
        pairs.append(
            {
                "document_id_a": first.document_id,
                "filename_a": first.filename,
                "document_id_b": second.document_id,
                "filename_b": second.filename,
                "title_similarity": f"{title_score:.3f}",
                "content_similarity": f"{content_score:.3f}",
                "reason": "possible_version_or_overlap",
                "effective_document": "",
                "review_decision": "pending",
            }
        )
    return pairs


def normalize_layout(text: str) -> str:
    return re.sub(r"\s+", "", text)


def evidence_position(content: str, evidence: str) -> int:
    direct = content.find(evidence)
    if direct >= 0:
        return direct
    positions = [index for index, char in enumerate(content) if not char.isspace()]
    normalized_content = "".join(content[index] for index in positions)
    normalized_evidence = normalize_layout(evidence)
    normalized_position = normalized_content.find(normalized_evidence)
    if normalized_position < 0:
        return -1
    return positions[normalized_position]


def evidence_locator(content: str, position: int) -> str:
    markers = list(
        re.finditer(r"---\s+(?:Page\s+\d+|Slide\s+\d+|Sheet:\s*[^-\n]+)\s+---", content[: max(position, 0)])
    )
    return markers[-1].group(0).strip("- ") if markers else "document"


def validate_candidate(candidate: dict[str, Any], content: str) -> CandidateValidation:
    question = str(candidate.get("question", "")).strip()
    answer = str(candidate.get("reference_answer", "")).strip()
    evidence = str(candidate.get("evidence", "")).strip()
    phrases = candidate.get("answer_key_phrases", [])
    try:
        confidence = float(candidate.get("confidence", 0))
    except (TypeError, ValueError):
        confidence = 0
    if not 6 <= len(question) <= 240:
        return CandidateValidation(False, True, "invalid_question")
    if not 2 <= len(answer) <= 1200:
        return CandidateValidation(False, True, "invalid_answer")
    if not 6 <= len(evidence) <= 5000:
        return CandidateValidation(False, True, "invalid_evidence")
    if scan_pii(question) or scan_pii(answer):
        return CandidateValidation(False, True, "candidate_contains_pii")
    position = evidence_position(content, evidence)
    if position < 0:
        return CandidateValidation(False, True, "evidence_not_found")
    if not isinstance(phrases, list) or not phrases or not all(isinstance(value, str) for value in phrases):
        return CandidateValidation(False, True, "missing_key_phrases")
    normalized_evidence = normalize_layout(evidence)
    normalized_answer = normalize_layout(answer)
    supported_phrases = []
    for phrase in phrases:
        value = normalize_layout(phrase)
        if value and value in normalized_evidence and value in normalized_answer:
            supported_phrases.append(str(phrase).strip())
    if not supported_phrases:
        return CandidateValidation(False, True, "unsupported_key_phrase")
    if not 0 <= confidence <= 1:
        return CandidateValidation(False, True, "invalid_confidence")
    low_confidence = confidence < SILVER_CONFIDENCE
    return CandidateValidation(
        True,
        low_confidence,
        "low_confidence" if low_confidence else "evidence_verified",
        position,
        evidence_locator(content, position),
        supported_phrases,
    )


def source_path_for(path: Path) -> str:
    try:
        return str(path.resolve().relative_to(ROOT.resolve()))
    except ValueError:
        return str(path.resolve())


def extract_document(path: Path) -> ExtractedDocument:
    parser_root = ROOT / "services" / "doc-parser-service"
    if str(parser_root) not in sys.path:
        sys.path.insert(0, str(parser_root))
    from app.services.parser import parse_document

    digest = sha256_file(path)
    content, file_size, parser_name = parse_document(str(path))
    content = content.strip()
    if not content:
        raise ValueError("parser returned no text")
    permission, sensitivity_flags, permission_requires_review = classify_document(path.name)
    return ExtractedDocument(
        document_id=stable_document_id(path.name, digest),
        filename=path.name,
        source_path=source_path_for(path),
        sha256=digest,
        file_size=file_size,
        extension=path.suffix.lower(),
        parser=parser_name,
        content=content,
        permission=permission,
        sensitivity_flags=sensitivity_flags,
        pii_flags=scan_pii(content),
        permission_requires_review=permission_requires_review,
        quality_flags=extraction_quality_flags(content),
    )


def representative_content(content: str, max_chars: int = 5_000) -> str:
    if len(content) <= max_chars:
        return content
    segment = max_chars // 3
    middle = max((len(content) - segment) // 2, segment)
    return "\n[中间省略]\n".join(
        (content[:segment], content[middle : middle + segment], content[-segment:])
    )


def _usable_evidence(text: str, min_chars: int, max_chars: int) -> bool:
    stripped = text.strip()
    meaningful = sum(char.isalnum() or "\u4e00" <= char <= "\u9fff" for char in stripped)
    return (
        min_chars <= len(stripped) <= max_chars
        and meaningful >= 12
        and not stripped.startswith("---")
        and not scan_pii(stripped)
    )


def select_evidence_passages(
    content: str,
    *,
    count: int,
    min_chars: int = 15,
    max_chars: int = 120,
) -> list[str]:
    """Select diverse, exact source spans before asking the model to draft QA."""
    spans: list[tuple[int, int]] = []
    for match in re.finditer(r"[^。！？；!?;\n]+[。！？；!?;]?", content):
        start, end = match.span()
        while start < end and content[start].isspace():
            start += 1
        while end > start and content[end - 1].isspace():
            end -= 1
        if start >= end:
            continue
        if end - start <= max_chars:
            spans.append((start, end))
            continue
        cursor = start
        while cursor < end:
            chunk_end = min(cursor + max_chars, end)
            if chunk_end < end:
                boundary = max(
                    content.rfind(delimiter, cursor + min_chars, chunk_end)
                    for delimiter in ("，", ",", "、", " ")
                )
                if boundary >= cursor + min_chars:
                    chunk_end = boundary + 1
            spans.append((cursor, chunk_end))
            cursor = chunk_end

    candidates: list[tuple[int, str]] = []
    seen: set[str] = set()
    for index, (start, end) in enumerate(spans):
        passage = content[start:end].strip()
        if not _usable_evidence(passage, min_chars, max_chars):
            for next_start, next_end in spans[index + 1 :]:
                if next_end - start > max_chars:
                    break
                combined = content[start:next_end].strip()
                if _usable_evidence(combined, min_chars, max_chars):
                    passage = combined
                    break
        normalized = normalize_layout(passage)
        if not _usable_evidence(passage, min_chars, max_chars) or normalized in seen:
            continue
        seen.add(normalized)
        candidates.append((start, passage))

    if len(candidates) <= count:
        return [passage for _, passage in candidates]
    if count == 1:
        return [candidates[len(candidates) // 2][1]]
    indexes = [round(index * (len(candidates) - 1) / (count - 1)) for index in range(count)]
    return [candidates[index][1] for index in indexes]


def ollama_json(
    endpoint: str,
    model: str,
    prompt: str,
    timeout: int,
    threads: int = 4,
    max_output_tokens: int = 700,
    response_schema: dict[str, Any] | None = None,
) -> dict[str, Any]:
    body = json.dumps(
        {
            "model": model,
            "stream": False,
            "think": False,
            "format": response_schema or QUESTION_SCHEMA,
            "messages": [{"role": "user", "content": prompt}],
            "options": {
                "temperature": 0.1,
                "num_ctx": 8192,
                "num_thread": threads,
                "num_predict": max_output_tokens,
            },
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
    content = payload.get("message", {}).get("content", "")
    parsed = json.loads(content)
    if not isinstance(parsed, dict):
        raise ValueError("local model returned a non-object JSON response")
    return parsed


def draft_questions(
    document: ExtractedDocument,
    *,
    endpoint: str,
    model: str,
    count: int,
    timeout: int,
    threads: int = 4,
    representative_chars: int = 5_000,
    max_output_tokens: int = 700,
) -> list[dict[str, Any]]:
    del representative_chars
    evidence_options = select_evidence_passages(document.content, count=max(count * 2, count))
    if not evidence_options:
        return []
    evidence_by_id = {
        f"E{index:02d}": evidence for index, evidence in enumerate(evidence_options, start=1)
    }
    formatted_evidence = "\n".join(
        f"{evidence_id}: {evidence}" for evidence_id, evidence in evidence_by_id.items()
    )
    prompt = f"""你在本机为企业 RAG 构建银标评测集。根据下方单份文档的候选证据生成 {count} 个互不重复的中文业务问题。

严格要求：
1. 只能使用候选证据明确写出的事实，不推测制度效力或现实状态。
2. evidence_id 必须从候选编号中选择，每条使用不同编号；不输出或改写证据原文。
3. reference_answer 必须由对应证据完整支持，简洁回答问题，不超过 300 字。
4. answer_key_phrases 提供 1-2 个同时原样出现在对应证据和 reference_answer 中的关键短语。
5. 问题应像员工真实提问，不得询问“文档写了什么”或直接包含答案。
6. 涉及姓名、账号、手机号等个人信息时，不生成要求复述该个人信息的问题；可询问流程或字段定义。
7. confidence 表示证据是否明确，范围 0-1。存在歧义时低于 0.85。

文件名：{document.filename}
候选证据：
{formatted_evidence}
"""
    payload = ollama_json(
        endpoint,
        model,
        prompt,
        timeout,
        threads=threads,
        max_output_tokens=max_output_tokens,
        response_schema=question_schema(count, list(evidence_by_id)),
    )
    cases = payload.get("cases", [])
    if not isinstance(cases, list):
        return []
    mapped: list[dict[str, Any]] = []
    for candidate in cases:
        if not isinstance(candidate, dict):
            continue
        evidence_id = str(candidate.get("evidence_id", ""))
        evidence = evidence_by_id.get(evidence_id)
        if evidence is None:
            continue
        mapped_candidate = {key: value for key, value in candidate.items() if key != "evidence_id"}
        mapped_candidate["evidence"] = evidence
        mapped.append(mapped_candidate)
    return mapped


def dataset_document(document: ExtractedDocument) -> dict[str, Any]:
    return {
        "id": document.document_id,
        "filename": document.filename,
        "source_path": document.source_path,
        "permission": document.permission,
        "content": document.content,
        "metadata": {
            "eval_dataset": "p1.4-enterprise-silver",
            "source_sha256": document.sha256,
            "source_extension": document.extension,
            "source_parser": document.parser,
            "extraction_quality_flags": ",".join(document.quality_flags),
            "silver_status": "automated_evidence_checked",
            "eval_cohort": "enterprise-p14",
        },
    }


def build_positive_case(
    document: ExtractedDocument,
    candidate: dict[str, Any],
    validation: CandidateValidation,
    index: int,
) -> dict[str, Any]:
    case_id = f"p14-{document.document_id.removeprefix('enterprise-')}-{index:02d}"
    return {
        "id": case_id,
        "document_id": document.document_id,
        "query": str(candidate["question"]).strip(),
        "query_permission": "admin" if document.permission == "confidential" else "user",
        "acceptable_doc_ids": [document.document_id],
        "expect_hit": True,
        "max_strict_rank": 5,
        "require_source_citation": True,
        "reference_answer": str(candidate["reference_answer"]).strip(),
        "answer_must_include": validation.supported_key_phrases,
        "metadata": {
            "category": str(candidate.get("category", "fact")),
            "silver_confidence": f"{float(candidate.get('confidence', 0)):.2f}",
            "evidence_locator": validation.evidence_locator,
            "evidence_sha256": hashlib.sha256(str(candidate["evidence"]).encode("utf-8")).hexdigest(),
            "review_status": "needs_review" if validation.requires_review else "auto_silver",
        },
        "_review": {
            "evidence": str(candidate["evidence"]).strip(),
            "confidence": float(candidate.get("confidence", 0)),
            "reason": validation.reason,
        },
    }


def permission_negative_case(document: ExtractedDocument) -> dict[str, Any]:
    suffix = document.document_id.removeprefix("enterprise-")
    return {
        "id": f"p14-permission-{suffix}",
        "document_id": document.document_id,
        "query": f"普通员工可以查看《{document.filename}》中的具体记录吗？",
        "query_permission": "user",
        "expect_hit": False,
        "require_source_citation": False,
        "reference_answer": "无权访问该机密文档，应拒绝回答且不得返回来源。",
        "metadata": {
            "category": "permission_negative",
            "silver_confidence": "1.00",
            "review_status": "permission_draft_needs_review",
        },
        "_review": {
            "evidence": "",
            "confidence": 1.0,
            "reason": "draft_permission_requires_business_confirmation",
        },
    }


def no_answer_cases(documents: list[ExtractedDocument], count: int = 5) -> list[dict[str, Any]]:
    cases = []
    eligible = [
        document
        for document in documents
        if document.permission == "internal" and not document.quality_flags
    ]
    for index, document in enumerate(eligible[:count], start=1):
        cases.append(
            {
                "id": f"p14-no-answer-{index:02d}",
                "document_id": document.document_id,
                "query": f"编号 P14-NOANSWER-{index:04d} 的 2035 年制度规定是什么？",
                "query_permission": "user",
                "expect_hit": False,
                "require_source_citation": False,
                "reference_answer": "知识库中没有该编号或制度，应拒绝回答且不得返回来源。",
                "metadata": {
                    "category": "no_answer",
                    "silver_confidence": "1.00",
                    "review_status": "auto_silver",
                },
                "_review": {"evidence": "", "confidence": 1.0, "reason": "synthetic_absent_identifier"},
            }
        )
    return cases


def public_case(case: dict[str, Any]) -> dict[str, Any]:
    return {key: value for key, value in case.items() if key != "_review"}


def write_inventory(path: Path, documents: Iterable[ExtractedDocument]) -> None:
    fields = [
        "document_id", "filename", "source_path", "sha256", "file_size", "extension", "parser",
        "content_chars", "permission", "sensitivity_flags", "pii_flags", "permission_requires_review",
        "quality_flags",
        "business_owner", "knowledge_scope", "effective_status", "review_decision",
    ]
    with path.open("w", encoding="utf-8-sig", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        for document in documents:
            writer.writerow(
                {
                    **{key: getattr(document, key) for key in fields if hasattr(document, key)},
                    "content_chars": len(document.content),
                    "sensitivity_flags": ",".join(document.sensitivity_flags),
                    "pii_flags": ",".join(document.pii_flags),
                    "quality_flags": ",".join(document.quality_flags),
                    "business_owner": "",
                    "knowledge_scope": "enterprise-p14",
                    "effective_status": "unknown",
                    "review_decision": (
                        "pending"
                        if document.permission_requires_review or document.quality_flags
                        else "sample_review"
                    ),
                }
            )


def write_conflict_review(path: Path, pairs: Iterable[dict[str, Any]]) -> None:
    fields = [
        "document_id_a",
        "filename_a",
        "document_id_b",
        "filename_b",
        "title_similarity",
        "content_similarity",
        "reason",
        "effective_document",
        "review_decision",
    ]
    with path.open("w", encoding="utf-8-sig", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        writer.writerows(pairs)


def write_case_review(path: Path, cases: Iterable[dict[str, Any]], filenames: dict[str, str]) -> None:
    fields = [
        "case_id", "review_status", "document_id", "filename", "category", "query",
        "reference_answer", "evidence_locator", "evidence", "confidence", "review_reason", "review_decision",
    ]
    with path.open("w", encoding="utf-8-sig", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        for case in cases:
            metadata = case.get("metadata", {})
            review = case.get("_review", {})
            writer.writerow(
                {
                    "case_id": case["id"],
                    "review_status": metadata.get("review_status", ""),
                    "document_id": case["document_id"],
                    "filename": filenames.get(case["document_id"], ""),
                    "category": metadata.get("category", ""),
                    "query": case["query"],
                    "reference_answer": case.get("reference_answer", ""),
                    "evidence_locator": metadata.get("evidence_locator", ""),
                    "evidence": review.get("evidence", ""),
                    "confidence": review.get("confidence", ""),
                    "review_reason": review.get("reason", ""),
                    "review_decision": "pending" if metadata.get("review_status") != "auto_silver" else "sample_review",
                }
            )


def write_json(path: Path, value: Any) -> None:
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def make_dataset(
    documents: list[ExtractedDocument],
    cases: list[dict[str, Any]],
    status: str,
    *,
    model: str,
    questions_per_document: int,
) -> dict[str, Any]:
    return {
        "version": "2.0",
        "name": f"p1.4-enterprise-{status}",
        "dataset_type": "enterprise_private_silver",
        "evaluation_scope": "answer_and_retrieval",
        "provenance": {
            "source_format": "local_enterprise_documents",
            "generation_model": model,
            "generation_runtime": "local_ollama",
            "questions_per_document": questions_per_document,
            "prompt_version": PROMPT_VERSION,
            "label_status": status,
            "external_data_transfer": False,
        },
        "documents": [dataset_document(document) for document in documents],
        "cases": [public_case(case) for case in cases],
    }


def load_cache(path: Path) -> dict[str, Any]:
    if not path.exists():
        return {}
    value = json.loads(path.read_text(encoding="utf-8"))
    return value if isinstance(value, dict) else {}


def load_json_list(path: Path) -> list[dict[str, Any]]:
    if not path.exists():
        return []
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, list):
        return []
    return [item for item in value if isinstance(item, dict)]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Build a local-only enterprise RAG silver dataset")
    parser.add_argument("--input-dir", default=str(DEFAULT_INPUT))
    parser.add_argument("--output-dir", default=str(DEFAULT_OUTPUT))
    parser.add_argument("--ollama-endpoint", default="http://host.docker.internal:11434")
    parser.add_argument("--model", default="qwen3:4b")
    parser.add_argument("--questions-per-document", type=int, default=4)
    parser.add_argument("--model-timeout", type=int, default=300)
    parser.add_argument("--model-threads", type=int, default=4)
    parser.add_argument("--extract-only", action="store_true")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    input_dir = Path(args.input_dir)
    output_dir = Path(args.output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)
    paths = sorted(path for path in input_dir.iterdir() if path.is_file())
    if not paths:
        print(f"ERROR: no source documents found in {input_dir}", file=sys.stderr)
        return 2

    documents: list[ExtractedDocument] = []
    errors: list[dict[str, str]] = []
    for index, path in enumerate(paths, start=1):
        print(f"[p1.4] extracting {index}/{len(paths)}")
        try:
            documents.append(extract_document(path))
        except Exception as exc:
            errors.append({"filename": path.name, "error_type": type(exc).__name__, "error": str(exc)[:500]})
    write_json(output_dir / "extraction-errors.json", errors)
    write_json(output_dir / "extracted-documents.json", [asdict(document) for document in documents])
    write_inventory(output_dir / "document-review.csv", documents)
    write_conflict_review(output_dir / "conflict-review.csv", find_potential_conflicts(documents))
    print(f"[p1.4] extracted={len(documents)} failed={len(errors)}")
    if errors:
        return 2
    if args.extract_only:
        return 0

    cache_path = output_dir / "generation-cache.json"
    cache = load_cache(cache_path)
    cache_metadata = {
        "model": args.model,
        "questions_per_document": args.questions_per_document,
        "prompt_version": PROMPT_VERSION,
    }
    existing_metadata = cache.get("_meta")
    if existing_metadata is not None and existing_metadata != cache_metadata:
        cache = {}
    cache["_meta"] = cache_metadata
    write_json(cache_path, cache)
    all_cases: list[dict[str, Any]] = []
    rejected: list[dict[str, Any]] = []
    retry_ids = timeout_document_ids(load_json_list(output_dir / "rejected-candidates.json"))
    for index, document in enumerate(documents, start=1):
        print(f"[p1.4] drafting {index}/{len(documents)}")
        if document.quality_flags:
            rejected.append({
                "document_id": document.document_id,
                "reason": "document_quality_review:" + ",".join(document.quality_flags),
            })
            if document.permission == "confidential":
                all_cases.append(permission_negative_case(document))
            continue
        cached = cache.get(document.sha256)
        if not isinstance(cached, list):
            cached = []
        cached = merge_candidates(cached, [], limit=args.questions_per_document)
        missing_count = args.questions_per_document - len(cached)
        if missing_count > 0:
            representative_chars, max_output_tokens = generation_profile(
                document.document_id, retry_ids
            )
            try:
                drafted = draft_questions(
                    document,
                    endpoint=args.ollama_endpoint,
                    model=args.model,
                    count=missing_count,
                    timeout=args.model_timeout,
                    threads=args.model_threads,
                    representative_chars=representative_chars,
                    max_output_tokens=max_output_tokens,
                )
                cached = merge_candidates(
                    cached,
                    drafted,
                    limit=args.questions_per_document,
                )
            except Exception as exc:
                rejected.append({
                    "document_id": document.document_id,
                    "reason": f"model_error:{type(exc).__name__}",
                })
                if not cached:
                    continue
            cache[document.sha256] = cached
            write_json(cache_path, cache)
        accepted_index = 0
        for candidate in cached:
            if not isinstance(candidate, dict):
                continue
            validation = validate_candidate(candidate, document.content)
            if not validation.accepted:
                rejected.append({"document_id": document.document_id, "reason": validation.reason})
                continue
            accepted_index += 1
            all_cases.append(build_positive_case(document, candidate, validation, accepted_index))
        if document.permission == "confidential":
            all_cases.append(permission_negative_case(document))

    all_cases.extend(no_answer_cases(documents))
    auto_silver = [case for case in all_cases if case.get("metadata", {}).get("review_status") == "auto_silver"]
    filenames = {document.document_id: document.filename for document in documents}
    write_case_review(output_dir / "case-review.csv", all_cases, filenames)
    write_json(output_dir / "rejected-candidates.json", rejected)
    write_json(
        output_dir / "silver-candidates.json",
        make_dataset(
            documents,
            all_cases,
            "candidate",
            model=args.model,
            questions_per_document=args.questions_per_document,
        ),
    )
    write_json(
        output_dir / "silver-eval.json",
        make_dataset(
            documents,
            auto_silver,
            "auto_silver",
            model=args.model,
            questions_per_document=args.questions_per_document,
        ),
    )
    print(
        f"[p1.4] cases={len(all_cases)} auto_silver={len(auto_silver)} "
        f"needs_review={len(all_cases) - len(auto_silver)} rejected={len(rejected)}"
    )
    return 0 if len(auto_silver) >= 100 else 3


if __name__ == "__main__":
    sys.exit(main())
