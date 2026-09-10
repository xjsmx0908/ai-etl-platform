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
SCRIPT = ROOT / "scripts" / "ingestion-capacity.py"


def load_module():
    spec = importlib.util.spec_from_file_location("ingestion_capacity", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


class IngestionCapacityTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.mod = load_module()

    def test_percentile_and_stage_durations(self):
        self.assertEqual(self.mod.percentile([], 0.95), 0.0)
        self.assertEqual(self.mod.percentile([10.0], 0.95), 10.0)
        self.assertEqual(self.mod.percentile([10.0, 20.0, 30.0, 40.0], 0.50), 25.0)
        samples = [
            self.mod.Sample(0.0, "processing", "parsing", 0, 0),
            self.mod.Sample(100.0, "processing", "embedding", 1, 2),
            self.mod.Sample(400.0, "completed", "completed", 2, 2),
        ]
        self.assertEqual(
            self.mod.stage_durations(samples, 400.0),
            {"parsing": 100.0, "embedding": 300.0},
        )

    def test_evaluate_gate_optional_thresholds(self):
        summary = {
            "success_rate": 1.0,
            "p95_accepted_to_ready_ms": 12_000.0,
            "chunks_per_min": 4.0,
        }
        self.assertEqual(self.mod.evaluate_gate(summary, self.mod.GateThresholds()), [])
        failures = self.mod.evaluate_gate(
            summary,
            self.mod.GateThresholds(max_accepted_to_ready_ms=1000.0, min_chunks_per_min=10.0),
        )
        self.assertEqual(len(failures), 2)

    def test_summarize_and_report_are_not_query_gate(self):
        result = self.mod.DocResult(
            fixture="short-text",
            filename="a.txt",
            doc_id="doc-1",
            ok=True,
            accepted_to_ready_ms=1500.0,
            chunks_done=2,
            total_chunks=2,
            stage_ms={"parsing": 200.0, "embedding": 1200.0},
        )
        summary = self.mod.summarize([result])
        self.assertEqual(summary["chunk_count"], 2)
        self.assertGreater(summary["chunks_per_min"], 0.0)
        with tempfile.TemporaryDirectory() as tmp:
            json_path, md_path = self.mod.write_report(
                Path(tmp),
                {
                    "kind": "ingestion-capacity",
                    "not_slo": True,
                    "scenario": "ingestion-capacity",
                    "profile": "short-text",
                    "tenant_id": "t1",
                    "embed_model_hint": "bge-m3",
                    "summary": summary,
                    "gate": {"passed": True, "failures": []},
                },
            )
            payload = json.loads(json_path.read_text(encoding="utf-8"))
            markdown = md_path.read_text(encoding="utf-8")
        self.assertTrue(payload["not_slo"])
        self.assertIn("not the L1 query gate", markdown)
        self.assertIn("Passed: yes", markdown)

    def test_script_does_not_query_or_start_compose(self):
        source = SCRIPT.read_text(encoding="utf-8")
        self.assertNotIn('"/v1/query"', source)
        self.assertIn("never calls /v1/query", source)
        self.assertNotIn("docker compose", source)
        self.assertNotIn("COMPOSE_PROJECT_NAME", source)

    def test_cli_passes_and_fails_against_local_api(self):
        server = _FakeIngestionAPI()
        try:
            report_dir = tempfile.TemporaryDirectory()
            common = [
                sys.executable,
                str(SCRIPT),
                "--api-base",
                server.base,
                "--username",
                "admin",
                "--password",
                "admin",
                "--profile",
                "short-text",
                "--repeat",
                "2",
                "--timeout-sec",
                "5",
                "--poll-interval-sec",
                "0.01",
                "--report-dir",
                report_dir.name,
                "--skip-health",
            ]
            passed = subprocess.run(common, cwd=str(ROOT), text=True, capture_output=True)
            self.assertEqual(passed.returncode, 0, passed.stderr)
            self.assertEqual(server.query_count, 0)
            self.assertEqual(server.upload_count, 2)
            failed = subprocess.run(
                [*common, "--min-chunks-per-min", "999999"],
                cwd=str(ROOT),
                text=True,
                capture_output=True,
            )
            self.assertEqual(failed.returncode, 1, failed.stderr)
            self.assertIn("GATE FAIL", failed.stderr)
        finally:
            server.stop()
            report_dir.cleanup()

    def test_docs_keep_tracks_separate(self):
        ingest_doc = (ROOT / "docs" / "ingestion-capacity.md").read_text(encoding="utf-8")
        load_doc = (ROOT / "docs" / "load-test.md").read_text(encoding="utf-8")
        readme = (ROOT / "README.md").read_text(encoding="utf-8")
        self.assertIn("not the L1 query gate", ingest_doc)
        self.assertIn("run-evals.py --real-models", ingest_doc)
        self.assertIn("ingestion-capacity.md", load_doc)
        self.assertIn("docs/ingestion-capacity.md", readme)


class _FakeIngestionAPI:
    def __init__(self) -> None:
        this = self
        self.upload_count = 0
        self.query_count = 0
        self._polls = {}
        self._doc_seq = 0

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, format, *args):
                return

            def do_GET(self):
                path = urlparse(self.path).path
                if path == "/healthz":
                    this._write(self, 200, {"status": "ok"})
                    return
                if path.startswith("/v1/tasks/"):
                    doc_id = path.rsplit("/", 1)[-1]
                    count = this._polls.get(doc_id, 0)
                    this._polls[doc_id] = count + 1
                    if count == 0:
                        this._write(
                            self,
                            200,
                            {
                                "status": "processing",
                                "stage": "parsing",
                                "doc_id": doc_id,
                                "chunks_done": 0,
                                "total_chunks": 2,
                            },
                        )
                        return
                    if count == 1:
                        this._write(
                            self,
                            200,
                            {
                                "status": "processing",
                                "stage": "embedding",
                                "doc_id": doc_id,
                                "chunks_done": 1,
                                "total_chunks": 2,
                            },
                        )
                        return
                    this._write(
                        self,
                        200,
                        {
                            "status": "completed",
                            "stage": "completed",
                            "doc_id": doc_id,
                            "chunks_done": 2,
                            "total_chunks": 2,
                        },
                    )
                    return
                this._write(self, 404, {"error": "not found"})

            def do_POST(self):
                length = int(self.headers.get("Content-Length", "0"))
                if length:
                    self.rfile.read(length)
                path = urlparse(self.path).path
                if path == "/v1/auth/login":
                    this._write(self, 200, {"token": "fake-token"})
                    return
                if path == "/v1/upload":
                    this._doc_seq += 1
                    this.upload_count += 1
                    this._write(self, 202, {"doc_id": f"doc-{this._doc_seq}"})
                    return
                if path == "/v1/query":
                    this.query_count += 1
                    this._write(self, 200, {"answer": "should not be called"})
                    return
                this._write(self, 404, {"error": "not found"})

        self._httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.base = f"http://127.0.0.1:{self._httpd.server_address[1]}"
        self._thread = threading.Thread(target=self._httpd.serve_forever, daemon=True)
        self._thread.start()

    def stop(self) -> None:
        self._httpd.shutdown()
        self._httpd.server_close()
        self._thread.join(timeout=2)

    @staticmethod
    def _write(handler: BaseHTTPRequestHandler, status: int, payload: dict) -> None:
        body = json.dumps(payload).encode("utf-8")
        handler.send_response(status)
        handler.send_header("Content-Type", "application/json")
        handler.send_header("Content-Length", str(len(body)))
        handler.end_headers()
        handler.wfile.write(body)


if __name__ == "__main__":
    unittest.main()
