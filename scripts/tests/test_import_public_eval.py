import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "import-public-eval.py"


class PublicEvalImportCLITests(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp_dir.cleanup)
        self.source = Path(self.temp_dir.name) / "beir"
        self.source.mkdir()
        self.output = Path(self.temp_dir.name) / "output.json"

        (self.source / "corpus.jsonl").write_text(
            "\n".join(
                (
                    json.dumps({"_id": "d1", "title": "Policy", "text": "First evidence."}),
                    json.dumps({"_id": "d2", "title": "Guide", "text": "Second evidence."}),
                    json.dumps({"_id": "d3", "title": "Noise", "text": "Distractor evidence."}),
                )
            )
            + "\n",
            encoding="utf-8",
        )
        (self.source / "queries.jsonl").write_text(
            "\n".join(
                (
                    json.dumps({"_id": "q1", "text": "Which evidence is first?"}),
                    json.dumps({"_id": "q2", "text": "Which guide is relevant?"}),
                )
            )
            + "\n",
            encoding="utf-8",
        )
        (self.source / "qrels.tsv").write_text(
            "query-id\tcorpus-id\tscore\nq1\td1\t2\nq1\td2\t1\nq2\td2\t1\n",
            encoding="utf-8",
        )

    def run_import(self, *extra: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--beir-dir",
                str(self.source),
                "--output",
                str(self.output),
                "--name",
                "fixture-benchmark",
                "--dataset-version",
                "fixture-v1",
                "--source-url",
                "https://example.invalid/fixture",
                "--license",
                "Apache-2.0",
                "--split",
                "test",
                *extra,
            ],
            text=True,
            capture_output=True,
            check=False,
        )

    def test_converts_beir_without_duplicating_shared_documents(self):
        result = self.run_import()

        self.assertEqual(result.returncode, 0, result.stderr)
        dataset = json.loads(self.output.read_text(encoding="utf-8"))
        self.assertEqual(dataset["version"], "2.0")
        self.assertEqual(dataset["evaluation_scope"], "retrieval")
        self.assertEqual(dataset["provenance"]["license"], "Apache-2.0")
        self.assertEqual(dataset["provenance"]["dataset_version"], "fixture-v1")
        self.assertEqual([doc["id"] for doc in dataset["documents"]], ["d1", "d2", "d3"])
        self.assertEqual(len(dataset["documents"]), 3)
        self.assertEqual(dataset["cases"][0]["document_id"], "d1")
        self.assertEqual(dataset["cases"][0]["acceptable_doc_ids"], ["d1", "d2"])
        self.assertFalse(dataset["cases"][0]["require_source_citation"])

    def test_sampling_is_deterministic_and_disclosed_as_non_comparable(self):
        first = self.run_import("--max-queries", "1", "--max-documents", "2", "--seed", "17")
        self.assertEqual(first.returncode, 0, first.stderr)
        first_payload = self.output.read_text(encoding="utf-8")

        second = self.run_import("--max-queries", "1", "--max-documents", "2", "--seed", "17")
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(self.output.read_text(encoding="utf-8"), first_payload)
        dataset = json.loads(first_payload)
        self.assertFalse(dataset["provenance"]["benchmark_comparable"])
        self.assertEqual(dataset["provenance"]["sampling"], {"max_documents": 2, "max_queries": 1, "seed": 17})

    def test_rejects_missing_license_provenance(self):
        command = [
            sys.executable,
            str(SCRIPT),
            "--beir-dir",
            str(self.source),
            "--output",
            str(self.output),
            "--name",
            "fixture-benchmark",
            "--dataset-version",
            "fixture-v1",
            "--source-url",
            "https://example.invalid/fixture",
            "--split",
            "test",
        ]
        result = subprocess.run(command, text=True, capture_output=True, check=False)

        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.output.exists())
        self.assertIn("--license", result.stderr)


if __name__ == "__main__":
    unittest.main()
