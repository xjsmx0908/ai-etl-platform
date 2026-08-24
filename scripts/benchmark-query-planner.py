#!/usr/bin/env python3
"""Run the public-safe activation gate for the local retrieval query planner."""

from __future__ import annotations

import argparse
import hashlib
import ipaddress
import json
import re
import sys
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any
from urllib.parse import urlparse


SYSTEM_PROMPT = """Decide whether answering the question requires evidence from at least two independent policy topics.
Return exactly one JSON object with keys activate and facets. Do not return markdown or explanations.
Rules:
1. If multiple independent topics are required, return {"activate":true,"facets":["query one","query two"]}.
2. An active facets array has exactly two or three distinct, standalone search queries in the question's language.
3. Otherwise return exactly {"activate":false,"facets":[]}. When activate is false, facets MUST be empty.
Examples:
Question: What should an event organizer check before a public conference?
{"activate":true,"facets":["public conference venue safety requirements","conference attendee personal data requirements"]}
Question: Explain the password rotation policy.
{"activate":false,"facets":[]}
Question: Find ticket TKT-10027.
{"activate":false,"facets":[]}
"""


class BenchmarkError(RuntimeError):
    pass


def require_local_endpoint(endpoint: str) -> None:
    parsed = urlparse(endpoint)
    hostname = (parsed.hostname or "").casefold()
    local = hostname in {"localhost", "host.docker.internal"}
    if not local:
        try:
            local = ipaddress.ip_address(hostname).is_loopback
        except ValueError:
            local = False
    if parsed.scheme not in {"http", "https"} or not local:
        raise BenchmarkError("planner endpoint must use a local model address")


EXACT_TOKEN_PATTERN = re.compile(r"(?i)\b[a-z0-9][a-z0-9._:/-]{5,}[a-z0-9]\b")


def exact_tokens(value: str) -> set[str]:
    tokens: set[str] = set()
    for raw in EXACT_TOKEN_PATTERN.findall(value):
        normalized = re.sub(r"[ ._:/-]", "", raw.casefold())
        has_digit = any(character.isdigit() for character in normalized)
        has_letter = any(character.isalpha() for character in normalized)
        if len(normalized) >= 6 and has_digit and (has_letter or len(normalized) >= 8):
            tokens.add(normalized)
    return tokens


def parse_plan(content: str, question: str) -> dict[str, Any]:
    try:
        value = json.loads(content)
    except json.JSONDecodeError as exc:
        raise BenchmarkError("planner content is not strict JSON") from exc
    if not isinstance(value, dict) or set(value) != {"activate", "facets"}:
        raise BenchmarkError("planner content must contain only activate and facets")
    activate = value["activate"]
    facets = value["facets"]
    if not isinstance(activate, bool) or not isinstance(facets, list):
        raise BenchmarkError("planner fields have invalid types")
    if any(not isinstance(item, str) or not item.strip() for item in facets):
        raise BenchmarkError("planner facets must be non-empty strings")
    normalized = [item.strip() for item in facets]
    if activate and len(normalized) not in (2, 3):
        raise BenchmarkError("active plans require two or three facets")
    if not activate and normalized:
        raise BenchmarkError("inactive plans cannot contain facets")
    question_key = " ".join(question.casefold().split())
    facet_keys = [" ".join(item.casefold().split()) for item in normalized]
    if any(len(item) > 160 for item in normalized):
        raise BenchmarkError("planner facet exceeds length limit")
    if len(set(facet_keys)) != len(facet_keys) or question_key in facet_keys:
        raise BenchmarkError("planner facets must be distinct from each other and the question")
    original_exact = exact_tokens(question)
    if any(exact_tokens(item) - original_exact for item in normalized):
        raise BenchmarkError("planner facet introduced an exact identifier")
    return {"activate": activate, "facets": normalized}


def request_plan(endpoint: str, model: str, question: str, timeout: float) -> dict[str, Any]:
    body = json.dumps(
        {
            "model": model,
            "stream": False,
            "temperature": 0,
            "response_format": {"type": "json_object"},
            "messages": [
                {"role": "system", "content": SYSTEM_PROMPT},
                {"role": "user", "content": question},
            ],
        }
    ).encode("utf-8")
    request = urllib.request.Request(
        endpoint,
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            payload = json.load(response)
    except (OSError, urllib.error.URLError, json.JSONDecodeError) as exc:
        raise BenchmarkError(f"planner request failed: {exc}") from exc
    try:
        content = payload["choices"][0]["message"]["content"]
    except (KeyError, IndexError, TypeError) as exc:
        raise BenchmarkError("planner response is missing message content") from exc
    if not isinstance(content, str):
        raise BenchmarkError("planner message content must be a string")
    return parse_plan(content, question)


def matches_expected_facet(facets: list[str], terms: list[str]) -> bool:
    expected = [str(term).strip().casefold() for term in terms if str(term).strip()]
    return any(all(term in facet.casefold() for term in expected) for facet in facets)


def safe_rate(numerator: int, denominator: int) -> float:
    return numerator / denominator if denominator else 0.0


def run_benchmark(
    dataset: dict[str, Any], endpoint: str, model: str, repetitions: int, timeout: float
) -> dict[str, Any]:
    cases = dataset.get("cases")
    if not isinstance(cases, list) or not cases:
        raise BenchmarkError("dataset cases must be a non-empty array")

    total_responses = 0
    valid_responses = 0
    cohort_totals = {"cross_document": 0, "semantic": 0, "lexical": 0}
    cohort_activations = {"cross_document": 0, "semantic": 0, "lexical": 0}
    expected_total = 0
    expected_hits = 0

    for _ in range(repetitions):
        for case in cases:
            cohort = str(case.get("cohort", "")).strip()
            question = str(case.get("question", "")).strip()
            if cohort not in cohort_totals or not question:
                raise BenchmarkError("every case needs a supported cohort and question")
            cohort_totals[cohort] += 1
            total_responses += 1
            try:
                plan = request_plan(endpoint, model, question, timeout)
            except BenchmarkError:
                continue
            valid_responses += 1
            if plan["activate"]:
                cohort_activations[cohort] += 1
            if cohort == "cross_document":
                for terms in case.get("expected_facets", []):
                    expected_total += 1
                    if matches_expected_facet(plan["facets"], terms):
                        expected_hits += 1

    metrics = {
        "valid_response_rate": safe_rate(valid_responses, total_responses),
        "cross_document_activation_rate": safe_rate(
            cohort_activations["cross_document"], cohort_totals["cross_document"]
        ),
        "expected_facet_coverage_rate": safe_rate(expected_hits, expected_total),
        "semantic_false_activation_rate": safe_rate(
            cohort_activations["semantic"], cohort_totals["semantic"]
        ),
        "lexical_activation_rate": safe_rate(
            cohort_activations["lexical"], cohort_totals["lexical"]
        ),
    }
    gate_passed = (
        metrics["valid_response_rate"] == 1.0
        and metrics["cross_document_activation_rate"] >= 0.8
        and metrics["expected_facet_coverage_rate"] >= 0.8
        and metrics["semantic_false_activation_rate"] <= 0.1
        and metrics["lexical_activation_rate"] == 0.0
    )
    return {
        "schema_version": "1.0",
        "dataset_sha256": hashlib.sha256(
            json.dumps(dataset, ensure_ascii=True, sort_keys=True, separators=(",", ":")).encode()
        ).hexdigest(),
        "model": model,
        "repetitions": repetitions,
        "case_counts": {
            name: total // repetitions for name, total in cohort_totals.items()
        },
        "metrics": metrics,
        "thresholds": {
            "valid_response_rate": 1.0,
            "cross_document_activation_rate": 0.8,
            "expected_facet_coverage_rate": 0.8,
            "semantic_false_activation_rate_max": 0.1,
            "lexical_activation_rate_max": 0.0,
        },
        "gate_passed": gate_passed,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dataset", required=True)
    parser.add_argument("--endpoint", required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--repetitions", type=int, default=3)
    parser.add_argument("--timeout", type=float, default=30.0)
    parser.add_argument("--report", required=True)
    args = parser.parse_args()
    if args.repetitions < 1:
        parser.error("--repetitions must be at least 1")

    try:
        require_local_endpoint(args.endpoint)
        dataset = json.loads(Path(args.dataset).read_text(encoding="utf-8"))
        report = run_benchmark(
            dataset, args.endpoint, args.model, args.repetitions, args.timeout
        )
    except (OSError, json.JSONDecodeError, BenchmarkError) as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2

    report_path = Path(args.report)
    report_path.parent.mkdir(parents=True, exist_ok=True)
    report_path.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(
        "Query planner public gate: " + ("PASS" if report["gate_passed"] else "FAIL")
    )
    return 0 if report["gate_passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
