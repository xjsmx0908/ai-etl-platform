#!/usr/bin/env python3
"""Validate a private historical RAG evaluation dataset before use."""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Iterable


REQUIRED_FIELDS = ("id", "filename", "permission", "content", "query")

# Max fraction of query content words that may appear verbatim in the target
# content before the case is flagged as keyword-match instead of semantic.
# Anchor-token datasets (e.g. "Find the source containing alpha001") score near
# 1.0 and tell us nothing about semantic retrieval; well-written paraphrases
# stay well below this. Real business terms (PDF, Word) are allowed to overlap.
MAX_QUERY_CONTENT_OVERLAP = 0.55

# Tokens too common to carry retrieval signal. Mostly Chinese particles and
# light verbs; Latin words are handled by the term filter (len < 4 dropped).
STOPWORDS = frozenset(
    "的 了 是 和 与 或 吗 呢 吧 啊 在 有 能 会 要 把 被 对 从 到 于 而 也 都 这 那 什么 怎么 可以 这个 那个 一个 一下 一些 哪里 多少 系统 文件 文档 内容 上传 检索 查询 支持 使用 知道 看到 搜索 知识库 请问 我们 你们 他 她 它 我 你 得 很 太 比较 超过 是否 那个 因为 所以 然后 如果 可以 应该 需要 会 不会 能不能 是不是 有没有".split()
)

SENSITIVE_PATTERNS = (
    ("email address", re.compile(r"\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b", re.IGNORECASE)),
    ("China mobile number", re.compile(r"(?<!\d)1[3-9]\d{9}(?!\d)")),
    ("China identity number", re.compile(r"(?<!\d)\d{17}[\dXx](?![\dA-Za-z])")),
    ("payment-card-like number", re.compile(r"(?<!\d)(?:\d[ -]?){15,18}\d(?!\d)")),
    ("JWT", re.compile(r"\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b")),
    ("API key", re.compile(r"\b(?:sk|rk|pk)-[A-Za-z0-9_-]{16,}\b", re.IGNORECASE)),
    ("enterprise webhook URL", re.compile(r"https?://qyapi\.weixin\.qq\.com/cgi-bin/webhook/send\?key=", re.IGNORECASE)),
    (
        "credential assignment",
        re.compile(r"\b(?:api[_-]?key|password|secret|token)\s*[:=]\s*\S{8,}", re.IGNORECASE),
    ),
)


@dataclass
class DatasetReport:
    case_count: int = 0
    errors: list[str] = field(default_factory=list)
    warnings: list[str] = field(default_factory=list)

    @property
    def valid(self) -> bool:
        return not self.errors


def validate_dataset(
    path: Path,
    *,
    min_cases: int,
    require_reference_answers: bool,
    fail_on_sensitive_patterns: bool,
) -> DatasetReport:
    report = DatasetReport()
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        report.errors.append(f"dataset cannot be read as JSON: {exc.__class__.__name__}")
        return report

    if not isinstance(raw, dict):
        report.errors.append("dataset root must be an object")
        return report
    cases = raw.get("cases")
    if not isinstance(cases, list):
        report.errors.append("dataset cases must be an array")
        return report

    report.case_count = len(cases)
    if report.case_count < min_cases:
        report.errors.append(f"dataset has {report.case_count} cases; requires at least {min_cases}")

    seen_ids: set[str] = set()
    for index, case in enumerate(cases, start=1):
        case_label = f"case #{index}"
        if not isinstance(case, dict):
            report.errors.append(f"{case_label}: must be an object")
            continue

        for field_name in REQUIRED_FIELDS:
            if not isinstance(case.get(field_name), str) or not case[field_name].strip():
                report.errors.append(f"{case_label}: {field_name} is required")

        case_id = case.get("id")
        if isinstance(case_id, str) and case_id.strip():
            normalized_id = case_id.strip()
            if normalized_id in seen_ids:
                report.errors.append(f"{case_label}: duplicate id")
            seen_ids.add(normalized_id)

        if require_reference_answers and (
            not isinstance(case.get("reference_answer"), str)
            or not case["reference_answer"].strip()
        ):
            report.errors.append(f"{case_label}: reference_answer is required")

        validate_optional_types(case, case_label, report)

        # Semantic-set guardrail: a query that mostly repeats the target
        # document's words is a keyword-match test, not a semantic one. It
        # would pass without any semantic understanding and inflate metrics.
        overlap = query_content_overlap(case)
        if overlap > MAX_QUERY_CONTENT_OVERLAP:
            report.warnings.append(
                f"{case_label}: query/content lexical overlap {overlap:.0%} "
                f"(> {MAX_QUERY_CONTENT_OVERLAP:.0%}); likely keyword-match, not semantic"
            )

        for field_name, value in text_values(case):
            for pattern_name, pattern in SENSITIVE_PATTERNS:
                if pattern.search(value):
                    message = f"{case_label}: {field_name} matches {pattern_name}"
                    if fail_on_sensitive_patterns:
                        report.errors.append(message)
                    else:
                        report.warnings.append(message)
                    break

    return report


def tokenize(text: str) -> set[str]:
    """Content words in text. Chinese is split per character (not a real
    segmenter, good enough for overlap); Latin tokens must be >= 4 chars to be
    considered signal (PDF -> pdf, but "view"/"the" drop out)."""
    text = text.lower()
    tokens: set[str] = set()
    for ch in text:
        if "一" <= ch <= "鿿" and ch not in STOPWORDS:
            tokens.add(ch)
    for match in re.findall(r"[a-z0-9]{4,}", text):
        tokens.add(match)
    return tokens


def query_content_overlap(case: dict[str, Any]) -> float:
    """Fraction of the query's content words that appear verbatim in content.

    Returns 0.0 when either side has no content words.
    """
    query = case.get("query")
    content = case.get("content")
    if not isinstance(query, str) or not isinstance(content, str):
        return 0.0
    query_tokens = tokenize(query)
    if not query_tokens:
        return 0.0
    content_tokens = tokenize(content)
    if not content_tokens:
        return 0.0
    overlap = len(query_tokens & content_tokens) / len(query_tokens)
    return overlap


def validate_optional_types(case: dict[str, Any], case_label: str, report: DatasetReport) -> None:
    if "expect_hit" in case and not isinstance(case["expect_hit"], bool):
        report.errors.append(f"{case_label}: expect_hit must be boolean")
    if "metadata" in case and not isinstance(case["metadata"], dict):
        report.errors.append(f"{case_label}: metadata must be an object")
    for field_name in (
        "acceptable_doc_ids",
        "must_not_hit_doc_ids",
        "answer_must_include",
        "answer_must_not_include",
    ):
        if field_name in case and (
            not isinstance(case[field_name], list)
            or not all(isinstance(value, str) and value.strip() for value in case[field_name])
        ):
            report.errors.append(f"{case_label}: {field_name} must be a non-empty string array")


def text_values(case: dict[str, Any]) -> Iterable[tuple[str, str]]:
    for field_name in ("content", "query", "reference_answer"):
        value = case.get(field_name)
        if isinstance(value, str):
            yield field_name, value
    metadata = case.get("metadata")
    if isinstance(metadata, dict):
        for field_name, value in metadata.items():
            if isinstance(value, str):
                yield f"metadata.{field_name}", value


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Validate a deidentified historical RAG evaluation dataset"
    )
    parser.add_argument("--golden-set", required=True, help="private golden-set JSON path")
    parser.add_argument("--min-cases", type=int, default=100)
    parser.add_argument("--allow-missing-reference-answers", action="store_true")
    parser.add_argument("--allow-sensitive-patterns", action="store_true")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    report = validate_dataset(
        Path(args.golden_set),
        min_cases=max(args.min_cases, 1),
        require_reference_answers=not args.allow_missing_reference_answers,
        fail_on_sensitive_patterns=not args.allow_sensitive_patterns,
    )
    for message in report.errors:
        print(f"ERROR: {message}")
    for message in report.warnings:
        print(f"WARNING: {message}")
    print(f"Validated {report.case_count} cases")
    return 0 if report.valid else 2


if __name__ == "__main__":
    sys.exit(main())
