import hashlib
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "finalize-enterprise-gold.py"


class FinalizeEnterpriseGoldCLITests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.candidate = self.directory / "candidate.json"
        self.approval = self.directory / "approval.json"
        self.signed_approval = self.directory / "signed-approval.txt"
        self.output = self.directory / "gold.json"
        self.candidate.write_text(
            json.dumps(
                {
                    "version": "2.0",
                    "name": "enterprise-candidate",
                    "dataset_type": "enterprise_private_gold_candidate",
                    "evaluation_scope": "answer_and_retrieval",
                    "provenance": {"business_approval_complete": False},
                    "documents": [
                        {
                            "id": "doc-1",
                            "filename": "policy.txt",
                            "permission": "internal",
                            "content": "Approved synthetic policy.",
                        }
                    ],
                    "cases": [
                        {
                            "id": "case-1",
                            "document_id": "doc-1",
                            "query": "What is the approved policy?",
                            "reference_answer": "Approved synthetic policy.",
                        }
                    ],
                },
                sort_keys=True,
            ),
            encoding="utf-8",
        )

    def candidate_sha256(self):
        return hashlib.sha256(self.candidate.read_bytes()).hexdigest()

    def write_signed_approval(self):
        self.signed_approval.write_text(
            "Synthetic signed approval artifact.", encoding="utf-8"
        )

    def valid_approval(self):
        self.write_signed_approval()
        return {
            "schema_version": "1.0",
            "decision": "approved",
            "approval_id": "approval-2026-001",
            "candidate_sha256": self.candidate_sha256(),
            "signed_approval_sha256": hashlib.sha256(
                self.signed_approval.read_bytes()
            ).hexdigest(),
            "approved_by": "Business Owner",
            "approver_role": "policy_owner",
            "approved_at": "2026-08-24T10:00:00+08:00",
            "approval_scope": "entire_dataset",
            "approved_document_ids": ["doc-1"],
            "approved_case_ids": ["case-1"],
            "attestations": {
                "document_authority_confirmed": True,
                "permissions_confirmed": True,
                "effective_versions_confirmed": True,
                "questions_and_reference_answers_confirmed": True,
            },
        }

    def run_cli(self):
        return subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "--candidate",
                str(self.candidate),
                "--approval",
                str(self.approval),
                "--signed-approval",
                str(self.signed_approval),
                "--output",
                str(self.output),
            ],
            text=True,
            capture_output=True,
            check=False,
        )

    def test_rejects_missing_signed_approval_artifact(self):
        self.approval.write_text(
            json.dumps({"candidate_sha256": self.candidate_sha256()}),
            encoding="utf-8",
        )

        result = self.run_cli()

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("signed approval", result.stderr.lower())
        self.assertFalse(self.output.exists())

    def test_rejects_candidate_digest_mismatch(self):
        approval = self.valid_approval()
        approval["candidate_sha256"] = "0" * 64
        self.approval.write_text(json.dumps(approval), encoding="utf-8")

        result = self.run_cli()

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("candidate sha256", result.stderr.lower())
        self.assertFalse(self.output.exists())

    def test_rejects_signed_approval_digest_mismatch(self):
        approval = self.valid_approval()
        approval["signed_approval_sha256"] = "0" * 64
        self.approval.write_text(json.dumps(approval), encoding="utf-8")

        result = self.run_cli()

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("signed approval sha256", result.stderr.lower())
        self.assertFalse(self.output.exists())

    def test_rejects_incomplete_case_approval_scope(self):
        approval = self.valid_approval()
        approval["approved_case_ids"] = []
        self.approval.write_text(json.dumps(approval), encoding="utf-8")

        result = self.run_cli()

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("case approval scope", result.stderr.lower())
        self.assertFalse(self.output.exists())

    def test_rejects_unapproved_business_attestation(self):
        approval = self.valid_approval()
        approval["attestations"]["permissions_confirmed"] = False
        self.approval.write_text(json.dumps(approval), encoding="utf-8")

        result = self.run_cli()

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("attestations", result.stderr.lower())
        self.assertFalse(self.output.exists())

    def test_promotes_fully_approved_candidate(self):
        approval = self.valid_approval()
        self.approval.write_text(json.dumps(approval, sort_keys=True), encoding="utf-8")

        result = self.run_cli()

        self.assertEqual(result.returncode, 0, result.stderr)
        promoted = json.loads(self.output.read_text(encoding="utf-8"))
        self.assertEqual(self.output.stat().st_mode & 0o777, 0o600)
        self.assertEqual(promoted["dataset_type"], "enterprise_private_gold")
        self.assertTrue(promoted["provenance"]["business_approval_complete"])
        recorded = promoted["provenance"]["business_approval"]
        self.assertEqual(recorded["approval_id"], "approval-2026-001")
        self.assertEqual(recorded["candidate_sha256"], self.candidate_sha256())
        self.assertEqual(
            recorded["approval_record_sha256"],
            hashlib.sha256(self.approval.read_bytes()).hexdigest(),
        )
        original = json.loads(self.candidate.read_text(encoding="utf-8"))
        self.assertFalse(original["provenance"]["business_approval_complete"])


if __name__ == "__main__":
    unittest.main()
