import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT_PATH = ROOT / "scripts" / "validate_eval_dataset.py"
LEARNING_SET_PATH = ROOT / "docs" / "evals" / "engineering-learning-golden-set.json"
SPEC = importlib.util.spec_from_file_location("validate_eval_dataset", SCRIPT_PATH)
validate_eval_dataset = importlib.util.module_from_spec(SPEC)


def load_module():
    if SPEC is None or SPEC.loader is None:
        raise RuntimeError("could not load validate_eval_dataset module")
    sys.modules[SPEC.name] = validate_eval_dataset
    SPEC.loader.exec_module(validate_eval_dataset)


def make_case(number: int) -> dict:
    return {
        "id": f"history-{number:03d}",
        "filename": f"history-{number:03d}.txt",
        "permission": "internal",
        "content": f"Deidentified policy evidence {number}.",
        "query": f"What is the deidentified policy for scenario {number}?",
        "reference_answer": f"The documented policy for scenario {number} applies.",
    }


class EvalDatasetValidationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        load_module()

    def write_dataset(self, cases: list[dict]) -> Path:
        handle = tempfile.NamedTemporaryFile("w", suffix=".json", delete=False, encoding="utf-8")
        with handle:
            json.dump({"version": "1.0", "name": "history", "cases": cases}, handle)
        self.addCleanup(Path(handle.name).unlink, missing_ok=True)
        return Path(handle.name)

    def test_accepts_100_deidentified_cases_with_reference_answers(self):
        report = validate_eval_dataset.validate_dataset(
            self.write_dataset([make_case(number) for number in range(1, 101)]),
            min_cases=100,
            require_reference_answers=True,
            fail_on_sensitive_patterns=True,
        )

        self.assertTrue(report.valid)
        self.assertEqual(report.case_count, 100)
        self.assertEqual(report.errors, [])

    def test_rejects_missing_reference_answer_and_duplicate_id(self):
        cases = [make_case(number) for number in range(1, 101)]
        cases[1]["id"] = cases[0]["id"]
        cases[2]["reference_answer"] = ""

        report = validate_eval_dataset.validate_dataset(
            self.write_dataset(cases),
            min_cases=100,
            require_reference_answers=True,
            fail_on_sensitive_patterns=True,
        )

        self.assertFalse(report.valid)
        self.assertTrue(any("duplicate id" in message for message in report.errors))
        self.assertTrue(any("reference_answer is required" in message for message in report.errors))

    def test_sensitive_pattern_is_reported_without_echoing_source_value(self):
        cases = [make_case(number) for number in range(1, 101)]
        cases[0]["query"] = "Contact alice@example.com about this case."

        report = validate_eval_dataset.validate_dataset(
            self.write_dataset(cases),
            min_cases=100,
            require_reference_answers=True,
            fail_on_sensitive_patterns=True,
        )

        self.assertFalse(report.valid)
        self.assertTrue(any("email address" in message for message in report.errors))
        self.assertNotIn("alice@example.com", "\n".join(report.errors))

    def test_engineering_learning_dataset_meets_strict_100_case_gate(self):
        report = validate_eval_dataset.validate_dataset(
            LEARNING_SET_PATH,
            min_cases=100,
            require_reference_answers=True,
            fail_on_sensitive_patterns=True,
        )

        self.assertTrue(report.valid, report.errors)
        self.assertEqual(report.case_count, 100)

        data = json.loads(LEARNING_SET_PATH.read_text(encoding="utf-8"))
        categories = {case.get("metadata", {}).get("module") for case in data["cases"]}
        self.assertEqual(categories, {"etl", "retrieval", "agent", "observability"})
        self.assertGreaterEqual(sum(not case.get("expect_hit", True) for case in data["cases"]), 12)

    def test_accepts_licensed_v2_public_retrieval_dataset_without_reference_answers(self):
        dataset = {
            "version": "2.0",
            "name": "public-fixture",
            "dataset_type": "public_benchmark",
            "evaluation_scope": "retrieval",
            "provenance": {
                "license": "CC-BY-4.0",
                "source_url": "https://example.invalid/public-fixture",
                "dataset_version": "immutable-v1",
                "split": "test",
            },
            "documents": [
                {"id": "d1", "filename": "d1.txt", "permission": "internal", "content": "Evidence one."},
                {"id": "d2", "filename": "d2.txt", "permission": "internal", "content": "Evidence two."},
            ],
            "cases": [
                {
                    "id": "q1",
                    "document_id": "d1",
                    "query": "Which evidence applies?",
                    "acceptable_doc_ids": ["d1", "d2"],
                    "required_doc_ids": ["d1", "d2"],
                }
            ],
        }

        report = validate_eval_dataset.validate_dataset(
            self.write_raw_dataset(dataset),
            min_cases=1,
            require_reference_answers=True,
            fail_on_sensitive_patterns=True,
        )

        self.assertTrue(report.valid, report.errors)
        self.assertEqual(report.case_count, 1)

    def test_semantic_cohort_uses_bound_evidence_overlap_metadata(self):
        dataset = {
            "version": "2.0",
            "name": "private-semantic-fixture",
            "dataset_type": "enterprise_private_gold_candidate",
            "evaluation_scope": "answer_and_retrieval",
            "provenance": {},
            "documents": [
                {
                    "id": "d1",
                    "filename": "d1.txt",
                    "permission": "internal",
                    "content": "差旅报销材料返程十个工作日提交，员工问返程后材料什么时候提交。",
                }
            ],
            "cases": [
                {
                    "id": "q1",
                    "document_id": "d1",
                    "query": "返程后材料什么时候提交？",
                    "reference_answer": "十个工作日内。",
                    "metadata": {
                        "evaluation_cohort": "semantic",
                        "query_evidence_overlap": 0.25,
                    },
                }
            ],
        }

        report = validate_eval_dataset.validate_dataset(
            self.write_raw_dataset(dataset),
            min_cases=1,
            require_reference_answers=True,
            fail_on_sensitive_patterns=True,
        )

        self.assertTrue(report.valid, report.errors)
        self.assertFalse(any("likely keyword-match" in warning for warning in report.warnings))

    def test_rejects_v2_public_dataset_with_unknown_document_or_missing_license(self):
        dataset = {
            "version": "2.0",
            "name": "public-fixture",
            "dataset_type": "public_benchmark",
            "evaluation_scope": "retrieval",
            "provenance": {
                "source_url": "https://example.invalid/public-fixture",
                "dataset_version": "immutable-v1",
                "split": "test",
            },
            "documents": [
                {"id": "d1", "filename": "d1.txt", "permission": "internal", "content": "Evidence one."}
            ],
            "cases": [
                {
                    "id": "q1",
                    "document_id": "missing",
                    "query": "Find it",
                    "required_doc_ids": ["d1", "also-missing"],
                }
            ],
        }

        report = validate_eval_dataset.validate_dataset(
            self.write_raw_dataset(dataset),
            min_cases=1,
            require_reference_answers=True,
            fail_on_sensitive_patterns=True,
        )

        self.assertFalse(report.valid)
        self.assertTrue(any("provenance.license is required" in error for error in report.errors))
        self.assertTrue(any("unknown document_id" in error for error in report.errors))
        self.assertTrue(any("required_doc_ids contains unknown" in error for error in report.errors))

    def write_raw_dataset(self, payload: dict) -> Path:
        handle = tempfile.NamedTemporaryFile("w", suffix=".json", delete=False, encoding="utf-8")
        with handle:
            json.dump(payload, handle)
        self.addCleanup(Path(handle.name).unlink, missing_ok=True)
        return Path(handle.name)


if __name__ == "__main__":
    unittest.main()
