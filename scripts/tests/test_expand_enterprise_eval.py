import importlib.util
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "expand-enterprise-eval.py"


def load_module():
    spec = importlib.util.spec_from_file_location("expand_enterprise_eval", SCRIPT)
    if spec is None or spec.loader is None:
        raise RuntimeError("failed to load expand-enterprise-eval.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


class ExpandEnterpriseEvalTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.module = load_module()

    @staticmethod
    def document(document_id="doc-a", domain="finance", permission="internal"):
        return {
            "id": document_id,
            "filename": f"{document_id}.docx",
            "permission": permission,
            "content": "差旅申请由直属负责人审批。报销材料应在返程后十个工作日内提交。",
            "metadata": {"business_domain": domain},
        }

    @staticmethod
    def generated_case():
        return {
            "query": "出差回来后，最晚什么时候需要交报销材料？",
            "reference_answer": "报销材料应在返程后十个工作日内提交。",
            "answer_must_include": ["十个工作日"],
        }

    def test_semantic_candidate_requires_verbatim_evidence_and_supported_key_facts(self):
        document = self.document()

        result = self.module.validate_semantic_candidate(
            self.generated_case(),
            document,
            "报销材料应在返程后十个工作日内提交。",
        )

        self.assertEqual(result, [])

        bad = self.generated_case()
        bad["answer_must_include"] = ["十五个工作日"]
        reasons = self.module.validate_semantic_candidate(
            bad,
            document,
            "报销材料应在返程后十个工作日内提交。",
        )
        self.assertIn("key_fact_not_supported", reasons)

        reasons = self.module.validate_semantic_candidate(
            self.generated_case(), document, "原文不存在的证据"
        )
        self.assertIn("evidence_not_verbatim", reasons)

    def test_semantic_candidate_rejects_keyword_copy(self):
        generated = self.generated_case()
        generated["query"] = "报销材料返程后十个工作日内提交"

        reasons = self.module.validate_semantic_candidate(
            generated,
            self.document(),
            "报销材料应在返程后十个工作日内提交。",
        )

        self.assertIn("query_too_lexical", reasons)

    def test_semantic_overlap_is_measured_against_bound_evidence_not_whole_document(self):
        document = self.document()
        document["content"] += "\n常见问法：临时外出结束后，相关票据最迟何时递交？"
        generated = self.generated_case()
        generated["query"] = "临时外出结束后，相关票据最迟何时递交？"

        reasons = self.module.validate_semantic_candidate(
            generated,
            document,
            "报销材料应在返程后十个工作日内提交。",
        )

        self.assertNotIn("query_too_lexical", reasons)

    def test_cross_document_candidate_requires_two_compatible_sources(self):
        left = self.document("doc-a", "finance")
        right = {
            "id": "doc-b",
            "filename": "doc-b.docx",
            "permission": "internal",
            "content": "借款应在报销完成后五个工作日内核销。",
            "metadata": {"business_domain": "finance"},
        }
        generated = {
            "query": "办完这次差事后，两项后续手续各自的截止要求是什么？",
            "reference_answer": "报销材料应在返程后十个工作日内提交；借款应在报销完成后五个工作日内核销。",
            "answer_must_include": ["十个工作日", "五个工作日"],
            "source_key_facts": {"doc-a": "十个工作日", "doc-b": "五个工作日"},
        }
        evidence = {
            "doc-a": "报销材料应在返程后十个工作日内提交。",
            "doc-b": "借款应在报销完成后五个工作日内核销。",
        }

        self.assertEqual(
            self.module.validate_cross_document_candidate(generated, [left, right], evidence),
            [],
        )

        incompatible = dict(right)
        incompatible["metadata"] = {"business_domain": "human_resources"}
        reasons = self.module.validate_cross_document_candidate(
            generated, [left, incompatible], evidence
        )
        self.assertIn("incompatible_business_domains", reasons)

    def test_build_dataset_uses_explicit_cohorts_and_required_document_ids(self):
        source = {
            "provenance": {},
            "documents": [self.document(), self.document("doc-b")],
            "cases": [],
        }
        lexical = [{"id": "lex-1", "document_id": "doc-a", "metadata": {}}]
        semantic = [{"id": "sem-1", "document_id": "doc-a", "metadata": {}}]
        safety = [
            {
                "id": "safe-1",
                "document_id": "doc-a",
                "expect_hit": False,
                "metadata": {},
            }
        ]
        cross = [
            {
                "id": "cross-1",
                "document_id": "doc-a",
                "required_doc_ids": ["doc-a", "doc-b"],
                "metadata": {},
            }
        ]

        result = self.module.build_dataset(source, lexical, semantic, cross, safety)

        cohorts = {
            case["id"]: case["metadata"]["evaluation_cohort"]
            for case in result["cases"]
        }
        self.assertEqual(
            cohorts,
            {
                "lex-1": "lexical",
                "sem-1": "semantic",
                "cross-1": "cross_document",
                "safe-1": "safety_negative",
            },
        )
        self.assertEqual(result["provenance"]["label_status"], "technical_gold_candidate_v2")
        self.assertFalse(result["provenance"]["business_approval_complete"])
        self.assertFalse(result["provenance"]["external_data_transfer"])

    def test_validate_dataset_fails_closed_on_counts_review_and_references(self):
        documents = {"doc-a": self.document(), "doc-b": self.document("doc-b")}
        cases = []
        for index in range(2):
            cases.append(
                {
                    "id": f"sem-{index}",
                    "document_id": "doc-a",
                    "expect_hit": True,
                    "query": "如何办理这项业务？",
                    "reference_answer": "按规定办理。",
                    "metadata": {
                        "evaluation_cohort": "semantic",
                        "local_review_status": "approved",
                    },
                }
            )
        cases.append(
            {
                "id": "cross-1",
                "document_id": "doc-a",
                "required_doc_ids": ["doc-a", "missing"],
                "expect_hit": True,
                "query": "两项规定如何衔接？",
                "reference_answer": "按两项规定衔接。",
                "metadata": {
                    "evaluation_cohort": "cross_document",
                    "local_review_status": "approved",
                },
            }
        )

        errors = self.module.validate_final_dataset(
            {"documents": list(documents.values()), "cases": cases},
            {"semantic": 3, "cross_document": 1},
        )

        self.assertTrue(any("semantic" in error for error in errors))
        self.assertTrue(any("unknown required_doc_ids" in error for error in errors))

    def test_final_validation_rechecks_private_evidence_bindings(self):
        document = self.document()
        case = {
            "id": "sem-1",
            "document_id": "doc-a",
            "expect_hit": True,
            "query": "出差回来后，相关材料什么时候交？",
            "reference_answer": "报销材料应在返程后十个工作日内提交。",
            "answer_must_include": ["十个工作日"],
            "metadata": {
                "evaluation_cohort": "semantic",
                "local_review_status": "approved",
            },
        }
        dataset = {"documents": [document], "cases": [case]}

        valid = self.module.validate_final_dataset(
            dataset,
            {"semantic": 1},
            {"sem-1": {"doc-a": "报销材料应在返程后十个工作日内提交。"}},
        )
        tampered = self.module.validate_final_dataset(
            dataset,
            {"semantic": 1},
            {"sem-1": {"doc-a": "并不存在的证据"}},
        )

        self.assertEqual(valid, [])
        self.assertTrue(any("evidence is not verbatim" in error for error in tampered))

    def test_deterministic_case_id_is_stable_and_cohort_specific(self):
        first = self.module.deterministic_case_id("semantic", ["doc-a"], "如何办理？")
        second = self.module.deterministic_case_id("semantic", ["doc-a"], "如何办理？")
        cross = self.module.deterministic_case_id("cross_document", ["doc-a"], "如何办理？")

        self.assertEqual(first, second)
        self.assertNotEqual(first, cross)
        self.assertTrue(first.startswith("p16-semantic-"))

    def test_all_sources_review_is_required_only_for_cross_document_cases(self):
        review = {
            "evidence_supported": True,
            "answer_complete": True,
            "question_natural": True,
            "question_independent": True,
            "uses_all_sources": False,
        }

        self.assertTrue(self.module.review_approved(review, cross_document=False))
        self.assertFalse(self.module.review_approved(review, cross_document=True))

    def test_third_generation_attempt_preserves_prevalidated_key_facts(self):
        source_case = {
            "query": "原问题",
            "reference_answer": "十个工作日内办理。",
            "answer_must_include": ["十个工作日"],
        }

        ordinary = self.module.semantic_prompt(source_case, "证据", 2)
        repair = self.module.semantic_prompt(source_case, "证据", 3)

        self.assertNotIn("预验证关键短语", ordinary)
        self.assertIn("预验证关键短语", repair)
        self.assertIn("十个工作日", repair)

    def test_cross_prompt_requires_prevalidated_facts_from_each_source(self):
        prompt = self.module.cross_prompt(
            [
                {
                    "document_id": "doc-a",
                    "evidence": "证据一",
                    "prevalidated_key_facts": ["十个工作日"],
                },
                {
                    "document_id": "doc-b",
                    "evidence": "证据二",
                    "prevalidated_key_facts": ["五个工作日"],
                },
            ]
        )

        self.assertIn("预验证关键短语", prompt)
        self.assertIn("十个工作日", prompt)
        self.assertIn("五个工作日", prompt)

    def test_cross_answer_is_composed_from_reviewed_source_answers(self):
        generated = {
            "query": "两项手续分别何时完成？",
            "reference_answer": "模型改写了事实。",
            "answer_must_include": ["错误事实"],
            "source_key_facts": [],
        }
        sources = [
            {
                "document_id": "doc-a",
                "reference_answer": "第一项应在十个工作日内完成。",
                "prevalidated_key_facts": ["十个工作日"],
            },
            {
                "document_id": "doc-b",
                "reference_answer": "第二项应在五个工作日内完成。",
                "prevalidated_key_facts": ["五个工作日"],
            },
        ]

        result = self.module.materialize_cross_candidate(generated, sources)

        self.assertEqual(
            result["reference_answer"],
            "第一项应在十个工作日内完成；第二项应在五个工作日内完成。",
        )
        self.assertEqual(result["answer_must_include"], ["十个工作日", "五个工作日"])
        self.assertEqual(
            self.module.source_fact_map(result["source_key_facts"]),
            {"doc-a": "十个工作日", "doc-b": "五个工作日"},
        )


if __name__ == "__main__":
    unittest.main()
