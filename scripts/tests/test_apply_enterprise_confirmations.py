import importlib.util
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "apply-enterprise-confirmations.py"


def load_module():
    spec = importlib.util.spec_from_file_location("apply_enterprise_confirmations", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


class ApplyEnterpriseConfirmationsTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.module = load_module()

    def test_extracts_latest_explicit_date(self):
        value = "2019年1月执行，修订日期为2024-09-11。"

        result = self.module.extract_text_dates(value)

        self.assertEqual(result[-1].isoformat(), "2024-09-11")

    def test_version_resolution_uses_later_embedded_modification(self):
        candidates = {
            "doc-a": self.module.DateEvidence("doc-a", "2021-08-02", "office_modified"),
            "doc-b": self.module.DateEvidence("doc-b", "2021-07-30", "office_modified"),
        }

        winner = self.module.choose_latest_document(candidates)

        self.assertEqual(winner.document_id, "doc-a")

    def test_tied_dates_remain_unresolved(self):
        candidates = {
            "doc-a": self.module.DateEvidence("doc-a", "2021-08-02", "content"),
            "doc-b": self.module.DateEvidence("doc-b", "2021-08-02", "content"),
        }

        with self.assertRaises(self.module.ConfirmationError):
            self.module.choose_latest_document(candidates)

    def test_build_candidate_promotes_confirmed_winner_and_excludes_loser(self):
        source = {
            "provenance": {},
            "documents": [
                {"id": "doc-a", "permission": "internal", "metadata": {}},
                {"id": "doc-b", "permission": "internal", "metadata": {}},
            ],
            "cases": [
                {"id": "case-a", "document_id": "doc-a", "metadata": {}},
                {"id": "case-b", "document_id": "doc-b", "metadata": {}},
            ],
        }
        document_reviews = {
            "doc-a": {"proposed_permission": "confidential", "review_decision": "pending_version_confirmation"},
            "doc-b": {"proposed_permission": "confidential", "review_decision": "pending_version_confirmation"},
        }
        case_reviews = {
            "case-a": {"technical_decision": "approved", "retrieval_style": "lexical"},
            "case-b": {"technical_decision": "approved", "retrieval_style": "lexical"},
        }
        confirmations = [
            {
                "confirmation_type": "effective_version",
                "document_id": "doc-a",
                "related_document_id": "doc-b",
                "confirmed_owner": "human_resources",
                "confirmed_effective_status": "historical",
                "confirmed_effective_document": "doc-a",
                "review_decision": "approved",
                "selected_date": "2021-08-02",
                "date_source": "office_modified",
            }
        ]

        candidate = self.module.build_gold_candidate(
            source, document_reviews, case_reviews, confirmations
        )

        self.assertEqual([item["id"] for item in candidate["documents"]], ["doc-a"])
        self.assertEqual([item["id"] for item in candidate["cases"]], ["case-a"])
        self.assertEqual(candidate["documents"][0]["permission"], "confidential")
        self.assertFalse(candidate["provenance"]["business_approval_complete"])
        self.assertTrue(candidate["provenance"]["user_date_rule_confirmation_complete"])


if __name__ == "__main__":
    unittest.main()
