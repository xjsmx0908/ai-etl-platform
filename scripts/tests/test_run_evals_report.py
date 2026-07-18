import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path


sys.path.insert(0, str(Path(__file__).resolve().parents[1]))


class JudgeReportTest(unittest.TestCase):
    def test_writes_judge_summary_and_case_scores(self):
        module_path = Path(__file__).resolve().parents[1] / "run-evals.py"
        spec = importlib.util.spec_from_file_location("run_evals", module_path)
        if spec is None or spec.loader is None:
            self.fail("failed to load run-evals module")
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)

        result = {
            "timestamp": "2026-07-13T00:00:00Z",
            "tenant_id": "tenant-test",
            "mock_port": 18080,
            "summary": {
                "total_cases": 1,
                "assertion_passed_cases": 1,
                "assertion_failed_cases": 0,
                "positive_cases": 1,
                "positive_passed_cases": 1,
                "positive_final_passed_cases": 1,
                "negative_cases": 0,
                "negative_passed_cases": 0,
                "negative_final_passed_cases": 0,
                "retrieval_pass_rate": 1.0,
                "answer_pass_rate": 1.0,
                "pass_rate": 1.0,
                "hit_rate": 1.0,
                "acceptable_hit_rate": 1.0,
                "recall_at_1": 1.0,
                "recall_at_3": 1.0,
                "recall_at_5": 1.0,
                "avg_score": 0.9,
                "avg_acceptable_score": 0.9,
                "judge_enabled": True,
                "judge_model": "mock-judge",
                "judge_attempted_cases": 1,
                "judge_error_cases": 0,
                "judge_pass_rate": 1.0,
                "judge_avg_faithfulness": 5.0,
                "judge_avg_correctness": 4.0,
                "judge_avg_relevance": 5.0,
            },
            "cases": [
                {
                    "case_id": "case-001",
                    "retrieval_assertion_pass": True,
                    "answer_assertion_pass": True,
                    "assertion_pass": True,
                    "strict_rank": 1,
                    "judge": {
                        "faithfulness_score": 5,
                        "correctness_score": 4,
                        "relevance_score": 5,
                        "overall_pass": True,
                    },
                    "judge_error": "",
                }
            ],
        }

        with tempfile.TemporaryDirectory() as directory:
            json_path, markdown_path = module.write_report(Path(directory), result)
            markdown = markdown_path.read_text(encoding="utf-8")

        self.assertTrue(json_path.name.startswith("eval-"))
        self.assertIn("Judge pass rate: 100.00%", markdown)
        self.assertIn("| case-001 | Y | Y | Y | 1 | Y | 5 | 4 | 5 | - |", markdown)

    def test_loads_engineering_learning_dataset(self):
        module_path = Path(__file__).resolve().parents[1] / "run-evals.py"
        spec = importlib.util.spec_from_file_location("run_evals_learning_dataset", module_path)
        if spec is None or spec.loader is None:
            self.fail("failed to load run-evals module")
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)

        dataset_path = (
            Path(__file__).resolve().parents[2]
            / "docs"
            / "evals"
            / "engineering-learning-golden-set.json"
        )
        cases = module.load_cases(dataset_path)

        self.assertEqual(len(cases), 100)
        self.assertEqual(sum(not case.expect_hit for case in cases), 12)
        self.assertTrue(all(case.reference_answer for case in cases))

    def test_compose_eval_environment_uses_unique_project_and_dynamic_ports(self):
        module_path = Path(__file__).resolve().parents[1] / "run-evals.py"
        spec = importlib.util.spec_from_file_location("run_evals_isolation", module_path)
        if spec is None or spec.loader is None:
            self.fail("failed to load run-evals module")
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)

        env = module.compose_env(
            mock_port=18080,
            embed_dim=8,
            tenant_id="tenant-test",
            jwt_secret="test-secret",
            compose_project="ai-etl-eval-test",
        )

        self.assertEqual(env["COMPOSE_PROJECT_NAME"], "ai-etl-eval-test")
        self.assertTrue(all(env[name] == "0" for name in module.ISOLATED_HOST_PORT_VARIABLES))
        self.assertEqual(module.api_base_from_compose_port("0.0.0.0:49152"), "http://127.0.0.1:49152")
        self.assertEqual(module.api_base_from_compose_port("[::]:49152"), "http://127.0.0.1:49152")
        self.assertEqual(module.resolve_compose_project("AI-ETL_EVAL-42"), "ai-etl_eval-42")
        with self.assertRaisesRegex(module.EvalRunnerError, "--compose-project"):
            module.resolve_compose_project("invalid project name")


if __name__ == "__main__":
    unittest.main()
