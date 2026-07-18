import json
import importlib.util
import sys
import threading
import unittest
from pathlib import Path


sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from judge_eval import JudgeClient, JudgeConfig, JudgeError, JudgeInput


class JudgeClientTest(unittest.TestCase):
    def test_mock_server_accepts_project_no_answer_response(self):
        module_path = Path(__file__).resolve().parents[1] / "mock-openai-server.py"
        spec = importlib.util.spec_from_file_location("mock_openai_server_no_answer", module_path)
        if spec is None or spec.loader is None:
            self.fail("failed to load mock server module")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        server = module.MockServer(("127.0.0.1", 0), module.Handler)
        server.embed_dim = 8
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)

        client = JudgeClient(
            JudgeConfig(
                endpoint=f"http://127.0.0.1:{server.server_port}/v1",
                api_key="mock-key",
                model="mock-judge",
            )
        )
        result = client.judge(
            JudgeInput(
                case_id="case-no-answer",
                question="Can a normal user retrieve alpha123?",
                reference_answer="No. alpha123 is restricted to privileged review.",
                system_answer="\u672a\u5728\u53c2\u8003\u6587\u6863\u4e2d\u76f4\u63a5\u5b9a\u4f4d\u951a\u70b9 alpha123\u3002",
                retrieved_contexts=[],
                expect_hit=False,
            )
        )

        self.assertTrue(result.overall_pass)
        self.assertEqual(result.correctness_score, 5)

    def test_mock_server_supports_judge_protocol(self):
        module_path = Path(__file__).resolve().parents[1] / "mock-openai-server.py"
        spec = importlib.util.spec_from_file_location("mock_openai_server", module_path)
        if spec is None or spec.loader is None:
            self.fail("failed to load mock server module")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        server = module.MockServer(("127.0.0.1", 0), module.Handler)
        server.embed_dim = 8
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)

        client = JudgeClient(
            JudgeConfig(
                endpoint=f"http://127.0.0.1:{server.server_port}/v1",
                api_key="mock-key",
                model="mock-judge",
            )
        )
        result = client.judge(
            JudgeInput(
                case_id="case-001",
                question="What is the retry policy?",
                reference_answer="Retry with bounded backoff.",
                system_answer="Retry with bounded backoff.",
                retrieved_contexts=["Retry with bounded backoff."],
                expect_hit=True,
            )
        )

        self.assertTrue(result.overall_pass)
        self.assertEqual(result.faithfulness_score, 5)

    def test_uses_strict_schema_and_parses_scores(self):
        captured = {}

        def transport(url, headers, body, timeout):
            captured.update(
                {
                    "url": url,
                    "headers": headers,
                    "body": json.loads(body.decode("utf-8")),
                    "timeout": timeout,
                }
            )
            decision = {
                "faithfulness_score": 5,
                "correctness_score": 4,
                "relevance_score": 5,
                "overall_pass": True,
                "reason": "The answer is supported and addresses the question.",
                "unsupported_claims": [],
            }
            return 200, {
                "choices": [
                    {
                        "message": {
                            "content": json.dumps(decision),
                        }
                    }
                ]
            }

        client = JudgeClient(
            JudgeConfig(
                endpoint="https://api.openai.com/v1",
                api_key="judge-key",
                model="judge-model",
            ),
            transport=transport,
        )
        result = client.judge(
            JudgeInput(
                case_id="case-001",
                question="What is the retry policy?",
                reference_answer="Retry failed tasks with bounded backoff.",
                system_answer="Failed tasks are retried with bounded backoff.",
                retrieved_contexts=["The retry policy uses bounded backoff."],
                expect_hit=True,
            )
        )

        self.assertEqual(result.faithfulness_score, 5)
        self.assertEqual(result.correctness_score, 4)
        self.assertTrue(result.overall_pass)
        self.assertEqual(captured["url"], "https://api.openai.com/v1/chat/completions")
        self.assertEqual(captured["headers"]["Authorization"], "Bearer judge-key")
        response_format = captured["body"]["response_format"]
        self.assertEqual(response_format["type"], "json_schema")
        self.assertTrue(response_format["json_schema"]["strict"])

    def test_retries_transient_status(self):
        calls = []

        def transport(url, headers, body, timeout):
            calls.append(url)
            if len(calls) == 1:
                return 500, {"error": "temporary"}
            decision = {
                "faithfulness_score": 4,
                "correctness_score": 4,
                "relevance_score": 4,
                "overall_pass": True,
                "reason": "Supported.",
                "unsupported_claims": [],
            }
            return 200, {"choices": [{"message": {"content": json.dumps(decision)}}]}

        client = JudgeClient(
            JudgeConfig(
                endpoint="https://api.openai.com/v1/chat/completions",
                api_key="judge-key",
                model="judge-model",
                max_retries=1,
                retry_base_seconds=0,
            ),
            transport=transport,
            sleep_fn=lambda _: None,
        )
        result = client.judge(
            JudgeInput(
                case_id="case-001",
                question="question",
                reference_answer="reference",
                system_answer="answer",
                retrieved_contexts=["context"],
                expect_hit=True,
            )
        )

        self.assertEqual(result.correctness_score, 4)
        self.assertEqual(len(calls), 2)

    def test_rejects_out_of_range_score(self):
        decision = {
            "faithfulness_score": 6,
            "correctness_score": 4,
            "relevance_score": 4,
            "overall_pass": True,
            "reason": "Invalid score.",
            "unsupported_claims": [],
        }

        client = JudgeClient(
            JudgeConfig(
                endpoint="https://api.openai.com/v1/chat/completions",
                api_key="judge-key",
                model="judge-model",
            ),
            transport=lambda *_: (200, {"choices": [{"message": {"content": json.dumps(decision)}}]}),
        )

        with self.assertRaisesRegex(JudgeError, "faithfulness_score"):
            client.judge(
                JudgeInput(
                    case_id="case-001",
                    question="question",
                    reference_answer="reference",
                    system_answer="answer",
                    retrieved_contexts=["context"],
                    expect_hit=True,
                )
            )


if __name__ == "__main__":
    unittest.main()
