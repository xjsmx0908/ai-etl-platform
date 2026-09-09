import importlib.util
import json
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlparse


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "load-test.py"


def load_module():
    spec = importlib.util.spec_from_file_location("load_test", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


class LoadTestGateTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.mod = load_module()

    def test_percentile_interpolates_between_ordered_samples(self):
        self.assertEqual(self.mod.percentile([], 0.95), 0.0)
        self.assertEqual(self.mod.percentile([10.0], 0.95), 10.0)
        self.assertEqual(self.mod.percentile([10.0, 20.0, 30.0, 40.0], 0.50), 25.0)

    def test_evaluate_gate_passes_when_no_thresholds_are_set(self):
        failures = self.mod.evaluate_gate(
            {"success_rate": 0.0, "p95_ms": 12_000.0, "throughput_qps": 0.1},
            self.mod.GateThresholds(),
        )
        self.assertEqual(failures, [])

    def test_evaluate_gate_fails_on_latency_and_success_thresholds(self):
        failures = self.mod.evaluate_gate(
            {
                "success_rate": 0.75,
                "hit_rate": 1.0,
                "top_hit_rate": 1.0,
                "throughput_qps": 2.0,
                "p50_ms": 10.0,
                "p95_ms": 900.0,
                "p99_ms": 1200.0,
            },
            self.mod.GateThresholds(min_success_rate=1.0, max_p95_ms=800.0, min_qps=3.0),
        )
        self.assertEqual(len(failures), 3)
        self.assertTrue(any("success_rate" in item for item in failures))
        self.assertTrue(any("p95_ms" in item for item in failures))
        self.assertTrue(any("throughput_qps" in item for item in failures))

    def test_cold_retrieval_profile_sets_retrieval_only_and_budgets(self):
        args = self.mod.parse_args(["--profile", "cold-retrieval"])
        self.assertTrue(args.retrieval_only)
        self.assertFalse(args.unique_questions)
        self.assertEqual(args.warmup, 2)
        self.assertEqual(args.scenario, "cold-retrieval")
        self.assertEqual(args.max_p95_ms, 800.0)
        self.assertEqual(args.min_success_rate, 1.0)

    def test_explicit_flags_override_profile_thresholds(self):
        args = self.mod.parse_args(
            ["--profile", "cached-e2e", "--max-p95-ms", "250", "--retrieval-only"]
        )
        self.assertTrue(args.retrieval_only)
        self.assertEqual(args.max_p95_ms, 250.0)
        self.assertEqual(args.max_p99_ms, 800.0)
        self.assertFalse(args.unique_questions)

    def test_query_payload_and_unique_questions(self):
        payload = self.mod.build_query_payload("q", 5, retrieval_only=True, knowledge_space_id="space-1")
        self.assertEqual(
            payload,
            {"question": "q", "top_k": 5, "retrieval_only": True, "knowledge_space_id": "space-1"},
        )
        self.assertEqual(self.mod.question_for_index("base", 3, False), "base")
        self.assertEqual(self.mod.question_for_index("base", 3, True), "base [loadtest-3]")

    def test_summarize_and_report_include_gate_and_cache_hit(self):
        results = [
            self.mod.Result(True, True, True, 200, 100.0, "doc-1", True),
            self.mod.Result(True, True, True, 200, 200.0, "doc-1", False),
        ]
        summary = self.mod.summarize_results(results, 400.0, "doc-1", "q", ingestion_ready_ms=1500.0)
        self.assertEqual(summary["cache_hit_rate"], 0.5)
        self.assertEqual(summary["ingestion_ready_ms"], 1500.0)
        failures = self.mod.evaluate_gate(summary, self.mod.GateThresholds(max_p95_ms=50.0))
        with tempfile.TemporaryDirectory() as tmp:
            json_path, md_path = self.mod.write_report(
                Path(tmp),
                {
                    "scenario": "cold-retrieval",
                    "tenant_id": "t1",
                    "config": {"concurrency": 2},
                    "summary": summary,
                    "gate": {
                        "profile": "cold-retrieval",
                        "thresholds": {"max_p95_ms": 50.0},
                        "passed": False,
                        "failures": failures,
                    },
                },
            )
            payload = json.loads(json_path.read_text(encoding="utf-8"))
            markdown = md_path.read_text(encoding="utf-8")
        self.assertFalse(payload["gate"]["passed"])
        self.assertIn("Passed: no", markdown)
        self.assertIn("p95_ms", markdown)

    def test_cli_passes_and_fails_against_local_api(self):
        server = _FakeQueryAPI()
        try:
            report_dir = tempfile.TemporaryDirectory()
            common = [
                sys.executable,
                str(SCRIPT),
                "--api-base",
                server.base,
                "--keep-mock-server",
                "--username",
                "admin",
                "--password",
                "admin",
                "--retrieval-only",
                "--requests",
                "3",
                "--concurrency",
                "1",
                "--warmup",
                "0",
                "--report-dir",
                report_dir.name,
            ]
            passed = subprocess.run(
                [*common, "--min-success-rate", "1.0", "--max-p95-ms", "60000"],
                cwd=str(ROOT),
                text=True,
                capture_output=True,
            )
            self.assertEqual(passed.returncode, 0, passed.stderr)
            failed = subprocess.run(
                [*common, "--min-qps", "999999"],
                cwd=str(ROOT),
                text=True,
                capture_output=True,
            )
            self.assertEqual(failed.returncode, 1, failed.stderr)
            self.assertIn("GATE FAIL", failed.stderr)
            self.assertTrue(any(body.get("retrieval_only") for body in server.query_bodies))
        finally:
            server.stop()
            report_dir.cleanup()

    def test_isolated_gate_script_keeps_mock_stack_and_both_profiles(self):
        script = (ROOT / "scripts" / "load-test-gate.sh").read_text(encoding="utf-8")
        workflow = (ROOT / ".github" / "workflows" / "load-test.yml").read_text(encoding="utf-8")
        required = (ROOT / ".github" / "workflows" / "ci.yml").read_text(encoding="utf-8")
        self.assertIn("ai-etl-loadtest", script)
        self.assertIn("docker-compose.eval.yml", script)
        self.assertIn("run_profile cold-retrieval", script)
        self.assertIn("run_profile cached-e2e", script)
        self.assertIn("RETRIEVAL_DIAGNOSTICS_ENABLED=true", script)
        self.assertIn("SEMANTIC_CACHE_ENABLED=false", script)
        self.assertIn("force-recreate", script)
        self.assertIn('mkdir -p "${REPORT_DIR}"', script)
        self.assertLess(script.index('mkdir -p "${REPORT_DIR}"'), script.index("docker compose up"))
        self.assertIn("workflow_dispatch", workflow)
        self.assertIn("scripts/load-test-gate.sh", workflow)
        self.assertNotIn("loadtest", required.lower().split("required checks")[-1] if False else required)
        self.assertNotIn("load-test-gate.sh", required)


class _FakeQueryAPI:
    def __init__(self) -> None:
        this = self
        self.doc_id = "doc-loadtest"
        self.query_bodies = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                path = urlparse(self.path).path
                if path == "/healthz":
                    this._write(self, 200, {"status": "ok"})
                    return
                if path.startswith("/v1/tasks/"):
                    this._write(self, 200, {"status": "completed", "doc_id": this.doc_id})
                    return
                this._write(self, 404, {"error": "not found"})

            def do_POST(self):
                length = int(self.headers.get("Content-Length", "0"))
                raw = self.rfile.read(length) if length else b""
                path = urlparse(self.path).path
                if path == "/v1/auth/login":
                    this._write(self, 200, {"token": "fake-token"})
                    return
                if path == "/v1/upload":
                    this._write(self, 202, {"doc_id": this.doc_id})
                    return
                if path == "/v1/query":
                    body = json.loads(raw.decode("utf-8") or "{}")
                    this.query_bodies.append(body)
                    this._write(
                        self,
                        200,
                        {
                            "answer": "",
                            "sources": [{"doc_id": this.doc_id, "content": "seed"}],
                            "retrieved_sources": [{"doc_id": this.doc_id, "content": "seed"}],
                            "retrieval": {"cache_hit": False},
                        },
                    )
                    return
                this._write(self, 404, {"error": "not found"})

            def log_message(self, format, *args):
                return

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        host, port = self.server.server_address[:2]
        self.base = f"http://{host}:{port}"

    @staticmethod
    def _write(handler: BaseHTTPRequestHandler, status: int, payload: dict) -> None:
        body = json.dumps(payload).encode("utf-8")
        handler.send_response(status)
        handler.send_header("Content-Type", "application/json")
        handler.send_header("Content-Length", str(len(body)))
        handler.end_headers()
        handler.wfile.write(body)

    def stop(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)


if __name__ == "__main__":
    unittest.main()
