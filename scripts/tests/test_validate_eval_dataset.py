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


if __name__ == "__main__":
    unittest.main()
