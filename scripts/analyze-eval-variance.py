#!/usr/bin/env python3
"""Quantify run-to-run variance across repeated eval runs.

Repeated real-model runs on the same config and dataset do not produce identical
retrieval metrics: embedding ties and near-duplicate candidates make ranking
unstable. Before any A/B experiment (retrieval strategy, rerank on/off, chunk
size), we need to know how large a difference has to be to mean anything.

Usage:
    python3 scripts/analyze-eval-variance.py report1.json report2.json ...
    python3 scripts/analyze-eval-variance.py --latest 3
"""

from __future__ import annotations

import argparse
import json
import statistics
import sys
from pathlib import Path
from typing import Any, Dict, List


ROOT = Path(__file__).resolve().parent.parent
REPORT_DIR = ROOT / "docs" / "evals" / "reports"

# Metrics worth tracking for stability. Retrieval metrics are the volatile ones;
# answer metrics inherit that volatility plus LLM sampling.
METRICS = (
    "pass_rate",
    "retrieval_pass_rate",
    "answer_pass_rate",
    "hit_rate",
    "recall_at_1",
    "recall_at_3",
    "recall_at_5",
)


def load_reports(paths: List[Path]) -> List[Dict[str, Any]]:
    reports = []
    for p in paths:
        data = json.loads(p.read_text(encoding="utf-8"))
        data["_file"] = p.name
        reports.append(data)
    return reports


def resolve_paths(args: argparse.Namespace) -> List[Path]:
    if args.latest:
        candidates = sorted(
            REPORT_DIR.glob("eval-*.json"),
            key=lambda p: p.stat().st_mtime,
            reverse=True,
        )
        if len(candidates) < args.latest:
            raise SystemExit(f"need {args.latest} reports, found {len(candidates)}")
        return list(reversed(candidates[: args.latest]))
    if not args.reports:
        raise SystemExit("pass report paths or --latest N")
    return [Path(r) for r in args.reports]


def main() -> int:
    parser = argparse.ArgumentParser(description="Quantify eval run-to-run variance")
    parser.add_argument("reports", nargs="*", help="eval-*.json report paths")
    parser.add_argument("--latest", type=int, default=0, help="use the N most recent reports")
    args = parser.parse_args()

    reports = load_reports(resolve_paths(args))
    if len(reports) < 2:
        raise SystemExit("variance needs at least 2 runs")

    modes = {r.get("model_mode", "mock") for r in reports}
    print(f"Runs: {len(reports)}   Model mode: {', '.join(sorted(modes))}")
    if len(modes) > 1:
        print("  WARNING: mixing model modes — variance is not comparable")
    for r in reports:
        models = r.get("models", {})
        print(f"  {r['_file']}  embed={models.get('embed_model','?')} llm={models.get('llm_model','?')}")
    print()

    print(f"{'metric':<22}{'mean':>9}{'stdev':>9}{'min':>9}{'max':>9}{'spread':>9}")
    print("-" * 67)
    spreads = {}
    for metric in METRICS:
        values = [r["summary"][metric] for r in reports if metric in r["summary"]]
        if len(values) < 2:
            continue
        mean = statistics.mean(values)
        stdev = statistics.stdev(values)
        spread = max(values) - min(values)
        spreads[metric] = spread
        print(
            f"{metric:<22}{mean:>8.2%}{stdev:>9.2%}{min(values):>9.2%}"
            f"{max(values):>9.2%}{spread:>9.2%}"
        )

    # Case-level churn: which cases flip pass/fail between runs. A case that
    # flips is by definition not a reliable signal about a code change.
    per_case: Dict[str, List[bool]] = {}
    for r in reports:
        for c in r.get("cases", []):
            per_case.setdefault(c["case_id"], []).append(bool(c.get("assertion_pass")))

    flaky = {cid: v for cid, v in per_case.items() if len(set(v)) > 1}
    always_fail = sorted(cid for cid, v in per_case.items() if not any(v))
    always_pass = sum(1 for v in per_case.values() if all(v))

    print()
    print(f"Stable pass:  {always_pass}")
    print(f"Stable fail:  {len(always_fail)}  {always_fail}")
    print(f"Flaky:        {len(flaky)}")
    for cid in sorted(flaky):
        marks = "".join("P" if x else "F" for x in per_case[cid])
        print(f"   {cid}  {marks}")

    # The decision rule this whole script exists to produce.
    if spreads:
        retrieval_spread = max(
            spreads.get(m, 0)
            for m in ("recall_at_1", "recall_at_3", "recall_at_5", "retrieval_pass_rate")
        )
        overall_spread = spreads.get("pass_rate", 0)
        print()
        print("Significance floor (observed spread across identical runs):")
        print(f"  retrieval metrics: {retrieval_spread:.2%}")
        print(f"  pass_rate:         {overall_spread:.2%}")
        print()
        print(
            f"  → Treat an A/B difference below {max(retrieval_spread, overall_spread):.2%} "
            "as noise, not signal."
        )
        if flaky:
            print(
                f"  → {len(flaky)} flaky case(s) should be excluded from A/B comparisons "
                "or judged on aggregate across repeats."
            )

    return 0


if __name__ == "__main__":
    sys.exit(main())
