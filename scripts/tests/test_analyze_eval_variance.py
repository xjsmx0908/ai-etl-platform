import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "analyze-eval-variance.py"


def _cohort(
    *,
    recall: float,
    required: float = 0.0,
    safety: float = 0.0,
    p95: float = 100.0,
    tokens: float = 10.0,
) -> dict:
    return {
        "total_cases": 1,
        "positive_cases": 1 if safety == 0.0 else 0,
        "recall_at_5_eligible_cases": 1 if safety == 0.0 else 0,
        "recall_at_5": recall,
        "all_required_docs_eligible_cases": 1 if required else 0,
        "all_required_docs_hit_rate": required,
        "citation_eligible_cases": 1 if safety == 0.0 else 0,
        "citation_complete_rate": 1.0,
        "key_fact_eligible_cases": 1 if safety == 0.0 else 0,
        "key_fact_pass_rate": 1.0,
        "safety_refusal_eligible_cases": 1 if safety else 0,
        "safety_refusal_rate": safety,
        "grounding_eligible_cases": 1 if safety == 0.0 else 0,
        "grounding_pass_rate": 1.0,
        "latency_ms": {"count": 1, "p50": p95 * 0.8, "p95": p95, "mean": p95 * 0.8},
        "total_tokens": int(tokens),
        "avg_tokens_per_case": tokens,
    }


def _report(*, rerank: bool, semantic: float, cross: float, p95: float, tokens: float) -> dict:
    config = {
        "retrieval_enable_rerank": rerank,
        "retrieval_rerank_policy": "auto",
        "retrieval_candidate_k": 50,
        "retrieval_final_top_k": 5,
        "query_top_k": 5,
        "retrieval_grounding_check": True,
        "semantic_cache_enabled": False,
        "compose_profiles": ["rerank"] if rerank else [],
        "sha256": "candidate-config" if rerank else "baseline-config",
    }
    return {
        "tenant_id": "tenant-eval",
        "model_mode": "real",
        "dataset": {"sha256": "d" * 64},
        "models": {
            "embed_model": "bge-m3",
            "embed_dimension": 1024,
            "llm_model": "qwen2.5:1.5b",
            "reranker_model": "cross-encoder/model-v1",
            "store_collection": "documents-real-bge-m3-1024",
        },
        "configuration": config,
        "summary": {
            "pass_rate": 1.0,
            "retrieval_pass_rate": 1.0,
            "run_valid": True,
            "successful_query_cases": 4,
            "total_cases": 4,
            "grounding_unavailable_cases": 0,
        },
        "cohorts": {
            "semantic": _cohort(recall=semantic, p95=p95, tokens=tokens),
            "cross_document": _cohort(
                recall=cross, required=cross, p95=p95, tokens=tokens
            ),
            "lexical": _cohort(recall=1.0, p95=p95, tokens=tokens),
            "safety_negative": _cohort(
                recall=0.0, safety=1.0, p95=p95, tokens=tokens
            ),
        },
        "cases": [{"case_id": "private-case", "assertion_pass": rerank}],
    }


class AnalyzeEvalVarianceCLITest(unittest.TestCase):
    def test_compares_three_matched_runs_per_arm_and_applies_gates(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            baseline = []
            candidate = []
            for index in range(3):
                baseline_path = root / f"baseline-{index}.json"
                candidate_path = root / f"candidate-{index}.json"
                baseline_path.write_text(
                    json.dumps(
                        _report(
                            rerank=False,
                            semantic=0.50,
                            cross=0.60,
                            p95=100.0,
                            tokens=10.0,
                        )
                    ),
                    encoding="utf-8",
                )
                candidate_path.write_text(
                    json.dumps(
                        _report(
                            rerank=True,
                            semantic=0.60,
                            cross=0.80,
                            p95=140.0,
                            tokens=11.0,
                        )
                    ),
                    encoding="utf-8",
                )
                baseline.extend(["--baseline", str(baseline_path)])
                candidate.extend(["--candidate", str(candidate_path)])

            proc = subprocess.run(
                [sys.executable, str(SCRIPT), *baseline, *candidate],
                text=True,
                capture_output=True,
                check=False,
            )

        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("Matched A/B comparison", proc.stdout)
        self.assertIn("Cohort: semantic", proc.stdout)
        self.assertIn("Cohort: cross_document", proc.stdout)
        self.assertIn("b-range", proc.stdout)
        self.assertIn("c-range", proc.stdout)
        self.assertIn("Noise floor is reported per metric", proc.stdout)
        self.assertNotIn("largest within-arm spread", proc.stdout)
        self.assertIn("Case flips: 1", proc.stdout)
        self.assertNotIn("private-case", proc.stdout)
        self.assertIn("Decision: PASS", proc.stdout)

    def test_rejects_dataset_drift_before_comparing_arms(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            args = []
            for arm, rerank in (("baseline", False), ("candidate", True)):
                for index in range(3):
                    report = _report(
                        rerank=rerank,
                        semantic=0.5 if not rerank else 0.6,
                        cross=0.6 if not rerank else 0.8,
                        p95=100.0,
                        tokens=10.0,
                    )
                    if arm == "candidate" and index == 2:
                        report["dataset"]["sha256"] = "e" * 64
                    path = root / f"{arm}-{index}.json"
                    path.write_text(json.dumps(report), encoding="utf-8")
                    args.extend([f"--{arm}", str(path)])

            proc = subprocess.run(
                [sys.executable, str(SCRIPT), *args],
                text=True,
                capture_output=True,
                check=False,
            )

        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("candidate reports contain dataset, model, tenant, or configuration drift", proc.stderr)

    def test_preserves_positional_same_arm_variance_mode(self):
        with tempfile.TemporaryDirectory() as tmp:
            paths = []
            for index in range(2):
                path = Path(tmp) / f"run-{index}.json"
                path.write_text(
                    json.dumps(_report(rerank=False, semantic=0.5, cross=0.6, p95=100, tokens=10)),
                    encoding="utf-8",
                )
                paths.append(str(path))
            proc = subprocess.run(
                [sys.executable, str(SCRIPT), *paths],
                text=True,
                capture_output=True,
                check=False,
            )

        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("Runs: 2", proc.stdout)
        self.assertIn("Case flips: 0", proc.stdout)

    def test_rejects_incomplete_run_before_ab_metrics(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            args = []
            for arm, rerank in (("baseline", False), ("candidate", True)):
                for index in range(3):
                    report = _report(
                        rerank=rerank,
                        semantic=0.5 if not rerank else 0.6,
                        cross=0.6 if not rerank else 0.8,
                        p95=100.0,
                        tokens=10.0,
                    )
                    if arm == "baseline" and index == 0:
                        report["summary"]["run_valid"] = False
                        report["summary"]["successful_query_cases"] = 3
                    path = root / f"{arm}-{index}.json"
                    path.write_text(json.dumps(report), encoding="utf-8")
                    args.extend([f"--{arm}", str(path)])
            proc = subprocess.run(
                [sys.executable, str(SCRIPT), *args],
                text=True,
                capture_output=True,
                check=False,
            )

        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("baseline contains an invalid or incomplete eval run", proc.stderr)


if __name__ == "__main__":
    unittest.main()
