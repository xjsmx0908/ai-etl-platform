import argparse
import importlib.util
import os
import sys
import tempfile
import types
import unittest
from pathlib import Path


sys.path.insert(0, str(Path(__file__).resolve().parents[1]))


def _minimal_result(model_mode="mock", embed_model="eval-embed"):
    """Smallest result dict write_reports() accepts."""
    return {
        "timestamp": "2026-08-08T00:00:00Z",
        "tenant_id": "tenant-test",
        "mock_port": 18080 if model_mode == "mock" else "",
        "compose_project": "ai-etl-eval-test",
        "api_base": "http://127.0.0.1:49152",
        "model_mode": model_mode,
        "models": {
            "embed_model": embed_model,
            "embed_endpoint": "http://embed.invalid/v1/embeddings",
            "embed_dimension": 768,
            "llm_model": "eval-chat",
            "llm_endpoint": "http://llm.invalid/v1/chat/completions",
            "store_collection": "documents",
        },
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
            "judge_enabled": False,
        },
        "cases": [
            {
                "case_id": "case-001",
                "expect_hit": True,
                "retrieval_assertion_pass": True,
                "answer_assertion_pass": True,
                "assertion_pass": True,
                "strict_rank": 1,
                "max_strict_rank": 1,
                "assertion_reason": "all_assertions_passed",
                "judge_error": "",
            }
        ],
    }


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

        profile = module.ModelProfile(
            mode="mock",
            embed_endpoint="http://host.docker.internal:18080/v1/embeddings",
            embed_model="eval-embed",
            embed_dim=8,
            embed_api_key="",
            llm_endpoint="http://host.docker.internal:18080/v1/chat/completions",
            llm_model="eval-chat",
            llm_api_key="",
            store_collection="documents",
        )
        env = module.compose_env(
            profile,
            tenant_id="tenant-test",
            jwt_secret="test-secret",
            compose_project="ai-etl-eval-test",
        )

        self.assertEqual(env["COMPOSE_PROJECT_NAME"], "ai-etl-eval-test")
        self.assertEqual(
            env["COMPOSE_FILE"],
            os.pathsep.join(
                (
                    str(module.ROOT / "docker-compose.yml"),
                    str(module.EVAL_COMPOSE_FILE),
                )
            ),
        )
        self.assertEqual(env["QUERY_API_HOST_PORT"], "0")
        self.assertEqual(module.api_base_from_compose_port("0.0.0.0:49152"), "http://127.0.0.1:49152")
        self.assertEqual(module.api_base_from_compose_port("[::]:49152"), "http://127.0.0.1:49152")
        self.assertEqual(module.resolve_compose_project("AI-ETL_EVAL-42"), "ai-etl_eval-42")
        with self.assertRaisesRegex(module.EvalRunnerError, "--compose-project"):
            module.resolve_compose_project("invalid project name")

    def _load_module(self, name):
        module_path = Path(__file__).resolve().parents[1] / "run-evals.py"
        spec = importlib.util.spec_from_file_location(name, module_path)
        if spec is None or spec.loader is None:
            self.fail("failed to load run-evals module")
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)
        return module

    def _args(self, **overrides):
        base = dict(
            real_models=False,
            embed_dim=768,
            store_collection="",
            embed_endpoint="",
            embed_model="",
            llm_endpoint="",
            llm_model="",
        )
        base.update(overrides)
        return argparse.Namespace(**base)

    def test_mock_profile_points_at_local_mock_server(self):
        module = self._load_module("run_evals_profile_mock")
        profile = module.resolve_model_profile(self._args(embed_dim=8), 40000)

        self.assertFalse(profile.is_real)
        self.assertEqual(profile.mode, "mock")
        self.assertIn("40000", profile.embed_endpoint)
        self.assertEqual(profile.store_collection, "documents")

    def test_real_profile_requires_endpoints_and_models(self):
        module = self._load_module("run_evals_profile_missing")
        for key in ("EMBED_ENDPOINT", "EMBED_MODEL", "LLM_ENDPOINT", "LLM_MODEL"):
            os.environ.pop(key, None)

        with self.assertRaisesRegex(module.EvalRunnerError, "--real-models requires"):
            module.resolve_model_profile(self._args(real_models=True), 18080)

    def test_real_profile_derives_per_model_collection(self):
        # Qdrant collection dimension is immutable, so a real run must not reuse
        # the 8-dim collection a mock run created.
        module = self._load_module("run_evals_profile_real")
        profile = module.resolve_model_profile(
            self._args(
                real_models=True,
                embed_dim=768,
                embed_endpoint="http://host.docker.internal:11434/api/embeddings",
                embed_model="nomic-embed-text",
                llm_endpoint="https://example.invalid/v1",
                llm_model="deepseek-v4-flash",
            ),
            0,
        )

        self.assertTrue(profile.is_real)
        self.assertEqual(profile.store_collection, "documents-real-nomic-embed-text-768")
        self.assertNotEqual(profile.store_collection, "documents")

    # ES is an async full-text sink; querying before it catches up makes the
    # eval report false retrieval timeouts for exact-keyword cases (ES carries
    # 0.75 weight). wait_for_es_sync must block until the count is reached.
    def test_wait_for_es_sync_polls_until_count_matches(self):
        module = self._load_module("run_evals_es_sync")
        calls = {"n": 0}

        def fake_run(cmd, env, check, timeout_sec):
            calls["n"] += 1
            count = "20" if calls["n"] <= 2 else "47"
            return types.SimpleNamespace(stdout='{"count": %s}' % count)

        original = module.run_cmd
        module.run_cmd = fake_run
        try:
            module.wait_for_es_sync({"ES_INDEX": "documents_text"}, 47, timeout_sec=10, poll_sec=0)
        finally:
            module.run_cmd = original

        self.assertEqual(calls["n"], 3)

    def test_wait_for_es_sync_warns_and_continues_after_timeout(self):
        # ES sync lag must NOT fail the eval: wait_for_es_sync warns and returns
        # normally when the count never reaches the target, so a slow ES sink
        # does not turn a retrieval regression into an infra false-fail.
        module = self._load_module("run_evals_es_timeout")

        def fake_run(cmd, env, check, timeout_sec):
            return types.SimpleNamespace(stdout='{"count": 10}')

        original = module.run_cmd
        module.run_cmd = fake_run
        try:
            module.wait_for_es_sync({"ES_INDEX": "documents_text"}, 47, timeout_sec=1, poll_sec=0)
        finally:
            module.run_cmd = original

    # Refusal detection must not encode any single model's phrasing. The mock
    # server's wording ("未在参考文档中直接定位锚点") used to be an accepted marker,
    # which meant the assertion could never match a real model's refusal.
    def test_refusal_markers_are_model_agnostic(self):
        module = self._load_module("run_evals_markers")

        self.assertNotIn("未在参考文档中直接定位锚点", module.NEGATIVE_FALLBACK_MARKERS)
        for phrasing in (
            "未找到相关文档，无法回答该问题。",
            "参考文档不足以回答该问题。",
            "提供的文档中不包含相关信息。",
            "无法回答这个问题。",
        ):
            self.assertTrue(
                any(marker in phrasing for marker in module.NEGATIVE_FALLBACK_MARKERS),
                f"refusal not detected: {phrasing}",
            )

    # "来源:" in a dataset means "cite the source", not "emit this substring".
    # Asserting the literal encoded the mock's output template.
    def test_citation_token_is_checked_structurally(self):
        module = self._load_module("run_evals_citation")
        case = module.EvalCase(
            case_id="t",
            filename="t.txt",
            permission="internal",
            content="x",
            query="find alpha001",
            answer_must_include=["来源:"],
        )

        cited = module.evaluate_answer_assertions(
            case,
            {
                "answer": "根据文档 case-001，alpha001 涉及重试策略。",
                "sources": [{"doc_id": "case-001"}],
            },
            [],
            True,
        )
        self.assertTrue(cited["answer_assertion_pass"], cited)

        uncited = module.evaluate_answer_assertions(
            case,
            {"answer": "alpha001 的重试策略是这样的。", "sources": [{"doc_id": "case-001"}]},
            [],
            True,
        )
        self.assertFalse(uncited["answer_assertion_pass"])
        self.assertEqual(uncited["answer_assertion_reason"], "missing_source_citation")

    # Relaxing refusal phrasing must not let a negative case pass by answering.
    def test_negative_case_still_fails_when_model_answers(self):
        module = self._load_module("run_evals_negative")
        case = module.EvalCase(
            case_id="n",
            filename="n.txt",
            permission="confidential",
            content="x",
            query="find alpha033secret",
            expect_hit=False,
        )

        answered = module.evaluate_answer_assertions(
            case,
            {
                "answer": "alpha033secret 出现在机密政策草案中，内容涉及特权审查。",
                "sources": [{"doc_id": "case-033"}],
            },
            [],
            True,
        )
        self.assertFalse(answered["answer_assertion_pass"])
        self.assertEqual(
            answered["answer_assertion_reason"], "negative_case_missing_not_found_fallback"
        )

    def test_mock_report_marks_metrics_as_non_quality_signal(self):
        module = self._load_module("run_evals_report_mock")
        with tempfile.TemporaryDirectory() as tmp:
            result = _minimal_result(model_mode="mock")
            module.write_report(Path(tmp), result)
            md = next(Path(tmp).glob("eval-*.md")).read_text(encoding="utf-8")

        self.assertIn("NOT a quality signal", md)
        self.assertIn("--real-models", md)

    def test_real_report_states_metrics_reflect_quality(self):
        module = self._load_module("run_evals_report_real")
        with tempfile.TemporaryDirectory() as tmp:
            result = _minimal_result(model_mode="real", embed_model="nomic-embed-text")
            module.write_report(Path(tmp), result)
            md = next(Path(tmp).glob("eval-*.md")).read_text(encoding="utf-8")

        self.assertIn("Model mode: REAL", md)
        self.assertIn("nomic-embed-text", md)
        self.assertNotIn("NOT a quality signal", md)


if __name__ == "__main__":
    unittest.main()
