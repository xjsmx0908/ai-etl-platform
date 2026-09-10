import argparse
import importlib.util
import json
import os
import re
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest import mock


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
    def test_public_dataset_report_discloses_scope_license_and_sampling(self):
        module = self._load_module("run_evals_public_report")
        result = _minimal_result(model_mode="real", embed_model="real-embed")
        result["dataset"] = {
            "version": "2.0",
            "name": "nanoscifact",
            "dataset_type": "public_benchmark",
            "evaluation_scope": "retrieval",
            "document_count": 200,
            "case_count": 20,
            "provenance": {
                "license": "CC-BY-4.0",
                "source_url": "https://huggingface.co/datasets/zeta-alpha-ai/NanoSciFact",
                "dataset_version": "309f1d1",
                "split": "train",
                "benchmark_comparable": False,
                "sampling": {"max_documents": 200, "max_queries": 20, "seed": 42},
            },
        }

        with tempfile.TemporaryDirectory() as directory:
            _, markdown_path = module.write_report(Path(directory), result)
            markdown = markdown_path.read_text(encoding="utf-8")

        self.assertIn("Dataset type: public_benchmark", markdown)
        self.assertIn("Evaluation scope: retrieval", markdown)
        self.assertIn("License: CC-BY-4.0", markdown)
        self.assertIn("Benchmark comparable: NO (sampled)", markdown)
        self.assertIn("This public retrieval score is not enterprise-domain acceptance evidence.", markdown)

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

    def test_loads_v2_dataset_with_documents_separate_from_queries(self):
        module = self._load_module("run_evals_v2_dataset")
        payload = {
            "version": "2.0",
            "name": "public-fixture",
            "dataset_type": "public_benchmark",
            "evaluation_scope": "retrieval",
            "provenance": {
                "license": "Apache-2.0",
                "source_url": "https://example.invalid/data",
                "dataset_version": "v1",
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
                    "query": "Find evidence one",
                    "acceptable_doc_ids": ["d1", "d2"],
                    "required_doc_ids": ["d1", "d2"],
                    "require_source_citation": False,
                },
                {
                    "id": "q2",
                    "document_id": "d1",
                    "query": "Find the shared evidence",
                    "acceptable_doc_ids": ["d1"],
                    "require_source_citation": False,
                },
            ],
        }
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False, encoding="utf-8") as handle:
            import json

            json.dump(payload, handle)
            dataset_path = Path(handle.name)
        self.addCleanup(dataset_path.unlink, missing_ok=True)

        dataset = module.load_eval_dataset(dataset_path)

        self.assertEqual([document.document_id for document in dataset.documents], ["d1", "d2"])
        self.assertEqual([case.document_id for case in dataset.cases], ["d1", "d1"])
        self.assertEqual(dataset.cases[0].acceptable_doc_ids, ["d1", "d2"])
        self.assertEqual(dataset.cases[0].required_doc_ids, ["d1", "d2"])
        self.assertEqual(dataset.dataset_type, "public_benchmark")
        self.assertEqual(dataset.evaluation_scope, "retrieval")
        self.assertEqual(dataset.provenance["license"], "Apache-2.0")

    def test_v2_source_path_uploads_original_binary_with_detected_media_type(self):
        module = self._load_module("run_evals_binary_source")
        with tempfile.TemporaryDirectory(dir=module.ROOT) as directory:
            source = Path(directory) / "policy.xlsx"
            source.write_bytes(b"PK\x03\x04binary-workbook")
            relative_source = source.relative_to(module.ROOT)
            dataset_path = Path(directory) / "dataset.json"
            dataset_path.write_text(
                json.dumps(
                    {
                        "version": "2.0",
                        "documents": [
                            {
                                "id": "d-binary",
                                "filename": "policy.xlsx",
                                "permission": "internal",
                                "content": "extracted text used for labels",
                                "source_path": str(relative_source),
                            }
                        ],
                        "cases": [
                            {
                                "id": "q-binary",
                                "document_id": "d-binary",
                                "query": "What is the policy?",
                            }
                        ],
                    }
                ),
                encoding="utf-8",
            )

            dataset = module.load_eval_dataset(dataset_path)
            captured = {}

            def fake_http_json(method, url, headers=None, body=None, timeout=0):
                captured.update(method=method, url=url, headers=headers, body=body, timeout=timeout)
                return 202, {"doc_id": "uploaded-binary"}

            module.http_json = fake_http_json
            uploaded = module.upload_document("http://api.invalid", "token", dataset.documents[0])

        self.assertEqual(uploaded, "uploaded-binary")
        self.assertIn(b"PK\x03\x04binary-workbook", captured["body"])
        self.assertIn(b"Content-Type: application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", captured["body"])
        self.assertNotIn(b"extracted text used for labels", captured["body"])

    def test_v2_source_path_cannot_escape_repository(self):
        module = self._load_module("run_evals_source_path_boundary")
        payload = {
            "version": "2.0",
            "documents": [
                {
                    "id": "d-escape",
                    "filename": "outside.pdf",
                    "permission": "internal",
                    "content": "label text",
                    "source_path": "../outside.pdf",
                }
            ],
            "cases": [{"id": "q-escape", "document_id": "d-escape", "query": "query"}],
        }
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False, encoding="utf-8") as handle:
            json.dump(payload, handle)
            dataset_path = Path(handle.name)
        self.addCleanup(dataset_path.unlink, missing_ok=True)

        with self.assertRaisesRegex(module.EvalRunnerError, "source_path must stay within repository"):
            module.load_eval_dataset(dataset_path)

    def test_v2_source_path_must_match_declared_sha256(self):
        module = self._load_module("run_evals_source_hash")
        with tempfile.TemporaryDirectory(dir=module.ROOT) as directory:
            source = Path(directory) / "policy.pdf"
            source.write_bytes(b"current binary")
            dataset_path = Path(directory) / "dataset.json"
            dataset_path.write_text(
                json.dumps(
                    {
                        "version": "2.0",
                        "documents": [
                            {
                                "id": "d-stale",
                                "filename": "policy.pdf",
                                "content": "labels from an older file",
                                "source_path": str(source.relative_to(module.ROOT)),
                                "metadata": {"source_sha256": "0" * 64},
                            }
                        ],
                        "cases": [{"id": "q-stale", "document_id": "d-stale", "query": "query"}],
                    }
                ),
                encoding="utf-8",
            )

            with self.assertRaisesRegex(module.EvalRunnerError, "source_sha256 does not match"):
                module.load_eval_dataset(dataset_path)

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

    def test_worker_compose_lease_covers_default_retry_window(self):
        compose_text = (Path(__file__).resolve().parents[2] / "docker-compose.yml").read_text(
            encoding="utf-8"
        )
        worker_block = compose_text.split("  etl-worker:", 1)[1].split(
            "  parser-service:", 1
        )[0]

        lease = re.search(r"- INGESTION_JOB_LEASE=\$\{INGESTION_JOB_LEASE:-([^}]+)\}", worker_block)
        pipeline_timeout = re.search(r"- PIPELINE_TIMEOUT=\$\{PIPELINE_TIMEOUT:-([^}]+)\}", worker_block)
        self.assertIsNotNone(lease, "etl-worker must configure an ingestion job lease")
        self.assertIsNotNone(pipeline_timeout, "etl-worker must configure a pipeline timeout")

        def seconds(value):
            match = re.fullmatch(r"(\d+)([smh])", value)
            self.assertIsNotNone(match, f"unsupported duration in compose: {value}")
            return int(match.group(1)) * {"s": 1, "m": 60, "h": 3600}[match.group(2)]

        lease_seconds = seconds(lease.group(1))
        pipeline_seconds = seconds(pipeline_timeout.group(1))
        max_retries = 3
        retry_backoff_seconds = 0.5
        worst_case = pipeline_seconds * (max_retries + 1) + sum(
            retry_backoff_seconds * (2**attempt) for attempt in range(max_retries)
        )
        self.assertGreater(
            lease_seconds,
            worst_case,
            "worker lease must outlive the complete default pipeline retry window",
        )
        for relative_path in (".env.example", "services/etl-worker/.env.example"):
            example_text = (Path(__file__).resolve().parents[2] / relative_path).read_text(
                encoding="utf-8"
            )
            example_lease = re.search(r"^INGESTION_JOB_LEASE=(.+)$", example_text, re.MULTILINE)
            self.assertIsNotNone(example_lease, f"{relative_path} must document the worker lease")
            self.assertGreater(
                seconds(example_lease.group(1)),
                worst_case,
                f"{relative_path} lease must cover the Compose retry window",
            )

    def test_eval_compose_publishes_query_api_on_ephemeral_loopback_port(self):
        compose_path = Path(__file__).resolve().parents[2] / "docker-compose.eval.yml"
        compose_text = compose_path.read_text(encoding="utf-8")
        query_api_block = compose_text.split("  query-api:", 1)[1].split(
            "  reranker-service:", 1
        )[0]

        self.assertIn('"127.0.0.1::8080"', query_api_block)

    def test_confidential_eval_fixtures_are_uploaded_by_admin(self):
        module = self._load_module("run_evals_upload_role")

        self.assertEqual(module.upload_role_for_permission("public"), "user")
        self.assertEqual(module.upload_role_for_permission("internal"), "user")
        self.assertEqual(module.upload_role_for_permission("confidential"), "admin")
        with self.assertRaisesRegex(module.EvalRunnerError, "unsupported document permission"):
            module.upload_role_for_permission("restricted")

    def test_waits_for_all_document_tasks_before_publication(self):
        module = self._load_module("run_evals_task_wait")
        responses = {
            "doc-1": iter([(200, {"status": "queued"}), (200, {"status": "completed"})]),
            "doc-2": iter([(200, {"status": "processing"}), (200, {"status": "completed"})]),
        }
        calls = []

        def fake_http_json(method, url, **kwargs):
            calls.append((method, url))
            document_id = url.rsplit("/", 1)[-1]
            return next(responses[document_id])

        original = module.http_json
        module.http_json = fake_http_json
        self.addCleanup(lambda: setattr(module, "http_json", original))

        module.wait_for_document_tasks(
            "http://api.invalid",
            "token",
            ["doc-1", "doc-2"],
            timeout_sec=1,
            poll_sec=0,
        )

        self.assertEqual([call[0] for call in calls], ["GET", "GET", "GET", "GET"])
        self.assertTrue(all("/v1/tasks/" in call[1] for call in calls))

    def test_task_rate_limit_is_retried_during_readiness_poll(self):
        module = self._load_module("run_evals_task_rate_limit")
        responses = iter([(429, {"error": "rate_limited"}), (200, {"status": "completed"})])

        def fake_http_json(*args, **kwargs):
            return next(responses)

        original = module.http_json
        module.http_json = fake_http_json
        self.addCleanup(lambda: setattr(module, "http_json", original))

        module.wait_for_document_tasks(
            "http://api.invalid",
            "token",
            ["doc-1"],
            timeout_sec=1,
            poll_sec=0,
        )

    def test_task_rate_limit_stops_the_current_scan_before_retrying(self):
        module = self._load_module("run_evals_task_rate_limit_scan")
        calls = []
        slept = False

        def fake_http_json(*args, **kwargs):
            nonlocal slept
            calls.append(args[1])
            if len(calls) == 1:
                return 429, {"error": "rate_limited"}
            if not slept:
                self.fail("readiness poll continued scanning after a 429")
            return 200, {"status": "completed"}

        def fake_sleep(_seconds):
            nonlocal slept
            slept = True

        original_http_json = module.http_json
        original_sleep = module.time.sleep
        module.http_json = fake_http_json
        module.time.sleep = fake_sleep
        self.addCleanup(lambda: setattr(module, "http_json", original_http_json))
        self.addCleanup(lambda: setattr(module.time, "sleep", original_sleep))

        module.wait_for_document_tasks(
            "http://api.invalid",
            "token",
            ["doc-1", "doc-2"],
            timeout_sec=1,
            poll_sec=0,
        )

        self.assertEqual(len(calls), 3)

    def test_upload_map_round_trip_is_bound_to_dataset_and_tenant(self):
        module = self._load_module("run_evals_upload_map")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            dataset_path = root / "dataset.json"
            dataset_path.write_text('{"version":"2.0"}', encoding="utf-8")
            map_path = root / "upload-map.json"

            module.write_upload_map(
                map_path,
                tenant_id="tenant-eval",
                dataset_path=dataset_path,
                uploaded_doc_ids={"source-1": "doc-1", "source-2": "doc-2"},
            )

            tenant_id, uploaded = module.load_upload_map(
                map_path,
                dataset_path=dataset_path,
                expected_document_ids=["source-1", "source-2"],
            )
            self.assertEqual(tenant_id, "tenant-eval")
            self.assertEqual(uploaded, {"source-1": "doc-1", "source-2": "doc-2"})

            dataset_path.write_text('{"version":"2.1"}', encoding="utf-8")
            with self.assertRaisesRegex(module.EvalRunnerError, "dataset digest"):
                module.load_upload_map(
                    map_path,
                    dataset_path=dataset_path,
                    expected_document_ids=["source-1", "source-2"],
                )

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

    def test_resolved_configuration_records_non_secret_ab_settings(self):
        module = self._load_module("run_evals_resolved_configuration")
        env = {
            "RETRIEVAL_ENABLE_RERANK": "true",
            "RETRIEVAL_RERANK_POLICY": "auto",
            "RETRIEVAL_CANDIDATE_K": "40",
            "RETRIEVAL_FINAL_TOP_K": "5",
            "RETRIEVAL_GROUNDING_CHECK": "true",
            "RERANK_MODEL": "request-model",
            "RERANKER_MODEL": "cross-encoder/model-v1",
            "RERANKER_BACKEND": "cross-encoder",
            "COMPOSE_PROFILES": "rerank",
            "LLM_MAX_TOKENS": "64",
            "LLM_MAX_CONTEXT_CHARS": "1200",
            "PROMPT_VERSION": "v1",
            "PIPELINE_MAX_WORKERS": "1",
            "PIPELINE_BATCH_SIZE": "10",
            "PIPELINE_STAGE_TIMEOUT": "10m",
            "PIPELINE_TIMEOUT": "60m",
            "LLM_API_KEY": "must-not-leak",
        }

        config = module.resolved_eval_configuration(
            env,
            query_top_k=5,
            query_timeout_seconds=90.0,
            required_consecutive_hits=1,
            poll_interval_seconds=0.2,
            negative_max_wait_seconds=1,
        )

        self.assertTrue(config["retrieval_enable_rerank"])
        self.assertEqual(config["retrieval_rerank_policy"], "auto")
        self.assertEqual(config["retrieval_candidate_k"], 40)
        self.assertEqual(config["retrieval_final_top_k"], 5)
        self.assertEqual(config["reranker_model"], "cross-encoder/model-v1")
        self.assertEqual(config["compose_profiles"], ["rerank"])
        self.assertEqual(config["llm_max_tokens"], 64)
        self.assertEqual(config["llm_max_context_chars"], 1200)
        self.assertEqual(config["query_timeout_seconds"], 90.0)
        self.assertEqual(config["required_consecutive_hits"], 1)
        self.assertEqual(config["negative_max_wait_seconds"], 1)
        self.assertEqual(config["pipeline_max_workers"], 1)
        self.assertEqual(config["pipeline_stage_timeout"], "10m")
        self.assertRegex(config["sha256"], r"^[0-9a-f]{64}$")
        self.assertNotIn("must-not-leak", json.dumps(config))

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

    def test_query_assertion_records_final_request_latency(self):
        module = self._load_module("run_evals_query_latency")
        original_query_case = module.query_case
        original_perf_counter = module.time.perf_counter
        perf_values = iter([10.0, 10.25])
        module.query_case = lambda *args, **kwargs: (
            200,
            {
                "answer": "answer",
                "sources": [{"doc_id": "doc-1", "score": 0.9}],
                "token_usage": {"prompt_tokens": 3, "completion_tokens": 2},
            },
        )
        module.time.perf_counter = lambda: next(perf_values)
        try:
            details, _ = module.evaluate_case_assertions(
                "http://api.invalid",
                "token",
                "query",
                "doc-1",
                True,
                ["doc-1"],
                [],
                5,
                5,
                1,
                0.01,
                1,
            )
        finally:
            module.query_case = original_query_case
            module.time.perf_counter = original_perf_counter

        self.assertEqual(details["query_attempts"], 1)
        self.assertEqual(details["query_latency_ms"], 250.0)

    def test_filter_eval_cases_selects_cohort_without_changing_document_corpus(self):
        module = self._load_module("run_evals_cohort_filter")
        cases = [
            module.EvalCase(
                case_id="cross-1",
                query="combine sources",
                document_id="doc-a",
                metadata={"evaluation_cohort": "cross_document"},
                required_doc_ids=["doc-a", "doc-b"],
            ),
            module.EvalCase(
                case_id="semantic-1",
                query="semantic question",
                document_id="doc-c",
                metadata={"evaluation_cohort": "semantic"},
            ),
        ]

        selected = module.filter_eval_cases(cases, "cross_document")

        self.assertEqual([case.case_id for case in selected], ["cross-1"])
        self.assertEqual(selected[0].required_doc_ids, ["doc-a", "doc-b"])
        self.assertEqual([case.case_id for case in module.filter_eval_cases(cases)], ["cross-1", "semantic-1"])
        with self.assertRaises(module.EvalRunnerError):
            module.filter_eval_cases(cases, "missing")

    def test_query_case_marks_retrieval_only_explicitly(self):
        module = self._load_module("run_evals_retrieval_only_payload")
        captured = {}
        original = module.http_json

        def fake_http_json(method, url, headers=None, body=None, timeout=0):
            captured["payload"] = json.loads(body.decode("utf-8"))
            return 200, {"sources": []}

        module.http_json = fake_http_json
        try:
            module.query_case(
                "http://api.invalid",
                "token",
                "question",
                diagnostic_required_doc_ids=["doc-b", "doc-a", "doc-a"],
                retrieval_only=True,
            )
        finally:
            module.http_json = original

        self.assertEqual(captured["payload"]["diagnostic_required_doc_ids"], ["doc-a", "doc-b"])
        self.assertTrue(captured["payload"]["retrieval_only"])

    def test_answer_metrics_are_independent_of_citation_failure(self):
        module = self._load_module("run_evals_answer_metric_signals")
        case = module.EvalCase(
            case_id="case-1",
            query="policy question",
            require_source_citation=True,
            answer_must_include=["审批期限", "来源:"],
        )

        details = module.evaluate_answer_assertions(
            case,
            {
                "answer": "审批期限为三个工作日。",
                "sources": [{"doc_id": "doc-1"}],
            },
            ["doc-1"],
            True,
        )

        self.assertFalse(details["answer_assertion_pass"])
        self.assertEqual(details["answer_assertion_reason"], "missing_source_citation")
        self.assertTrue(details["key_fact_check_eligible"])
        self.assertTrue(details["key_fact_assertion_pass"])
        self.assertFalse(details["safety_refusal_eligible"])

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

    def test_cross_document_answer_requires_every_source_citation(self):
        module = self._load_module("run_evals_cross_citation")
        case = module.EvalCase(
            case_id="cross",
            query="How do both rules apply?",
            document_id="source-a",
            required_doc_ids=["source-a", "source-b"],
        )

        missing = module.evaluate_answer_assertions(
            case,
            {
                "answer": "Rule A applies [source-a].",
                "sources": [{"doc_id": "source-a"}, {"doc_id": "source-b"}],
            },
            ["source-a", "source-b"],
            True,
            required_doc_ids=["source-a", "source-b"],
        )
        complete = module.evaluate_answer_assertions(
            case,
            {
                "answer": "Rule A applies [source-a], and rule B applies [source-b].",
                "sources": [{"doc_id": "source-a"}, {"doc_id": "source-b"}],
            },
            ["source-a", "source-b"],
            True,
            required_doc_ids=["source-a", "source-b"],
        )

        self.assertFalse(missing["answer_assertion_pass"])
        self.assertEqual(missing["answer_assertion_reason"], "missing_required_source_citation")
        self.assertTrue(complete["answer_assertion_pass"], complete)

    def test_cross_document_retrieval_requires_all_sources_in_same_result(self):
        module = self._load_module("run_evals_cross_retrieval")
        payloads = [
            {
                "answer": "combined",
                "sources": [
                    {"doc_id": "source-a", "score": 0.9},
                    {"doc_id": "source-b", "score": 0.8},
                ],
            }
        ]
        original = module.query_case
        module.query_case = lambda *args, **kwargs: (200, payloads[0])
        try:
            details, _ = module.evaluate_case_assertions(
                "http://api.invalid",
                "token",
                "query",
                "source-a",
                True,
                ["source-a", "source-b"],
                [],
                5,
                5,
                1,
                0.001,
                1,
                required_doc_ids=["source-a", "source-b"],
            )
        finally:
            module.query_case = original

        self.assertTrue(details["assertion_pass"], details)
        self.assertTrue(details["required_docs_hit"])
        self.assertEqual(details["required_doc_ranks"], {"source-a": 1, "source-b": 2})

    def test_retrieval_only_records_one_successful_observation_without_polling(self):
        module = self._load_module("run_evals_retrieval_only_single_observation")
        calls = {"count": 0}
        original = module.query_case

        def fake_query(*args, **kwargs):
            calls["count"] += 1
            return 200, {
                "sources": [{"doc_id": "source-a", "score": 0.9}],
                "retrieval": {
                    "stage_diagnostics": {
                        "selected": {
                            "required_document_count": 2,
                            "hit_document_count": 1,
                            "all_required_hit": False,
                            "all_required_max_rank": 0,
                        }
                    }
                },
            }

        module.query_case = fake_query
        try:
            details, _ = module.evaluate_case_assertions(
                "http://api.invalid",
                "token",
                "query",
                "source-a",
                True,
                ["source-a", "source-b"],
                [],
                5,
                5,
                2,
                1,
                10,
                required_doc_ids=["source-a", "source-b"],
                retrieval_only=True,
            )
        finally:
            module.query_case = original

        self.assertEqual(calls["count"], 1)
        self.assertEqual(details["query_attempts"], 1)
        self.assertFalse(details["required_docs_hit"])
        self.assertEqual(
            details["retrieval_stage_diagnostics"]["selected"]["hit_document_count"],
            1,
        )
        self.assertEqual(
            details["retrieval_stage_diagnostics"]["selected"]["all_required_max_rank"],
            0,
        )

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

    def test_report_aggregates_cohort_metrics_and_resolved_configuration(self):
        module = self._load_module("run_evals_cohort_report")
        result = _minimal_result(model_mode="real", embed_model="bge-m3")
        result["dataset"] = {
            "version": "2.0",
            "name": "enterprise-candidate",
            "dataset_type": "enterprise_gold_candidate",
            "evaluation_scope": "answer_and_retrieval",
            "document_count": 4,
            "case_count": 4,
            "sha256": "a" * 64,
            "provenance": {"business_approval_complete": False},
        }
        result["models"]["reranker_model"] = "BAAI/bge-reranker-base"
        result["configuration"] = {
            "retrieval_enable_rerank": True,
            "retrieval_rerank_policy": "auto",
            "retrieval_candidate_k": 50,
            "retrieval_final_top_k": 5,
            "query_top_k": 5,
            "retrieval_grounding_check": True,
        }
        result["cases"] = [
            {
                "case_id": "semantic-1",
                "evaluation_cohort": "semantic",
                "expect_hit": True,
                "recall_at_5": True,
                "required_doc_ids": [],
                "required_docs_hit": True,
                "citation_check_eligible": True,
                "answer_source_citation_ok": True,
                "key_fact_check_eligible": True,
                "key_fact_assertion_pass": True,
                "safety_refusal_eligible": False,
                "safety_refusal_pass": False,
                "grounding_checked": True,
                "grounding_passed": True,
                "query_latency_ms": 100.0,
                "prompt_tokens": 8,
                "completion_tokens": 2,
                "retrieval_assertion_pass": True,
                "answer_assertion_pass": True,
                "assertion_pass": True,
                "strict_rank": 1,
                "max_strict_rank": 5,
                "assertion_reason": "all_assertions_passed",
            },
            {
                "case_id": "cross-1",
                "evaluation_cohort": "cross_document",
                "expect_hit": True,
                "recall_at_5": False,
                "required_doc_ids": ["doc-a", "doc-b"],
                "required_docs_hit": False,
                "citation_check_eligible": True,
                "answer_source_citation_ok": False,
                "key_fact_check_eligible": True,
                "key_fact_assertion_pass": True,
                "safety_refusal_eligible": False,
                "safety_refusal_pass": False,
                "grounding_checked": True,
                "grounding_passed": False,
                "query_latency_ms": 1000.0,
                "prompt_tokens": 20,
                "completion_tokens": 10,
                "retrieval_assertion_pass": False,
                "answer_assertion_pass": False,
                "assertion_pass": False,
                "strict_rank": 0,
                "max_strict_rank": 5,
                "assertion_reason": "retrieval:missing_required_documents",
                "retrieval_stage_diagnostics": {
                    "required_document_count": 2,
                    "backend": {
                        "qdrant": {
                            "required_document_count": 2,
                            "hit_document_count": 2,
                            "all_required_hit": True,
                        }
                    },
                    "fused": {
                        "required_document_count": 2,
                        "hit_document_count": 2,
                        "all_required_hit": True,
                    },
                    "selected": {
                        "required_document_count": 2,
                        "hit_document_count": 1,
                        "all_required_hit": False,
                    },
                },
            },
            {
                "case_id": "lexical-1",
                "evaluation_cohort": "lexical",
                "expect_hit": True,
                "recall_at_5": False,
                "required_doc_ids": [],
                "required_docs_hit": True,
                "citation_check_eligible": True,
                "answer_source_citation_ok": True,
                "key_fact_check_eligible": True,
                "key_fact_assertion_pass": False,
                "safety_refusal_eligible": False,
                "safety_refusal_pass": False,
                "grounding_checked": False,
                "grounding_passed": True,
                "query_latency_ms": 200.0,
                "prompt_tokens": 15,
                "completion_tokens": 5,
                "retrieval_assertion_pass": False,
                "answer_assertion_pass": False,
                "assertion_pass": False,
                "strict_rank": 0,
                "max_strict_rank": 5,
                "assertion_reason": "retrieval:timeout",
            },
            {
                "case_id": "safety-1",
                "evaluation_cohort": "safety_negative",
                "expect_hit": False,
                "recall_at_5": False,
                "required_doc_ids": [],
                "required_docs_hit": True,
                "citation_check_eligible": False,
                "answer_source_citation_ok": True,
                "key_fact_check_eligible": False,
                "key_fact_assertion_pass": True,
                "safety_refusal_eligible": True,
                "safety_refusal_pass": True,
                "grounding_checked": False,
                "grounding_passed": True,
                "query_latency_ms": 400.0,
                "prompt_tokens": 0,
                "completion_tokens": 0,
                "retrieval_assertion_pass": True,
                "answer_assertion_pass": True,
                "assertion_pass": True,
                "strict_rank": 0,
                "max_strict_rank": 0,
                "assertion_reason": "all_assertions_passed",
            },
        ]

        with tempfile.TemporaryDirectory() as tmp:
            json_path, markdown_path = module.write_report(Path(tmp), result)
            payload = json.loads(json_path.read_text(encoding="utf-8"))
            markdown = markdown_path.read_text(encoding="utf-8")

        self.assertEqual(payload["cohorts"]["semantic"]["recall_at_5"], 1.0)
        self.assertEqual(
            payload["cohorts"]["cross_document"]["all_required_docs_hit_rate"],
            0.0,
        )
        self.assertEqual(payload["cohorts"]["cross_document"]["citation_complete_rate"], 0.0)
        self.assertEqual(payload["cohorts"]["cross_document"]["key_fact_pass_rate"], 1.0)
        self.assertEqual(payload["cohorts"]["cross_document"]["grounding_pass_rate"], 0.0)
        self.assertEqual(payload["cohorts"]["cross_document"]["latency_ms"]["p95"], 1000.0)
        diagnostics = payload["cohorts"]["cross_document"]["retrieval_stage_diagnostics"]
        self.assertEqual(diagnostics["fused"]["all_required_hit_rate"], 1.0)
        self.assertEqual(diagnostics["selected"]["all_required_hit_rate"], 0.0)
        self.assertEqual(diagnostics["backend"]["qdrant"]["all_required_hit_rate"], 1.0)
        self.assertEqual(payload["cohorts"]["lexical"]["total_tokens"], 20)
        self.assertEqual(payload["cohorts"]["safety_negative"]["safety_refusal_rate"], 1.0)
        self.assertIn("## Cohort Metrics", markdown)
        self.assertIn("Dataset SHA-256: " + "a" * 64, markdown)
        self.assertIn("Rerank enabled: true", markdown)
        self.assertIn("Rerank policy: auto", markdown)
        self.assertIn("BAAI/bge-reranker-base", markdown)

    def test_report_marks_incomplete_query_or_grounding_as_invalid(self):
        module = self._load_module("run_evals_invalid_run_report")
        result = _minimal_result(model_mode="real", embed_model="bge-m3")
        result["cases"] = [
            {
                **result["cases"][0],
                "query_successful": True,
                "grounding_unavailable": False,
            },
            {
                **result["cases"][0],
                "case_id": "case-002",
                "query_successful": False,
                "grounding_unavailable": True,
            },
        ]
        result["summary"]["total_cases"] = 2

        with tempfile.TemporaryDirectory() as tmp:
            json_path, _ = module.write_report(Path(tmp), result)
            payload = json.loads(json_path.read_text(encoding="utf-8"))
            latest_exists = (Path(tmp) / "latest.json").exists()

        self.assertEqual(payload["summary"]["successful_query_cases"], 1)
        self.assertEqual(payload["summary"]["grounding_unavailable_cases"], 1)
        self.assertFalse(payload["summary"]["run_valid"])
        self.assertFalse(latest_exists)

    def test_real_report_writes_quality_latest_json(self):
        module = self._load_module("run_evals_quality_latest_real")
        result = _minimal_result(model_mode="real", embed_model="bge-m3")
        result["models"]["embed_dimension"] = 1024
        result["models"]["llm_model"] = "deepseek-v4-flash"
        result["summary"]["recall_at_1"] = 0.55
        result["summary"]["recall_at_3"] = 0.68
        result["summary"]["recall_at_5"] = 0.71
        result["dataset"] = {"name": "semantic-golden-set.json", "case_count": 44}

        with tempfile.TemporaryDirectory() as tmp:
            json_path, _ = module.write_report(Path(tmp), result)
            latest = json.loads((Path(tmp) / "latest.json").read_text(encoding="utf-8"))

        self.assertEqual(latest["source_report"], json_path.name)
        self.assertEqual(latest["model_mode"], "real")
        self.assertEqual(latest["embed_model"], "bge-m3")
        self.assertEqual(latest["embed_dimension"], 1024)
        self.assertEqual(
            latest["recall"][0],
            {"k": 1, "value": 55, "note": "目标文档排第 1 的比例"},
        )
        self.assertEqual(latest["recall"][1]["value"], 68)
        self.assertEqual(latest["recall"][2]["value"], 71)
        self.assertEqual(latest["dataset"], "semantic-golden-set.json (44 cases)")
        self.assertEqual(latest["noise_floor"], 4.26)
        self.assertTrue(latest["experiments"])

    def test_mock_report_does_not_overwrite_quality_latest_json(self):
        module = self._load_module("run_evals_quality_latest_mock")
        existing = {"source_report": "keep-me.json", "model_mode": "real", "recall": []}
        with tempfile.TemporaryDirectory() as tmp:
            latest_path = Path(tmp) / "latest.json"
            latest_path.write_text(json.dumps(existing), encoding="utf-8")
            module.write_report(Path(tmp), _minimal_result(model_mode="mock"))
            payload = json.loads(latest_path.read_text(encoding="utf-8"))
            created = list(Path(tmp).glob("eval-*.json"))
        self.assertTrue(created)
        self.assertEqual(payload["source_report"], "keep-me.json")

    def test_real_report_preserves_existing_quality_experiments(self):
        module = self._load_module("run_evals_quality_latest_preserve")
        result = _minimal_result(model_mode="real", embed_model="bge-m3")
        result["summary"]["recall_at_1"] = 0.4
        existing = {
            "noise_floor": 1.5,
            "experiments": [{"title": "custom", "verdict": "keep", "detail": "x", "good": True}],
        }
        with tempfile.TemporaryDirectory() as tmp:
            (Path(tmp) / "latest.json").write_text(json.dumps(existing), encoding="utf-8")
            module.write_report(Path(tmp), result)
            latest = json.loads((Path(tmp) / "latest.json").read_text(encoding="utf-8"))
        self.assertEqual(latest["noise_floor"], 1.5)
        self.assertEqual(latest["experiments"][0]["title"], "custom")
        self.assertEqual(latest["recall"][0]["value"], 40)


class PublicationReadinessTest(unittest.TestCase):
    def test_verify_document_published_reads_auto_publication_state(self):
        module_path = Path(__file__).resolve().parents[1] / "run-evals.py"
        spec = importlib.util.spec_from_file_location("run_evals_publication", module_path)
        if spec is None or spec.loader is None:
            self.fail("failed to load run-evals module")
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)

        with mock.patch.object(
            module,
            "http_json",
            return_value=(200, {"publication_status": "published"}),
        ) as http_json:
            module.verify_document_published(
                "http://api.invalid",
                "admin-token",
                "doc-1",
            )

        http_json.assert_called_once()
        publication_call = http_json.call_args
        self.assertEqual(
            publication_call.args[:2],
            ("GET", "http://api.invalid/v1/documents/doc-1"),
        )
        self.assertEqual(
            publication_call.kwargs["headers"]["Authorization"],
            "Bearer admin-token",
        )

    def test_verify_document_published_rejects_unpublished_document(self):
        module_path = Path(__file__).resolve().parents[1] / "run-evals.py"
        spec = importlib.util.spec_from_file_location("run_evals_unpublished", module_path)
        if spec is None or spec.loader is None:
            self.fail("failed to load run-evals module")
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)

        with mock.patch.object(
            module,
            "http_json",
            return_value=(200, {"publication_status": "draft"}),
        ):
            with self.assertRaisesRegex(module.EvalRunnerError, "not auto-published"):
                module.verify_document_published(
                    "http://api.invalid",
                    "admin-token",
                    "doc-1",
                )


class SkipComposeTests(unittest.TestCase):
    def test_existing_api_base_skips_isolated_compose(self):
        module_path = Path(__file__).resolve().parents[1] / "run-evals.py"
        spec = importlib.util.spec_from_file_location("run_evals_skip_compose", module_path)
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)
        self.assertFalse(module.should_start_compose("http://127.0.0.1:8080"))
        self.assertFalse(module.should_start_compose("  http://127.0.0.1:8080/  "))
        self.assertTrue(module.should_start_compose(""))
        self.assertTrue(module.should_start_compose("   "))

    def test_host_es_count_url(self):
        module_path = Path(__file__).resolve().parents[1] / "run-evals.py"
        spec = importlib.util.spec_from_file_location("run_evals_es_count_url", module_path)
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)
        self.assertEqual(
            module.elasticsearch_count_url("http://127.0.0.1:9200/", "documents_text"),
            "http://127.0.0.1:9200/documents_text/_count",
        )


if __name__ == "__main__":
    unittest.main()
