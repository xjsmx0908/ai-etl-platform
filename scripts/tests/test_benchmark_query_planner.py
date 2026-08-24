import json
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "benchmark-query-planner.py"


class PlannerHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        payload = json.loads(self.rfile.read(length))
        prompt = payload["messages"][-1]["content"]
        if "CROSS_PUBLIC" in prompt:
            content = json.dumps(
                {
                    "activate": True,
                    "facets": [
                        "remote work equipment reimbursement",
                        "remote access security requirements",
                    ],
                }
            )
        elif "DUPLICATE_PUBLIC" in prompt:
            content = json.dumps(
                {"activate": True, "facets": ["duplicate facet", "duplicate facet"]}
            )
        else:
            content = json.dumps({"activate": False, "facets": []})
        body = json.dumps({"choices": [{"message": {"content": content}}]}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        return


class QueryPlannerBenchmarkCLITests(unittest.TestCase):
    def test_runs_public_gate_three_times_and_writes_aggregate_only_report(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            dataset = root / "planner-public.json"
            report = root / "report.json"
            dataset.write_text(
                json.dumps(
                    {
                        "version": "1.0",
                        "cases": [
                            {
                                "id": "cross-public",
                                "cohort": "cross_document",
                                "question": "CROSS_PUBLIC compare remote work support and secure access",
                                "expected_facets": [
                                    ["equipment", "reimbursement"],
                                    ["access", "security"],
                                ],
                            },
                            {
                                "id": "semantic-public",
                                "cohort": "semantic",
                                "question": "SEMANTIC_PUBLIC explain travel reimbursement",
                            },
                            {
                                "id": "lexical-public",
                                "cohort": "lexical",
                                "question": "LEXICAL_PUBLIC find contract CN-2026-0042",
                            },
                        ],
                    }
                ),
                encoding="utf-8",
            )
            server = ThreadingHTTPServer(("127.0.0.1", 0), PlannerHandler)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            self.addCleanup(server.server_close)
            self.addCleanup(server.shutdown)

            proc = subprocess.run(
                [
                    sys.executable,
                    str(SCRIPT),
                    "--dataset",
                    str(dataset),
                    "--endpoint",
                    f"http://127.0.0.1:{server.server_port}/v1/chat/completions",
                    "--model",
                    "fixture-planner",
                    "--repetitions",
                    "3",
                    "--report",
                    str(report),
                ],
                text=True,
                capture_output=True,
                check=False,
            )

            self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
            payload = json.loads(report.read_text(encoding="utf-8"))

        self.assertTrue(payload["gate_passed"])
        self.assertEqual(payload["repetitions"], 3)
        self.assertEqual(payload["metrics"]["valid_response_rate"], 1.0)
        self.assertEqual(payload["metrics"]["cross_document_activation_rate"], 1.0)
        self.assertEqual(payload["metrics"]["expected_facet_coverage_rate"], 1.0)
        self.assertEqual(payload["metrics"]["semantic_false_activation_rate"], 0.0)
        self.assertEqual(payload["metrics"]["lexical_activation_rate"], 0.0)
        serialized = json.dumps(payload)
        self.assertNotIn("CROSS_PUBLIC", serialized)
        self.assertNotIn("cross-public", serialized)

    def test_rejects_non_local_endpoint_before_writing_a_report(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            dataset = root / "planner-public.json"
            report = root / "report.json"
            dataset.write_text(
                json.dumps(
                    {
                        "version": "1.0",
                        "cases": [
                            {
                                "id": "cross-public",
                                "cohort": "cross_document",
                                "question": "public question",
                                "expected_facets": [["policy"], ["security"]],
                            }
                        ],
                    }
                ),
                encoding="utf-8",
            )

            proc = subprocess.run(
                [
                    sys.executable,
                    str(SCRIPT),
                    "--dataset",
                    str(dataset),
                    "--endpoint",
                    "https://models.example.com/v1/chat/completions",
                    "--model",
                    "external-model",
                    "--report",
                    str(report),
                ],
                text=True,
                capture_output=True,
                check=False,
            )

            self.assertEqual(proc.returncode, 2)
            self.assertIn("local", proc.stderr.lower())
            self.assertFalse(report.exists())

    def test_counts_runtime_rejected_plans_as_invalid_responses(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            dataset = root / "planner-public.json"
            report = root / "report.json"
            dataset.write_text(
                json.dumps(
                    {
                        "version": "1.0",
                        "cases": [
                            {
                                "id": "duplicate-public",
                                "cohort": "cross_document",
                                "question": "DUPLICATE_PUBLIC public multi-source question",
                                "expected_facets": [["policy"], ["security"]],
                            }
                        ],
                    }
                ),
                encoding="utf-8",
            )
            server = ThreadingHTTPServer(("127.0.0.1", 0), PlannerHandler)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            self.addCleanup(server.server_close)
            self.addCleanup(server.shutdown)

            proc = subprocess.run(
                [
                    sys.executable,
                    str(SCRIPT),
                    "--dataset",
                    str(dataset),
                    "--endpoint",
                    f"http://127.0.0.1:{server.server_port}/v1/chat/completions",
                    "--model",
                    "fixture-planner",
                    "--repetitions",
                    "1",
                    "--report",
                    str(report),
                ],
                text=True,
                capture_output=True,
                check=False,
            )
            payload = json.loads(report.read_text(encoding="utf-8"))

        self.assertEqual(proc.returncode, 1)
        self.assertFalse(payload["gate_passed"])
        self.assertEqual(payload["metrics"]["valid_response_rate"], 0.0)


if __name__ == "__main__":
    unittest.main()
