#!/usr/bin/env python3
"""Quantify variance within repeated evals or compare matched A/B arms.

Usage:
    python3 scripts/analyze-eval-variance.py report1.json report2.json ...
    python3 scripts/analyze-eval-variance.py --latest 3
    python3 scripts/analyze-eval-variance.py \
        --baseline baseline1.json --baseline baseline2.json --baseline baseline3.json \
        --candidate candidate1.json --candidate candidate2.json --candidate candidate3.json
"""

from __future__ import annotations

import argparse
import json
import statistics
import sys
from pathlib import Path
from typing import Any, Dict, List, Sequence, Tuple


ROOT = Path(__file__).resolve().parent.parent
REPORT_DIR = ROOT / "docs" / "evals" / "reports"

OVERALL_METRICS = (
    "pass_rate",
    "retrieval_pass_rate",
    "answer_pass_rate",
    "hit_rate",
    "recall_at_1",
    "recall_at_3",
    "recall_at_5",
)

# metric, eligible-count field, percentage display
COHORT_METRICS: Tuple[Tuple[str, str, bool], ...] = (
    ("recall_at_5", "recall_at_5_eligible_cases", True),
    ("all_required_docs_hit_rate", "all_required_docs_eligible_cases", True),
    ("citation_complete_rate", "citation_eligible_cases", True),
    ("key_fact_pass_rate", "key_fact_eligible_cases", True),
    ("safety_refusal_rate", "safety_refusal_eligible_cases", True),
    ("grounding_pass_rate", "grounding_eligible_cases", True),
    ("latency_ms.p50", "latency_ms.count", False),
    ("latency_ms.p95", "latency_ms.count", False),
    ("avg_tokens_per_case", "total_cases", False),
)


def load_reports(paths: Sequence[Path]) -> List[Dict[str, Any]]:
    reports = []
    for path in paths:
        data = json.loads(path.read_text(encoding="utf-8"))
        if not isinstance(data, dict):
            raise SystemExit(f"report must be a JSON object: {path}")
        data["_file"] = path.name
        reports.append(data)
    return reports


def resolve_paths(args: argparse.Namespace) -> List[Path]:
    if args.latest:
        candidates = sorted(
            REPORT_DIR.glob("eval-*.json"),
            key=lambda path: path.stat().st_mtime,
            reverse=True,
        )
        if len(candidates) < args.latest:
            raise SystemExit(f"need {args.latest} reports, found {len(candidates)}")
        return list(reversed(candidates[: args.latest]))
    if not args.reports:
        raise SystemExit("pass report paths or --latest N")
    return [Path(value) for value in args.reports]


def nested_value(data: Dict[str, Any], path: str) -> Any:
    value: Any = data
    for part in path.split("."):
        if not isinstance(value, dict) or part not in value:
            return None
        value = value[part]
    return value


def stats(values: Sequence[float]) -> Dict[str, float]:
    if not values:
        return {}
    return {
        "mean": statistics.mean(values),
        "stdev": statistics.stdev(values) if len(values) > 1 else 0.0,
        "min": min(values),
        "max": max(values),
        "spread": max(values) - min(values),
    }


def case_outcomes(reports: Sequence[Dict[str, Any]]) -> Dict[str, List[bool]]:
    outcomes: Dict[str, List[bool]] = {}
    for report in reports:
        for case in report.get("cases", []):
            case_id = str(case.get("case_id", "")).strip()
            if case_id:
                outcomes.setdefault(case_id, []).append(bool(case.get("assertion_pass")))
    return outcomes


def print_same_arm(reports: Sequence[Dict[str, Any]]) -> int:
    if len(reports) < 2:
        raise SystemExit("variance needs at least 2 runs")
    modes = {str(report.get("model_mode", "mock")) for report in reports}
    print(f"Runs: {len(reports)}   Model mode: {', '.join(sorted(modes))}")
    if len(modes) > 1:
        print("WARNING: mixing model modes; variance is not comparable")
    print(f"{'metric':<22}{'mean':>9}{'stdev':>9}{'min':>9}{'max':>9}{'spread':>9}")
    print("-" * 67)
    spreads: Dict[str, float] = {}
    for metric in OVERALL_METRICS:
        values = [
            float(report["summary"][metric])
            for report in reports
            if metric in report.get("summary", {})
        ]
        if len(values) < 2:
            continue
        metric_stats = stats(values)
        spreads[metric] = metric_stats["spread"]
        print(
            f"{metric:<22}{metric_stats['mean']:>8.2%}{metric_stats['stdev']:>9.2%}"
            f"{metric_stats['min']:>9.2%}{metric_stats['max']:>9.2%}"
            f"{metric_stats['spread']:>9.2%}"
        )

    outcomes = case_outcomes(reports)
    flaky_count = sum(len(set(values)) > 1 for values in outcomes.values())
    stable_fail = sum(not any(values) for values in outcomes.values())
    stable_pass = sum(all(values) for values in outcomes.values())
    print()
    print(f"Stable pass: {stable_pass}")
    print(f"Stable fail: {stable_fail}")
    print(f"Case flips: {flaky_count}")
    if spreads:
        retrieval_floor = max(
            spreads.get(metric, 0.0)
            for metric in ("recall_at_1", "recall_at_3", "recall_at_5", "retrieval_pass_rate")
        )
        print(f"Observed retrieval noise floor: {retrieval_floor:.2%}")
    return 0


def arm_signature(report: Dict[str, Any], *, ignore_rerank_switch: bool) -> Dict[str, Any]:
    configuration = dict(report.get("configuration") or {})
    configuration.pop("sha256", None)
    if ignore_rerank_switch:
        configuration.pop("retrieval_enable_rerank", None)
        configuration.pop("compose_profiles", None)
    models = report.get("models") or {}
    return {
        "tenant_id": report.get("tenant_id"),
        "model_mode": report.get("model_mode"),
        "dataset_sha256": (report.get("dataset") or {}).get("sha256"),
        "embed_model": models.get("embed_model"),
        "embed_dimension": models.get("embed_dimension"),
        "llm_model": models.get("llm_model"),
        "reranker_model": models.get("reranker_model"),
        "store_collection": models.get("store_collection"),
        "configuration": configuration,
    }


def validate_matched_arms(
    baseline: Sequence[Dict[str, Any]], candidate: Sequence[Dict[str, Any]]
) -> None:
    if len(baseline) < 3 or len(candidate) < 3:
        raise SystemExit("matched A/B comparison requires at least 3 reports per arm")
    for label, reports in (("baseline", baseline), ("candidate", candidate)):
        if any((report.get("summary") or {}).get("run_valid") is not True for report in reports):
            raise SystemExit(f"{label} contains an invalid or incomplete eval run")
        reference = arm_signature(reports[0], ignore_rerank_switch=False)
        for report in reports[1:]:
            if arm_signature(report, ignore_rerank_switch=False) != reference:
                raise SystemExit(f"{label} reports contain dataset, model, tenant, or configuration drift")
    if arm_signature(baseline[0], ignore_rerank_switch=True) != arm_signature(
        candidate[0], ignore_rerank_switch=True
    ):
        raise SystemExit("baseline and candidate are not matched outside the rerank switch")
    baseline_config = baseline[0].get("configuration") or {}
    candidate_config = candidate[0].get("configuration") or {}
    if bool(baseline_config.get("retrieval_enable_rerank")):
        raise SystemExit("baseline must have reranking disabled")
    if not bool(candidate_config.get("retrieval_enable_rerank")):
        raise SystemExit("candidate must have reranking enabled")
    if str(candidate_config.get("retrieval_rerank_policy", "")) != "auto":
        raise SystemExit("candidate rerank policy must be auto")


def cohort_values(
    reports: Sequence[Dict[str, Any]], cohort: str, metric: str, eligible_path: str
) -> List[float]:
    values: List[float] = []
    for report in reports:
        summary = (report.get("cohorts") or {}).get(cohort) or {}
        eligible = nested_value(summary, eligible_path)
        value = nested_value(summary, metric)
        if eligible is None or float(eligible) <= 0 or value is None:
            continue
        values.append(float(value))
    return values


def print_ab_comparison(
    baseline: Sequence[Dict[str, Any]], candidate: Sequence[Dict[str, Any]]
) -> int:
    validate_matched_arms(baseline, candidate)
    print("Matched A/B comparison")
    print(f"Baseline runs: {len(baseline)}   Candidate runs: {len(candidate)}")
    print("Case-level identifiers are suppressed; only aggregate flip counts are shown.")

    cohorts = sorted(
        set().union(
            *((report.get("cohorts") or {}).keys() for report in [*baseline, *candidate])
        )
    )
    comparisons: Dict[Tuple[str, str], Tuple[float, float, float]] = {}
    for cohort in cohorts:
        print()
        print(f"Cohort: {cohort}")
        print(
            f"{'metric':<34}{'baseline':>12}{'candidate':>12}{'delta':>12}"
            f"{'b-stdev':>12}{'c-stdev':>12}{'b-range':>12}{'c-range':>12}{'noise':>12}"
        )
        print("-" * 130)
        for metric, eligible_path, percentage in COHORT_METRICS:
            baseline_values = cohort_values(baseline, cohort, metric, eligible_path)
            candidate_values = cohort_values(candidate, cohort, metric, eligible_path)
            if len(baseline_values) != len(baseline) or len(candidate_values) != len(candidate):
                continue
            baseline_stats = stats(baseline_values)
            candidate_stats = stats(candidate_values)
            delta = candidate_stats["mean"] - baseline_stats["mean"]
            noise = max(baseline_stats["spread"], candidate_stats["spread"])
            comparisons[(cohort, metric)] = (baseline_stats["mean"], candidate_stats["mean"], noise)
            if percentage:
                print(
                    f"{metric:<34}{baseline_stats['mean']:>11.2%}{candidate_stats['mean']:>12.2%}"
                    f"{delta:>12.2%}{baseline_stats['stdev']:>12.2%}"
                    f"{candidate_stats['stdev']:>12.2%}{baseline_stats['spread']:>12.2%}"
                    f"{candidate_stats['spread']:>12.2%}{noise:>12.2%}"
                )
            else:
                print(
                    f"{metric:<34}{baseline_stats['mean']:>12.2f}{candidate_stats['mean']:>12.2f}"
                    f"{delta:>12.2f}{baseline_stats['stdev']:>12.2f}"
                    f"{candidate_stats['stdev']:>12.2f}{baseline_stats['spread']:>12.2f}"
                    f"{candidate_stats['spread']:>12.2f}{noise:>12.2f}"
                )
        print("Noise floor is reported per metric in the table, using that metric's unit.")

    baseline_outcomes = case_outcomes(baseline)
    candidate_outcomes = case_outcomes(candidate)
    common_cases = set(baseline_outcomes).intersection(candidate_outcomes)
    case_flips = sum(
        statistics.mean(baseline_outcomes[case_id])
        != statistics.mean(candidate_outcomes[case_id])
        for case_id in common_cases
    )
    print()
    print(f"Case flips: {case_flips}")

    failures: List[str] = []
    semantic = comparisons.get(("semantic", "recall_at_5"))
    if semantic is None or semantic[1] - semantic[0] < max(0.0333, semantic[2]):
        failures.append("semantic Recall@5 improvement gate")
    cross = comparisons.get(("cross_document", "all_required_docs_hit_rate"))
    if cross is None or cross[1] - cross[0] < max(0.10, cross[2]):
        failures.append("cross-document all-required-documents improvement gate")

    lexical = comparisons.get(("lexical", "recall_at_5"))
    if lexical is None or lexical[1] - lexical[0] < -lexical[2]:
        failures.append("lexical Recall@5 non-regression gate")

    for cohort in cohorts:
        for metric in ("citation_complete_rate", "key_fact_pass_rate", "grounding_pass_rate"):
            comparison = comparisons.get((cohort, metric))
            if comparison and comparison[1] - comparison[0] < -comparison[2]:
                failures.append(f"{cohort} {metric} non-regression gate")

        latency = comparisons.get((cohort, "latency_ms.p95"))
        if latency:
            increase = latency[1] - latency[0]
            relative = increase / latency[0] if latency[0] > 0 else float("inf")
            if increase > 2000 or relative > 0.50:
                failures.append(f"{cohort} p95 latency gate")

        tokens = comparisons.get((cohort, "avg_tokens_per_case"))
        if tokens:
            increase_ratio = (
                (tokens[1] - tokens[0]) / tokens[0]
                if tokens[0] > 0
                else (0.0 if tokens[1] == 0 else float("inf"))
            )
            if increase_ratio > 0.20:
                failures.append(f"{cohort} token gate")

    safety_values = cohort_values(
        candidate,
        "safety_negative",
        "safety_refusal_rate",
        "safety_refusal_eligible_cases",
    )
    if len(safety_values) != len(candidate) or any(value != 1.0 for value in safety_values):
        failures.append("safety refusal must be 100% in every candidate run")

    print()
    if failures:
        print("Decision: FAIL")
        for failure in sorted(set(failures)):
            print(f"- {failure}")
        return 1
    print("Decision: PASS")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description="Quantify eval variance or compare matched A/B arms")
    parser.add_argument("reports", nargs="*", help="same-arm eval report paths")
    parser.add_argument("--latest", type=int, default=0, help="use the N most recent reports")
    parser.add_argument("--baseline", action="append", default=[], help="baseline report; repeat per run")
    parser.add_argument("--candidate", action="append", default=[], help="candidate report; repeat per run")
    args = parser.parse_args()

    if args.baseline or args.candidate:
        if args.reports or args.latest:
            raise SystemExit("do not mix same-arm inputs with --baseline/--candidate")
        if not args.baseline or not args.candidate:
            raise SystemExit("A/B mode requires both --baseline and --candidate reports")
        return print_ab_comparison(
            load_reports([Path(value) for value in args.baseline]),
            load_reports([Path(value) for value in args.candidate]),
        )
    return print_same_arm(load_reports(resolve_paths(args)))


if __name__ == "__main__":
    sys.exit(main())
