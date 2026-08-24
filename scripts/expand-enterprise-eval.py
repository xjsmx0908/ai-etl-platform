#!/usr/bin/env python3
"""Expand the private enterprise eval into explicit P1.6 cohorts.

Private source text is sent only to a loopback Ollama endpoint. The output is a
technical candidate, not business-approved acceptance gold.
"""

from __future__ import annotations

import argparse
import copy
import csv
import hashlib
import itertools
import json
from pathlib import Path
import re
import sys
from typing import Any, Iterable
from urllib import error, parse, request


ROOT = Path(__file__).resolve().parent.parent
DEFAULT_INPUT = ROOT / "docs" / "evals" / "private" / "p1.4-enterprise"
PROMPT_VERSION = 1
MAX_QUERY_CONTENT_OVERLAP = 0.55
STOPWORDS = frozenset(
    "的 了 是 和 与 或 吗 呢 吧 啊 在 有 能 会 要 把 被 对 从 到 于 而 也 都 这 那 什么 怎么 可以 "
    "这个 那个 一个 一下 一些 哪里 多少 系统 文件 文档 内容 上传 检索 查询 支持 使用 知道 看到 "
    "搜索 知识库 请问 我们 你们 他 她 它 我 你 得 很 太 比较 超过 是否 因为 所以 然后 如果 应该 "
    "需要 不会 能不能 是不是 有没有".split()
)
SENSITIVE_PATTERNS = (
    re.compile(r"(?<!\d)1[3-9]\d{9}(?!\d)"),
    re.compile(r"(?<!\d)\d{17}[\dXx](?![\dA-Za-z])"),
    re.compile(r"\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b", re.IGNORECASE),
    re.compile(r"(?:账号|密码|password|token)\s*[:：=]\s*\S{4,}", re.IGNORECASE),
)


class ExpansionError(RuntimeError):
    pass


def read_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def write_json(path: Path, value: Any) -> None:
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def read_csv(path: Path) -> list[dict[str, str]]:
    with path.open(encoding="utf-8-sig", newline="") as handle:
        return list(csv.DictReader(handle))


def normalized_text(value: str) -> str:
    return re.sub(r"\s+", "", value or "")


def content_tokens(value: str) -> set[str]:
    text = (value or "").lower()
    tokens = {char for char in text if "一" <= char <= "鿿" and char not in STOPWORDS}
    tokens.update(re.findall(r"[a-z0-9]{4,}", text))
    return tokens


def query_content_overlap(query: str, content: str) -> float:
    query_tokens = content_tokens(query)
    if not query_tokens:
        return 0.0
    return len(query_tokens & content_tokens(content)) / len(query_tokens)


def contains_sensitive_value(value: str) -> bool:
    return any(pattern.search(value or "") for pattern in SENSITIVE_PATTERNS)


def is_verbatim(content: str, evidence: str) -> bool:
    return bool(evidence.strip()) and normalized_text(evidence) in normalized_text(content)


def string_list(value: Any) -> list[str]:
    if not isinstance(value, list):
        return []
    return [str(item).strip() for item in value if isinstance(item, str) and item.strip()]


def validate_semantic_candidate(
    generated: dict[str, Any], document: dict[str, Any], evidence: str
) -> list[str]:
    reasons: list[str] = []
    query = str(generated.get("query", "")).strip()
    answer = str(generated.get("reference_answer", "")).strip()
    key_facts = string_list(generated.get("answer_must_include"))
    content = str(document.get("content", ""))
    if not query or not answer:
        reasons.append("query_or_answer_missing")
    if contains_sensitive_value(query) or contains_sensitive_value(answer):
        reasons.append("case_contains_sensitive_value")
    if not is_verbatim(content, evidence):
        reasons.append("evidence_not_verbatim")
    if query_content_overlap(query, evidence) > MAX_QUERY_CONTENT_OVERLAP:
        reasons.append("query_too_lexical")
    if not key_facts:
        reasons.append("key_fact_missing")
    normalized_evidence = normalized_text(evidence)
    normalized_answer = normalized_text(answer)
    if key_facts and not all(
        normalized_text(fact) in normalized_evidence
        and normalized_text(fact) in normalized_answer
        for fact in key_facts
    ):
        reasons.append("key_fact_not_supported")
    return reasons


def document_domain(document: dict[str, Any]) -> str:
    metadata = document.get("metadata") if isinstance(document.get("metadata"), dict) else {}
    return str(metadata.get("business_domain", "")).strip()


def source_fact_map(value: Any) -> dict[str, str]:
    if isinstance(value, dict):
        return {
            str(document_id): str(fact).strip()
            for document_id, fact in value.items()
            if str(document_id).strip() and str(fact).strip()
        }
    if isinstance(value, list):
        output: dict[str, str] = {}
        for item in value:
            if not isinstance(item, dict):
                continue
            document_id = str(item.get("document_id", "")).strip()
            fact = str(item.get("key_fact", "")).strip()
            if document_id and fact:
                output[document_id] = fact
        return output
    return {}


def validate_cross_document_candidate(
    generated: dict[str, Any],
    documents: list[dict[str, Any]],
    evidence_by_document: dict[str, str],
    expected_facts_by_document: dict[str, list[str]] | None = None,
) -> list[str]:
    reasons: list[str] = []
    document_ids = [str(document.get("id", "")) for document in documents]
    if len(documents) < 2 or len(set(document_ids)) < 2:
        reasons.append("requires_two_documents")
    domains = {document_domain(document) for document in documents}
    if "" in domains or len(domains) != 1:
        reasons.append("incompatible_business_domains")
    query = str(generated.get("query", "")).strip()
    answer = str(generated.get("reference_answer", "")).strip()
    key_facts = string_list(generated.get("answer_must_include"))
    facts_by_document = source_fact_map(generated.get("source_key_facts"))
    if not query or not answer:
        reasons.append("query_or_answer_missing")
    if contains_sensitive_value(query) or contains_sensitive_value(answer):
        reasons.append("case_contains_sensitive_value")
    if set(facts_by_document) != set(document_ids):
        reasons.append("source_key_facts_incomplete")
    if not key_facts:
        reasons.append("key_fact_missing")
    normalized_answer = normalized_text(answer)
    combined_evidence = "\n".join(evidence_by_document.values())
    if query_content_overlap(query, combined_evidence) > MAX_QUERY_CONTENT_OVERLAP:
        reasons.append("query_too_lexical")
    for document in documents:
        document_id = str(document.get("id", ""))
        evidence = evidence_by_document.get(document_id, "")
        fact = facts_by_document.get(document_id, "")
        if not is_verbatim(str(document.get("content", "")), evidence):
            reasons.append(f"evidence_not_verbatim:{document_id}")
        if not fact or normalized_text(fact) not in normalized_text(evidence):
            reasons.append(f"source_fact_not_supported:{document_id}")
        expected_facts = (expected_facts_by_document or {}).get(document_id, [])
        if expected_facts and fact not in expected_facts:
            reasons.append(f"source_fact_not_prevalidated:{document_id}")
        if fact and normalized_text(fact) not in normalized_answer:
            reasons.append(f"source_fact_missing_from_answer:{document_id}")
        if fact and fact not in key_facts:
            reasons.append(f"source_fact_missing_from_assertions:{document_id}")
    return reasons


def deterministic_case_id(cohort: str, document_ids: Iterable[str], query: str) -> str:
    value = json.dumps(
        {"cohort": cohort, "documents": sorted(document_ids), "query": normalized_text(query)},
        ensure_ascii=False,
        sort_keys=True,
    )
    digest = hashlib.sha256(value.encode("utf-8")).hexdigest()[:16]
    return f"p16-{cohort.replace('_', '-')}-{digest}"


def local_endpoint(endpoint: str) -> bool:
    parsed = parse.urlparse(endpoint)
    return parsed.scheme in {"http", "https"} and parsed.hostname in {
        "127.0.0.1",
        "localhost",
        "::1",
    }


def ollama_json(
    endpoint: str,
    model: str,
    prompt: str,
    schema: dict[str, Any],
    timeout: int,
    *,
    num_predict: int = 500,
) -> dict[str, Any]:
    if not local_endpoint(endpoint):
        raise ExpansionError("private enterprise review endpoint must be loopback")
    body = json.dumps(
        {
            "model": model,
            "stream": False,
            "think": False,
            "format": schema,
            "messages": [{"role": "user", "content": prompt}],
            "options": {
                "temperature": 0.15,
                "num_ctx": 4096,
                "num_predict": num_predict,
                "num_thread": 4,
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
    result = json.loads(str(payload.get("message", {}).get("content", "")))
    if not isinstance(result, dict):
        raise ValueError("local model returned a non-object")
    return result


def cache_key(kind: str, model: str, payload: Any) -> str:
    value = {"kind": kind, "model": model, "prompt_version": PROMPT_VERSION, "payload": payload}
    return hashlib.sha256(
        json.dumps(value, ensure_ascii=False, sort_keys=True).encode("utf-8")
    ).hexdigest()


GENERATION_SCHEMA = {
    "type": "object",
    "properties": {
        "query": {"type": "string"},
        "reference_answer": {"type": "string"},
        "answer_must_include": {
            "type": "array",
            "minItems": 1,
            "maxItems": 3,
            "items": {"type": "string"},
        },
    },
    "required": ["query", "reference_answer", "answer_must_include"],
}

REVIEW_SCHEMA = {
    "type": "object",
    "properties": {
        "evidence_supported": {"type": "boolean"},
        "answer_complete": {"type": "boolean"},
        "question_natural": {"type": "boolean"},
        "question_independent": {"type": "boolean"},
        "uses_all_sources": {"type": "boolean"},
    },
    "required": [
        "evidence_supported",
        "answer_complete",
        "question_natural",
        "question_independent",
        "uses_all_sources",
    ],
}


def semantic_prompt(source_case: dict[str, Any], evidence: str, retry: int) -> str:
    repair_instruction = ""
    if retry >= 3:
        repair_instruction = (
            "\n修复要求：以下预验证关键短语必须逐字出现在 reference_answer 和 "
            "answer_must_include 中："
            + json.dumps(string_list(source_case.get("answer_must_include")), ensure_ascii=False)
            + "。问题改用员工场景和同义表达，避免复制这些短语。\n"
        )
    return """你是本机企业 RAG 评测数据编辑。根据证据重写一条自然业务问题和完整标准答案。

约束：
- 问题必须像员工真实提问，不提文件名、文档、原文、上述内容或证据。
- 问题不得照抄证据，应使用场景化、同义表达降低字面重合。
- 答案只能使用证据中的事实，并完整回答问题。
- answer_must_include 提取 1 至 3 个证据和答案中逐字共有的最小关键短语。
- 不得输出证据之外的数字、期限、角色、条件或结论。
- 只返回 JSON Schema。

原问题：{query}
原答案：{answer}
证据：{evidence}
重试轮次：{retry}
{repair_instruction}
""".format(
        query=source_case.get("query", ""),
        answer=source_case.get("reference_answer", ""),
        evidence=evidence,
        retry=retry,
        repair_instruction=repair_instruction,
    )


def independent_review_prompt(
    generated: dict[str, Any], evidence_by_document: dict[str, str], *, cross_document: bool
) -> str:
    return """你是独立的本机企业 RAG 标注审核员。判断案例，不修改案例，不推测现实制度效力。

检查：
- evidence_supported：标准答案的每个事实是否均由证据直接支持。
- answer_complete：答案是否完整回答问题且没有缺少关键条件。
- question_natural：是否像员工自然提问，而非摘抄或机器模板。
- question_independent：问题是否不依赖“该文档/上述/原文/文件名”等不可见上下文。
- uses_all_sources：跨文档案例是否确实需要且使用每一份证据；单文档案例返回 true。
只能返回 JSON Schema。

跨文档：{cross_document}
问题与答案：{case}
证据：{evidence}
""".format(
        cross_document="是" if cross_document else "否",
        case=json.dumps(generated, ensure_ascii=False),
        evidence=json.dumps(evidence_by_document, ensure_ascii=False),
    )


def review_approved(review: dict[str, Any], *, cross_document: bool) -> bool:
    base_approved = all(
        review.get(field) is True
        for field in (
            "evidence_supported",
            "answer_complete",
            "question_natural",
            "question_independent",
        )
    )
    return base_approved and (not cross_document or review.get("uses_all_sources") is True)


def cached_call(
    cache: dict[str, Any],
    key: str,
    cache_path: Path,
    endpoint: str,
    model: str,
    prompt: str,
    schema: dict[str, Any],
    timeout: int,
) -> dict[str, Any]:
    if key not in cache:
        cache[key] = ollama_json(endpoint, model, prompt, schema, timeout)
        write_json(cache_path, cache)
    result = cache[key]
    if not isinstance(result, dict):
        raise ValueError("cached local model result is invalid")
    return result


def base_positive_case(
    source_case: dict[str, Any],
    generated: dict[str, Any],
    document: dict[str, Any],
    evidence: str,
) -> dict[str, Any]:
    permission = str(document.get("permission", "internal")).lower()
    document_id = str(document.get("id", ""))
    return {
        "id": deterministic_case_id("semantic", [document_id], str(generated["query"])),
        "document_id": document_id,
        "query": str(generated["query"]).strip(),
        "query_permission": "admin" if permission == "confidential" else "user",
        "acceptable_doc_ids": [document_id],
        "expect_hit": True,
        "max_strict_rank": 5,
        "require_source_citation": True,
        "answer_must_include": string_list(generated.get("answer_must_include")),
        "reference_answer": str(generated["reference_answer"]).strip(),
        "metadata": {
            "category": str((source_case.get("metadata") or {}).get("category", "fact")),
            "generated_from_case_id": str(source_case.get("id", "")),
            "generation_prompt_version": str(PROMPT_VERSION),
            "local_review_status": "approved",
            "query_evidence_overlap": round(
                query_content_overlap(str(generated["query"]), evidence), 4
            ),
        },
    }


def cross_generation_schema(document_ids: list[str]) -> dict[str, Any]:
    return {
        "type": "object",
        "properties": {
            **GENERATION_SCHEMA["properties"],
            "source_key_facts": {
                "type": "array",
                "minItems": len(document_ids),
                "maxItems": len(document_ids),
                "items": {
                    "type": "object",
                    "properties": {
                        "document_id": {"type": "string", "enum": document_ids},
                        "key_fact": {"type": "string"},
                    },
                    "required": ["document_id", "key_fact"],
                },
            },
        },
        "required": [
            "query",
            "reference_answer",
            "answer_must_include",
            "source_key_facts",
        ],
    }


def cross_prompt(sources: list[dict[str, Any]]) -> str:
    return """你是本机企业 RAG 跨文档评测数据编辑。用两份同业务域证据生成一条必须结合两者才能完整回答的问题。

约束：
- 问题必须像员工真实业务提问，不提文件名、文档、原文或证据。
- 问题使用场景化和同义表达，不照抄证据。
- 答案明确分别覆盖两份证据，不能引入证据外事实。
- 每份来源从其“预验证关键短语”中原样选择一个，source_key_facts 必须覆盖所有 document_id。
- answer_must_include 必须包含每份来源的关键短语。
- 只返回 JSON Schema。

来源：{sources}
""".format(sources=json.dumps(sources, ensure_ascii=False))


def materialize_cross_candidate(
    generated: dict[str, Any], sources: list[dict[str, Any]]
) -> dict[str, Any]:
    result = copy.deepcopy(generated)
    answers = [str(source.get("reference_answer", "")).strip().rstrip("。；;") for source in sources]
    facts = [
        string_list(source.get("prevalidated_key_facts"))[0]
        for source in sources
        if string_list(source.get("prevalidated_key_facts"))
    ]
    result["reference_answer"] = "；".join(value for value in answers if value) + "。"
    result["answer_must_include"] = facts
    result["source_key_facts"] = [
        {"document_id": source["document_id"], "key_fact": fact}
        for source, fact in zip(sources, facts)
    ]
    return result


def build_dataset(
    source: dict[str, Any],
    lexical: list[dict[str, Any]],
    semantic: list[dict[str, Any]],
    cross_document: list[dict[str, Any]],
    safety: list[dict[str, Any]],
) -> dict[str, Any]:
    output_cases: list[dict[str, Any]] = []
    for cohort, cases in (
        ("lexical", lexical),
        ("semantic", semantic),
        ("cross_document", cross_document),
        ("safety_negative", safety),
    ):
        for original in cases:
            case = copy.deepcopy(original)
            metadata = case.setdefault("metadata", {})
            metadata["evaluation_cohort"] = cohort
            metadata["retrieval_style"] = cohort
            if cohort in {"lexical", "safety_negative"}:
                metadata.setdefault("local_review_status", "approved_p15")
            output_cases.append(case)
    provenance = copy.deepcopy(source.get("provenance", {}))
    provenance.update(
        {
            "label_status": "technical_gold_candidate_v2",
            "generation_runtime": "local_ollama",
            "generation_prompt_version": PROMPT_VERSION,
            "independent_local_review": True,
            "business_approval_complete": False,
            "external_data_transfer": False,
        }
    )
    return {
        "version": "2.0",
        "name": "p1.6-enterprise-technical-gold-candidate-v2",
        "dataset_type": "enterprise_private_gold_candidate",
        "evaluation_scope": "answer_and_retrieval",
        "provenance": provenance,
        "documents": copy.deepcopy(source.get("documents", [])),
        "cases": output_cases,
    }


def validate_final_dataset(
    dataset: dict[str, Any],
    targets: dict[str, int],
    evidence_bindings: dict[str, dict[str, str]] | None = None,
) -> list[str]:
    errors: list[str] = []
    documents = dataset.get("documents") if isinstance(dataset.get("documents"), list) else []
    cases = dataset.get("cases") if isinstance(dataset.get("cases"), list) else []
    documents_by_id = {str(document.get("id", "")): document for document in documents}
    document_ids = set(documents_by_id)
    counts: dict[str, int] = {}
    seen_ids: set[str] = set()
    for case in cases:
        case_id = str(case.get("id", ""))
        if not case_id or case_id in seen_ids:
            errors.append("case ids must be present and unique")
        seen_ids.add(case_id)
        metadata = case.get("metadata") if isinstance(case.get("metadata"), dict) else {}
        cohort = str(metadata.get("evaluation_cohort", ""))
        counts[cohort] = counts.get(cohort, 0) + 1
        if metadata.get("local_review_status") not in {"approved", "approved_p15"}:
            errors.append(f"case {case_id} lacks approved local review")
        document_id = str(case.get("document_id", ""))
        if document_id not in document_ids:
            errors.append(f"case {case_id} has unknown document_id")
        required_ids = string_list(case.get("required_doc_ids"))
        unknown = sorted(set(required_ids) - document_ids)
        if unknown:
            errors.append(f"case {case_id} has unknown required_doc_ids")
        if cohort == "cross_document" and len(set(required_ids)) < 2:
            errors.append(f"case {case_id} does not require two documents")
        if bool(case.get("expect_hit", True)) and (
            not str(case.get("query", "")).strip()
            or not str(case.get("reference_answer", "")).strip()
        ):
            errors.append(f"case {case_id} lacks positive query or answer")
        if evidence_bindings is not None and cohort in {"semantic", "cross_document"}:
            bound = evidence_bindings.get(case_id, {})
            expected_ids = set(required_ids) if cohort == "cross_document" else {document_id}
            if set(bound) != expected_ids:
                errors.append(f"case {case_id} has incomplete evidence bindings")
                continue
            combined_evidence = "\n".join(bound.values())
            for source_id, evidence in bound.items():
                source = documents_by_id.get(source_id, {})
                if not is_verbatim(str(source.get("content", "")), evidence):
                    errors.append(f"case {case_id} evidence is not verbatim for a required source")
            answer = normalized_text(str(case.get("reference_answer", "")))
            normalized_evidence = normalized_text(combined_evidence)
            for fact in string_list(case.get("answer_must_include")):
                if normalized_text(fact) not in normalized_evidence:
                    errors.append(f"case {case_id} key fact is absent from bound evidence")
                if normalized_text(fact) not in answer:
                    errors.append(f"case {case_id} key fact is absent from reference answer")
    for cohort, target in targets.items():
        if counts.get(cohort, 0) < target:
            errors.append(f"cohort {cohort} has {counts.get(cohort, 0)} cases; requires {target}")
    return errors


def enrich_documents(
    candidate: dict[str, Any], document_reviews: dict[str, dict[str, str]]
) -> dict[str, Any]:
    result = copy.deepcopy(candidate)
    for document in result.get("documents", []):
        document_id = str(document.get("id", ""))
        row = document_reviews.get(document_id, {})
        metadata = document.setdefault("metadata", {})
        metadata["business_domain"] = row.get("business_owner_suggestion", "unknown") or "unknown"
        metadata["knowledge_scope"] = row.get("knowledge_scope_suggestion", "")
        metadata["document_kind"] = row.get("document_kind", "other") or "other"
    return result


def semantic_source_rows(
    silver: dict[str, Any], eligible_ids: set[str], case_reviews: dict[str, dict[str, str]]
) -> list[dict[str, Any]]:
    output = []
    for case in silver.get("cases", []):
        row = case_reviews.get(str(case.get("id", "")), {})
        if str(case.get("document_id", "")) not in eligible_ids:
            continue
        if not bool(case.get("expect_hit", True)):
            continue
        if row.get("technical_decision") == "approved":
            continue
        output.append(case)
    return sorted(output, key=lambda value: str(value.get("id", "")))


def generate_semantic_cases(
    source_cases: list[dict[str, Any]],
    documents: dict[str, dict[str, Any]],
    evidence_rows: dict[str, dict[str, str]],
    generation_cache: dict[str, Any],
    review_cache: dict[str, Any],
    generation_cache_path: Path,
    review_cache_path: Path,
    args: argparse.Namespace,
    needed: int,
) -> tuple[list[dict[str, Any]], dict[str, dict[str, str]], dict[str, int]]:
    accepted: list[dict[str, Any]] = []
    bindings: dict[str, dict[str, str]] = {}
    stats = {
        "attempted": 0,
        "mechanical_rejected": 0,
        "review_rejected": 0,
        "duplicate_skipped": 0,
        "model_errors": 0,
    }
    for source_case in source_cases:
        if len(accepted) >= needed:
            break
        document_id = str(source_case.get("document_id", ""))
        document = documents[document_id]
        evidence = str(evidence_rows.get(str(source_case.get("id", "")), {}).get("evidence", ""))
        if not is_verbatim(str(document.get("content", "")), evidence):
            stats["mechanical_rejected"] += 1
            continue
        for retry in range(1, args.max_generation_attempts + 1):
            stats["attempted"] += 1
            generation_payload = {
                "case": source_case,
                "evidence": evidence,
                "retry": retry,
            }
            generation_key = cache_key("semantic_generation", args.generation_model, generation_payload)
            try:
                generated = cached_call(
                    generation_cache,
                    generation_key,
                    generation_cache_path,
                    args.ollama_endpoint,
                    args.generation_model,
                    semantic_prompt(source_case, evidence, retry),
                    GENERATION_SCHEMA,
                    args.model_timeout,
                )
            except (OSError, ValueError, ExpansionError, error.URLError, TimeoutError):
                stats["model_errors"] += 1
                continue
            if validate_semantic_candidate(generated, document, evidence):
                stats["mechanical_rejected"] += 1
                continue
            review_payload = {"generated": generated, "evidence": {document_id: evidence}}
            review_key = cache_key("semantic_review", args.review_model, review_payload)
            try:
                review = cached_call(
                    review_cache,
                    review_key,
                    review_cache_path,
                    args.ollama_endpoint,
                    args.review_model,
                    independent_review_prompt(generated, {document_id: evidence}, cross_document=False),
                    REVIEW_SCHEMA,
                    args.model_timeout,
                )
            except (OSError, ValueError, ExpansionError, error.URLError, TimeoutError):
                stats["model_errors"] += 1
                continue
            if not review_approved(review, cross_document=False):
                stats["review_rejected"] += 1
                continue
            case = base_positive_case(source_case, generated, document, evidence)
            if any(item["id"] == case["id"] for item in accepted):
                stats["duplicate_skipped"] += 1
                continue
            accepted.append(case)
            bindings[case["id"]] = {document_id: evidence}
            break
    return accepted, bindings, stats


def cross_source_pairs(
    semantic_cases: list[dict[str, Any]], documents: dict[str, dict[str, Any]]
) -> list[tuple[dict[str, Any], dict[str, Any]]]:
    by_domain: dict[str, list[dict[str, Any]]] = {}
    for case in semantic_cases:
        document = documents[str(case.get("document_id", ""))]
        domain = document_domain(document)
        if domain and domain != "unknown":
            by_domain.setdefault(domain, []).append(case)
    pairs = []
    for domain in sorted(by_domain):
        for left, right in itertools.combinations(by_domain[domain], 2):
            if left.get("document_id") != right.get("document_id"):
                pairs.append((left, right))
    return pairs


def generate_cross_cases(
    semantic_cases: list[dict[str, Any]],
    documents: dict[str, dict[str, Any]],
    semantic_bindings: dict[str, dict[str, str]],
    generation_cache: dict[str, Any],
    review_cache: dict[str, Any],
    generation_cache_path: Path,
    review_cache_path: Path,
    args: argparse.Namespace,
) -> tuple[list[dict[str, Any]], dict[str, dict[str, str]], dict[str, int]]:
    accepted: list[dict[str, Any]] = []
    bindings: dict[str, dict[str, str]] = {}
    stats = {
        "attempted": 0,
        "mechanical_rejected": 0,
        "review_rejected": 0,
        "duplicate_skipped": 0,
        "model_errors": 0,
    }
    for left, right in cross_source_pairs(semantic_cases, documents):
        if len(accepted) >= args.cross_target:
            break
        document_ids = [str(left["document_id"]), str(right["document_id"])]
        source_documents = [documents[value] for value in document_ids]
        evidence = {
            document_id: semantic_bindings[case["id"]][document_id]
            for document_id, case in zip(document_ids, (left, right))
        }
        sources = [
            {
                "document_id": document_id,
                "question_context": case.get("query", ""),
                "reference_answer": case.get("reference_answer", ""),
                "evidence": evidence[document_id],
                "prevalidated_key_facts": string_list(case.get("answer_must_include")),
            }
            for document_id, case in zip(document_ids, (left, right))
        ]
        stats["attempted"] += 1
        generation_key = cache_key("cross_generation", args.generation_model, sources)
        try:
            generated = cached_call(
                generation_cache,
                generation_key,
                generation_cache_path,
                args.ollama_endpoint,
                args.generation_model,
                cross_prompt(sources),
                cross_generation_schema(document_ids),
                args.model_timeout,
            )
        except (OSError, ValueError, ExpansionError, error.URLError, TimeoutError):
            stats["model_errors"] += 1
            continue
        generated = materialize_cross_candidate(generated, sources)
        expected_facts = {
            source["document_id"]: source["prevalidated_key_facts"] for source in sources
        }
        if validate_cross_document_candidate(
            generated, source_documents, evidence, expected_facts
        ):
            stats["mechanical_rejected"] += 1
            continue
        review_payload = {"generated": generated, "evidence": evidence}
        review_key = cache_key("cross_review", args.review_model, review_payload)
        try:
            review = cached_call(
                review_cache,
                review_key,
                review_cache_path,
                args.ollama_endpoint,
                args.review_model,
                independent_review_prompt(generated, evidence, cross_document=True),
                REVIEW_SCHEMA,
                args.model_timeout,
            )
        except (OSError, ValueError, ExpansionError, error.URLError, TimeoutError):
            stats["model_errors"] += 1
            continue
        if not review_approved(review, cross_document=True):
            stats["review_rejected"] += 1
            continue
        facts = source_fact_map(generated.get("source_key_facts"))
        permissions = {str(document.get("permission", "internal")).lower() for document in source_documents}
        query = str(generated["query"]).strip()
        case_id = deterministic_case_id("cross_document", document_ids, query)
        if any(item["id"] == case_id for item in accepted):
            stats["duplicate_skipped"] += 1
            continue
        accepted.append(
            {
                "id": case_id,
                "document_id": document_ids[0],
                "required_doc_ids": document_ids,
                "acceptable_doc_ids": document_ids,
                "query": query,
                "query_permission": "admin" if "confidential" in permissions else "user",
                "expect_hit": True,
                "max_strict_rank": 5,
                "require_source_citation": True,
                "answer_must_include": list(facts.values()),
                "reference_answer": str(generated["reference_answer"]).strip(),
                "metadata": {
                    "category": "cross_document",
                    "business_domain": document_domain(source_documents[0]),
                    "generation_prompt_version": str(PROMPT_VERSION),
                    "local_review_status": "approved",
                    "query_evidence_overlap": round(
                        query_content_overlap(query, "\n".join(evidence.values())), 4
                    ),
                },
            }
        )
        bindings[case_id] = evidence
    return accepted, bindings, stats


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Expand a private enterprise eval candidate locally")
    parser.add_argument("--input-dir", default=str(DEFAULT_INPUT))
    parser.add_argument("--ollama-endpoint", default="http://127.0.0.1:11434")
    parser.add_argument("--generation-model", default="qwen3:4b")
    parser.add_argument("--review-model", default="qwen2.5:1.5b")
    parser.add_argument("--model-timeout", type=int, default=180)
    parser.add_argument("--max-generation-attempts", type=int, default=3)
    parser.add_argument("--semantic-target", type=int, default=30)
    parser.add_argument("--lexical-target", type=int, default=15)
    parser.add_argument("--cross-target", type=int, default=10)
    parser.add_argument("--safety-target", type=int, default=15)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if not local_endpoint(args.ollama_endpoint):
        print("ERROR: private enterprise models must use a loopback endpoint", file=sys.stderr)
        return 2
    input_dir = Path(args.input_dir)
    candidate = read_json(input_dir / "gold-candidate.json")
    silver = read_json(input_dir / "silver-candidates.json")
    document_reviews = {
        row["document_id"]: row for row in read_csv(input_dir / "automated-document-review.csv")
    }
    case_reviews = {
        row["case_id"]: row for row in read_csv(input_dir / "automated-case-review.csv")
    }
    evidence_rows = {row["case_id"]: row for row in read_csv(input_dir / "case-review.csv")}
    candidate = enrich_documents(candidate, document_reviews)
    documents = {str(document["id"]): document for document in candidate.get("documents", [])}

    lexical = []
    existing_semantic = []
    safety = []
    existing_bindings: dict[str, dict[str, str]] = {}
    for case in candidate.get("cases", []):
        style = str((case.get("metadata") or {}).get("retrieval_style", ""))
        value = copy.deepcopy(case)
        if style == "lexical":
            lexical.append(value)
        elif style == "semantic":
            value.setdefault("metadata", {})["local_review_status"] = "approved_p15"
            existing_semantic.append(value)
            row = evidence_rows.get(str(case.get("id", "")), {})
            evidence = str(row.get("evidence", ""))
            if evidence:
                existing_bindings[str(case.get("id", ""))] = {
                    str(case.get("document_id", "")): evidence
                }
                value.setdefault("metadata", {})["query_evidence_overlap"] = round(
                    query_content_overlap(str(case.get("query", "")), evidence), 4
                )
        elif style == "safety_negative":
            safety.append(value)

    semantic_needed = max(args.semantic_target - len(existing_semantic), 0)
    generation_cache_path = input_dir / "p16-generation-cache.json"
    review_cache_path = input_dir / "p16-review-cache.json"
    generation_cache = read_json(generation_cache_path) if generation_cache_path.exists() else {}
    review_cache = read_json(review_cache_path) if review_cache_path.exists() else {}
    sources = semantic_source_rows(silver, set(documents), case_reviews)
    print(
        json.dumps(
            {
                "phase": "semantic_generation",
                "source_candidates": len(sources),
                "existing_semantic": len(existing_semantic),
                "needed": semantic_needed,
            },
            sort_keys=True,
        )
    )
    generated_semantic, semantic_bindings, semantic_stats = generate_semantic_cases(
        sources,
        documents,
        evidence_rows,
        generation_cache,
        review_cache,
        generation_cache_path,
        review_cache_path,
        args,
        semantic_needed,
    )
    semantic = [*existing_semantic, *generated_semantic]
    all_semantic_bindings = {**existing_bindings, **semantic_bindings}
    print(json.dumps({"phase": "semantic_complete", "accepted": len(semantic), **semantic_stats}, sort_keys=True))

    cross, cross_bindings, cross_stats = generate_cross_cases(
        semantic,
        documents,
        all_semantic_bindings,
        generation_cache,
        review_cache,
        generation_cache_path,
        review_cache_path,
        args,
    )
    print(json.dumps({"phase": "cross_complete", "accepted": len(cross), **cross_stats}, sort_keys=True))

    dataset = build_dataset(candidate, lexical, semantic, cross, safety)
    targets = {
        "semantic": args.semantic_target,
        "lexical": args.lexical_target,
        "cross_document": args.cross_target,
        "safety_negative": args.safety_target,
    }
    bindings = {**all_semantic_bindings, **cross_bindings}
    validation_errors = validate_final_dataset(dataset, targets, bindings)
    if validation_errors:
        print(
            json.dumps(
                {"status": "failed", "validation_error_counts": len(validation_errors), "errors": validation_errors},
                ensure_ascii=False,
                sort_keys=True,
            ),
            file=sys.stderr,
        )
        return 3

    write_json(input_dir / "p16-evidence-bindings.json", bindings)
    write_json(input_dir / "gold-candidate-v2.json", dataset)
    summary = {
        "status": "completed",
        "documents": len(dataset["documents"]),
        "cases": len(dataset["cases"]),
        "cohorts": {
            name: sum(
                (case.get("metadata") or {}).get("evaluation_cohort") == name
                for case in dataset["cases"]
            )
            for name in targets
        },
        "semantic_generation": semantic_stats,
        "cross_generation": cross_stats,
        "business_approval_complete": False,
        "external_data_transfer": False,
    }
    write_json(input_dir / "p16-summary.json", summary)
    print(json.dumps(summary, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
