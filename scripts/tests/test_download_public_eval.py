import json
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "download-public-eval.py"


class DatasetServerHandler(BaseHTTPRequestHandler):
    requests: list[dict[str, list[str]]] = []
    rows = {
        "corpus": [
            {"_id": "d1", "text": "Evidence one."},
            {"_id": "d2", "text": "Evidence two."},
            {"_id": "d3", "text": "Distractor."},
        ],
        "queries": [
            {"_id": "q1", "text": "Which evidence applies?"},
            {"_id": "q2", "text": "Find the second evidence."},
        ],
        "qrels": [
            {"query-id": "q1", "corpus-id": "d1"},
            {"query-id": "q1", "corpus-id": "d2"},
            {"query-id": "q2", "corpus-id": "d2"},
        ],
    }

    def do_GET(self):
        parsed = urlparse(self.path)
        query = parse_qs(parsed.query)
        type(self).requests.append(query)
        if parsed.path != "/rows" or query.get("revision") != ["immutable-fixture"]:
            self.send_error(400)
            return
        config = query["config"][0]
        offset = int(query["offset"][0])
        length = int(query["length"][0])
        all_rows = self.rows[config]
        payload = {
            "rows": [
                {"row_idx": index, "row": row, "truncated_cells": []}
                for index, row in enumerate(all_rows[offset : offset + length], start=offset)
            ],
            "num_rows_total": len(all_rows),
            "partial": False,
        }
        body = json.dumps(payload).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        return


class PublicEvalDownloadCLITests(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp_dir.cleanup)
        temp_path = Path(self.temp_dir.name)
        self.output_dir = temp_path / "download"
        self.catalog = temp_path / "catalog.json"
        self.catalog.write_text(
            json.dumps(
                {
                    "version": "1.0",
                    "datasets": {
                        "fixture": {
                            "name": "Fixture",
                            "repository": "example/fixture",
                            "revision": "immutable-fixture",
                            "license": "Apache-2.0",
                            "source_url": "https://example.invalid/fixture",
                            "configs": {
                                "corpus": {
                                    "config": "corpus",
                                    "split": "train",
                                    "expected_rows": 3,
                                    "sha256": "a54cfdae3ba87ca2844694dd9bf3ef29439783e3e0acd742889417fec26b5598",
                                },
                                "queries": {
                                    "config": "queries",
                                    "split": "train",
                                    "expected_rows": 2,
                                    "sha256": "7cacc0b7db878f77460246a61ea9b6961ef1cb389f8aea4e8b665fca4249f116",
                                },
                                "qrels": {
                                    "config": "qrels",
                                    "split": "train",
                                    "expected_rows": 3,
                                    "sha256": "170cdc3b9fb411c776fde86ac2cbae2922a86ce352cb4b22fd9d180028ace6bc",
                                },
                            },
                        }
                    },
                }
            ),
            encoding="utf-8",
        )
        DatasetServerHandler.requests = []
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), DatasetServerHandler)
        thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)

    def test_downloads_paged_snapshot_as_beir_files_with_manifest(self):
        result = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--dataset",
                "fixture",
                "--catalog",
                str(self.catalog),
                "--output-dir",
                str(self.output_dir),
                "--api-base-url",
                f"http://127.0.0.1:{self.server.server_port}",
                "--page-size",
                "2",
            ],
            text=True,
            capture_output=True,
            check=False,
        )

        self.assertEqual(result.returncode, 0, result.stderr)
        corpus = [json.loads(line) for line in (self.output_dir / "corpus.jsonl").read_text().splitlines()]
        queries = [json.loads(line) for line in (self.output_dir / "queries.jsonl").read_text().splitlines()]
        qrels = (self.output_dir / "qrels.tsv").read_text().splitlines()
        manifest = json.loads((self.output_dir / "download-manifest.json").read_text())
        self.assertEqual([row["_id"] for row in corpus], ["d1", "d2", "d3"])
        self.assertEqual([row["_id"] for row in queries], ["q1", "q2"])
        self.assertEqual(qrels, ["query-id\tcorpus-id\tscore", "q1\td1\t1", "q1\td2\t1", "q2\td2\t1"])
        self.assertEqual(manifest["revision"], "immutable-fixture")
        self.assertEqual(manifest["license"], "Apache-2.0")
        self.assertTrue(all(request["revision"] == ["immutable-fixture"] for request in DatasetServerHandler.requests))
        self.assertGreaterEqual(len(DatasetServerHandler.requests), 5)

    def test_rejects_content_that_does_not_match_approved_checksum(self):
        catalog = json.loads(self.catalog.read_text(encoding="utf-8"))
        catalog["datasets"]["fixture"]["configs"]["corpus"]["sha256"] = "0" * 64
        self.catalog.write_text(json.dumps(catalog), encoding="utf-8")

        result = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--dataset",
                "fixture",
                "--catalog",
                str(self.catalog),
                "--output-dir",
                str(self.output_dir),
                "--api-base-url",
                f"http://127.0.0.1:{self.server.server_port}",
                "--page-size",
                "2",
            ],
            text=True,
            capture_output=True,
            check=False,
        )

        self.assertEqual(result.returncode, 2)
        self.assertIn("checksum", result.stderr)
        self.assertFalse(self.output_dir.exists())


if __name__ == "__main__":
    unittest.main()
