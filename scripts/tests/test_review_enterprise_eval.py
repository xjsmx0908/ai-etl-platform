import importlib.util
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "review-enterprise-eval.py"


def load_module():
    spec = importlib.util.spec_from_file_location("review_enterprise_eval", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


class EnterpriseReviewTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.module = load_module()

    def document(self, **overrides):
        value = {
            "id": "enterprise-a",
            "filename": "record.pdf",
            "source_path": "rag_datas/record.pdf",
            "permission": "internal",
            "content": "完整的历史记录内容。",
            "metadata": {"source_sha256": "a" * 64},
        }
        value.update(overrides)
        return value

    def row(self, **overrides):
        value = {
            "document_id": "enterprise-a",
            "permission": "internal",
            "sensitivity_flags": "",
            "pii_flags": "",
            "quality_flags": "",
        }
        value.update(overrides)
        return value

    def semantic(self, **overrides):
        value = {
            "business_domain": "finance",
            "document_kind": "record",
            "effectiveness_signal": "not_applicable",
            "case_reviews": [],
        }
        value.update(overrides)
        return value

    def test_permission_is_conservatively_escalated_for_pii(self):
        decision = self.module.review_document(
            self.document(),
            self.row(pii_flags="china_mobile"),
            self.semantic(),
            conflict_ids=set(),
            integrity_ok=True,
        )

        self.assertEqual(decision.final_permission, "confidential")
        self.assertEqual(decision.permission_basis, "pii_signal")
        self.assertTrue(decision.gold_eligible)

    def test_low_quality_and_conflict_documents_are_excluded(self):
        low_quality = self.module.review_document(
            self.document(),
            self.row(quality_flags="low_content"),
            self.semantic(),
            conflict_ids=set(),
            integrity_ok=True,
        )
        conflict = self.module.review_document(
            self.document(),
            self.row(),
            self.semantic(),
            conflict_ids={"enterprise-a"},
            integrity_ok=True,
        )

        self.assertEqual(low_quality.review_decision, "excluded_low_quality")
        self.assertEqual(conflict.review_decision, "pending_version_confirmation")
        self.assertFalse(low_quality.gold_eligible)
        self.assertFalse(conflict.gold_eligible)

    def test_policy_requires_business_effectiveness_confirmation(self):
        decision = self.module.review_document(
            self.document(),
            self.row(),
            self.semantic(document_kind="policy", effectiveness_signal="explicit_current"),
            conflict_ids=set(),
            integrity_ok=True,
        )

        self.assertEqual(decision.effective_status, "pending_business_confirmation")
        self.assertFalse(decision.gold_eligible)

    def test_historical_notice_without_effectiveness_semantics_is_eligible(self):
        decision = self.module.review_document(
            self.document(),
            self.row(),
            self.semantic(document_kind="notice", effectiveness_signal="not_applicable"),
            conflict_ids=set(),
            integrity_ok=True,
        )

        self.assertEqual(decision.effective_status, "not_applicable")
        self.assertTrue(decision.gold_eligible)

    def test_positive_case_requires_verbatim_evidence_and_semantic_approval(self):
        document = self.document(content="差旅金额超过2000元时，由部门负责人审批。")
        case = {
            "id": "case-1",
            "document_id": "enterprise-a",
            "query": "报销额度达到两千后应交给哪个岗位处理？",
            "reference_answer": "由部门负责人审批。",
            "answer_must_include": ["部门负责人"],
            "expect_hit": True,
        }
        review_row = {"evidence": "金额超过2000元时，由部门负责人审批。"}
        semantic = {
            "evidence_supported": True,
            "answer_complete": True,
            "question_natural": True,
        }

        approved = self.module.review_case(case, review_row, document, semantic)
        rejected = self.module.review_case(
            case,
            {"evidence": "不存在的证据"},
            document,
            semantic,
        )

        self.assertEqual(approved.technical_decision, "approved")
        self.assertEqual(rejected.technical_decision, "rejected_evidence_not_found")

    def test_positive_case_labels_query_that_copies_source_as_lexical(self):
        document = self.document(content="差旅金额超过2000元时，由部门负责人审批。")
        case = {
            "id": "case-1",
            "document_id": "enterprise-a",
            "query": "差旅金额超过2000元时由部门负责人审批吗？",
            "reference_answer": "由部门负责人审批。",
            "answer_must_include": ["部门负责人"],
            "expect_hit": True,
        }
        semantic = {
            "evidence_supported": True,
            "answer_complete": True,
            "question_natural": True,
        }

        decision = self.module.review_case(
            case,
            {"evidence": "金额超过2000元时，由部门负责人审批。"},
            document,
            semantic,
        )

        self.assertEqual(decision.technical_decision, "approved")
        self.assertEqual(decision.retrieval_style, "lexical")

    def test_permission_negative_is_approved_only_for_confidential_document(self):
        case = {
            "id": "permission-1",
            "document_id": "enterprise-a",
            "query": "普通员工可以查看具体记录吗？",
            "reference_answer": "无权访问，应拒绝回答。",
            "expect_hit": False,
            "metadata": {"category": "permission_negative"},
        }

        confidential = self.module.review_case(
            case, {}, self.document(permission="confidential"), None
        )
        internal = self.module.review_case(case, {}, self.document(), None)

        self.assertEqual(confidential.technical_decision, "approved")
        self.assertEqual(internal.technical_decision, "rejected_permission_mismatch")

    def test_gold_draft_excludes_blocked_documents_and_labels_output_as_draft(self):
        documents = [self.document(), self.document(id="enterprise-b")]
        cases = [
            {"id": "case-a", "document_id": "enterprise-a", "query": "问题A"},
            {"id": "case-b", "document_id": "enterprise-b", "query": "问题B"},
        ]
        doc_decisions = {
            "enterprise-a": self.module.DocumentDecision(
                document_id="enterprise-a",
                final_permission="internal",
                permission_basis="source_label",
                business_domain="finance",
                document_kind="record",
                effectiveness_signal="not_applicable",
                effective_status="not_applicable",
                review_decision="auto_approved_technical",
                gold_eligible=True,
            ),
            "enterprise-b": self.module.DocumentDecision(
                document_id="enterprise-b",
                final_permission="internal",
                permission_basis="source_label",
                business_domain="unknown",
                document_kind="policy",
                effectiveness_signal="unclear",
                effective_status="pending_business_confirmation",
                review_decision="pending_business_confirmation",
                gold_eligible=False,
            ),
        }
        case_decisions = {
            "case-a": self.module.CaseDecision("case-a", "approved", True),
            "case-b": self.module.CaseDecision("case-b", "approved", True),
        }

        draft = self.module.build_gold_draft(
            {"provenance": {}}, documents, cases, doc_decisions, case_decisions, "local-model"
        )

        self.assertEqual(draft["dataset_type"], "enterprise_private_gold_draft")
        self.assertFalse(draft["provenance"]["business_approval_complete"])
        self.assertEqual([item["id"] for item in draft["documents"]], ["enterprise-a"])
        self.assertEqual([item["id"] for item in draft["cases"]], ["case-a"])


if __name__ == "__main__":
    unittest.main()
