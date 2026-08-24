import importlib.util
import sys
import unittest
from dataclasses import replace
from pathlib import Path
from unittest.mock import patch


SCRIPT = Path(__file__).resolve().parents[1] / "build-enterprise-eval.py"


def load_module():
    spec = importlib.util.spec_from_file_location("build_enterprise_eval", SCRIPT)
    if spec is None or spec.loader is None:
        raise RuntimeError("failed to load build-enterprise-eval.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


class EnterpriseEvalBuilderTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.module = load_module()

    def test_document_id_is_stable_and_does_not_expose_filename(self):
        first = self.module.stable_document_id("OA账号.xls", "a" * 64)
        second = self.module.stable_document_id("renamed.xls", "a" * 64)

        self.assertEqual(first, second)
        self.assertEqual(first, "enterprise-aaaaaaaaaaaaaaaa")
        self.assertNotIn("OA", first)

    def test_dataset_metadata_does_not_spoof_authoritative_knowledge_space(self):
        document = self.module.ExtractedDocument(
            document_id="enterprise-a",
            filename="policy.pdf",
            source_path="rag_datas/policy.pdf",
            sha256="a" * 64,
            file_size=1,
            extension=".pdf",
            parser="pdf",
            content="制度正文" * 100,
            permission="internal",
            sensitivity_flags=[],
            pii_flags=[],
            permission_requires_review=False,
            quality_flags=[],
        )

        metadata = self.module.dataset_document(document)["metadata"]

        self.assertNotIn("knowledge_space_id", metadata)
        self.assertEqual(metadata["eval_cohort"], "enterprise-p14")

    def test_sensitive_filename_defaults_to_confidential_and_review(self):
        permission, flags, requires_review = self.module.classify_document("员工打卡时间.xlsx")

        self.assertEqual(permission, "confidential")
        self.assertIn("attendance", flags)
        self.assertTrue(requires_review)

    def test_general_policy_defaults_to_internal(self):
        permission, flags, requires_review = self.module.classify_document("差旅管理通知.pdf")

        self.assertEqual(permission, "internal")
        self.assertEqual(flags, [])
        self.assertFalse(requires_review)

    def test_low_content_document_is_flagged_for_review(self):
        self.assertEqual(self.module.extraction_quality_flags("只有标题"), ["low_content"])
        self.assertEqual(self.module.extraction_quality_flags("有效制度正文" * 50), [])

    def test_representative_content_fits_local_model_context(self):
        content = "开" + ("中" * 20_000) + "结"

        representative = self.module.representative_content(content)

        self.assertLessEqual(len(representative), 5_100)
        self.assertTrue(representative.startswith("开"))
        self.assertTrue(representative.endswith("结"))

    def test_evidence_passages_are_verbatim_diverse_and_exclude_pii(self):
        content = """--- Page 1 ---
员工申请报销时，应当先在系统中提交完整的费用明细。
紧急联系电话为13800138000，仅用于当日联络。
--- Page 2 ---
部门负责人审批通过后，申请人再向财务部门提交纸质发票。
财务部门完成复核后，应将处理结果通过系统通知申请人。"""

        passages = self.module.select_evidence_passages(content, count=3)

        self.assertEqual(len(passages), 3)
        self.assertEqual(len(set(passages)), 3)
        self.assertTrue(all(passage in content for passage in passages))
        self.assertTrue(all("13800138000" not in passage for passage in passages))
        self.assertTrue(all("--- Page" not in passage for passage in passages))

    def test_draft_questions_maps_local_evidence_ids_to_verbatim_text(self):
        document = self.module.ExtractedDocument(
            document_id="enterprise-a",
            filename="policy.pdf",
            source_path="rag_datas/policy.pdf",
            sha256="a" * 64,
            file_size=1,
            extension=".pdf",
            parser="pdf",
            content="制度正文" * 100,
            permission="internal",
            sensitivity_flags=[],
            pii_flags=[],
            permission_requires_review=False,
            quality_flags=[],
        )
        evidence = ["申请人需先提交完整材料后再进入审批流程。", "部门负责人应在两个工作日内完成审批。"]
        response = {
            "cases": [
                {
                    "question": "审批需在多久内完成？",
                    "reference_answer": "应在两个工作日内完成。",
                    "evidence_id": "E02",
                    "answer_key_phrases": ["两个工作日"],
                    "category": "time",
                    "confidence": 0.96,
                },
                {
                    "question": "不应接收的无效证据是什么？",
                    "reference_answer": "无法确定。",
                    "evidence_id": "E99",
                    "answer_key_phrases": ["无法确定"],
                    "category": "fact",
                    "confidence": 0.2,
                },
            ]
        }

        with (
            patch.object(self.module, "select_evidence_passages", return_value=evidence),
            patch.object(self.module, "ollama_json", return_value=response) as mocked_ollama,
        ):
            cases = self.module.draft_questions(
                document,
                endpoint="http://localhost:11434",
                model="local-model",
                count=2,
                timeout=10,
            )

        self.assertEqual(len(cases), 1)
        self.assertEqual(cases[0]["evidence"], evidence[1])
        self.assertNotIn("evidence_id", cases[0])
        schema = mocked_ollama.call_args.kwargs["response_schema"]
        evidence_ids = schema["properties"]["cases"]["items"]["properties"]["evidence_id"]["enum"]
        self.assertEqual(evidence_ids, ["E01", "E02"])

    def test_question_schema_and_cache_merge_enforce_requested_count(self):
        schema = self.module.question_schema(3)
        cases_schema = schema["properties"]["cases"]
        self.assertEqual(cases_schema["minItems"], 3)
        self.assertEqual(cases_schema["maxItems"], 3)
        item_properties = cases_schema["items"]["properties"]
        self.assertEqual(item_properties["evidence"]["maxLength"], 120)
        self.assertEqual(item_properties["reference_answer"]["maxLength"], 300)
        self.assertEqual(item_properties["answer_key_phrases"]["maxItems"], 2)

        existing = [{"question": "问题一"}]
        drafted = [{"question": "问题一"}, {"question": "问题二"}, {"question": "问题三"}]
        merged = self.module.merge_candidates(existing, drafted, limit=3)
        self.assertEqual([item["question"] for item in merged], ["问题一", "问题二", "问题三"])

    def test_previous_timeout_uses_reduced_retry_profile(self):
        rejected = [
            {"document_id": "slow-doc", "reason": "model_error:TimeoutError"},
            {"document_id": "other-doc", "reason": "unsupported_key_phrase"},
        ]

        timeout_ids = self.module.timeout_document_ids(rejected)

        self.assertEqual(timeout_ids, {"slow-doc"})
        self.assertEqual(self.module.generation_profile("slow-doc", timeout_ids), (3_000, 600))
        self.assertEqual(self.module.generation_profile("new-doc", timeout_ids), (5_000, 700))

    def test_near_duplicate_versions_are_routed_to_conflict_review(self):
        base = self.module.ExtractedDocument(
            document_id="enterprise-a",
            filename="在职人员年休假信息收集表.xls",
            source_path="rag_datas/a.xls",
            sha256="a" * 64,
            file_size=1,
            extension=".xls",
            parser="xls",
            content="年休假信息填报要求和字段说明。" * 30,
            permission="confidential",
            sensitivity_flags=["attendance"],
            pii_flags=[],
            permission_requires_review=True,
            quality_flags=[],
        )
        copied = replace(
            base,
            document_id="enterprise-b",
            filename="在职人员年休假信息收集表(1).xls",
            source_path="rag_datas/b.xls",
            sha256="b" * 64,
            content=base.content + "本次更新。",
        )

        pairs = self.module.find_potential_conflicts([base, copied])

        self.assertEqual(len(pairs), 1)
        self.assertEqual(pairs[0]["reason"], "possible_version_or_overlap")

    def test_no_answer_cases_use_accessible_quality_documents(self):
        confidential = self.module.ExtractedDocument(
            document_id="enterprise-confidential",
            filename="account.xls",
            source_path="rag_datas/account.xls",
            sha256="a" * 64,
            file_size=1,
            extension=".xls",
            parser="xls",
            content="账号资料" * 100,
            permission="confidential",
            sensitivity_flags=["credentials"],
            pii_flags=[],
            permission_requires_review=True,
            quality_flags=[],
        )
        internal = replace(
            confidential,
            document_id="enterprise-internal",
            filename="policy.pdf",
            source_path="rag_datas/policy.pdf",
            sha256="b" * 64,
            permission="internal",
            sensitivity_flags=[],
            permission_requires_review=False,
        )

        cases = self.module.no_answer_cases([confidential, internal], count=1)

        self.assertEqual(len(cases), 1)
        self.assertEqual(cases[0]["document_id"], "enterprise-internal")

    def test_candidate_requires_verbatim_evidence_and_supported_key_phrases(self):
        content = "差旅审批规则如下：单次金额超过2000元时，由部门负责人审批。"
        valid = {
            "question": "单次差旅金额超过2000元由谁审批？",
            "reference_answer": "单次差旅金额超过2000元时，由部门负责人审批。",
            "evidence": "单次金额超过2000元时，由部门负责人审批。",
            "answer_key_phrases": ["部门负责人", "2000元"],
            "category": "procedure",
            "confidence": 0.94,
        }
        invalid = dict(valid, evidence="不存在的证据")

        accepted = self.module.validate_candidate(valid, content)
        rejected = self.module.validate_candidate(invalid, content)

        self.assertTrue(accepted.accepted)
        self.assertGreaterEqual(accepted.evidence_start, 0)
        self.assertFalse(accepted.requires_review)
        self.assertFalse(rejected.accepted)
        self.assertEqual(rejected.reason, "evidence_not_found")

    def test_candidate_keeps_only_key_phrases_supported_by_answer_and_evidence(self):
        content = "差旅审批规则：金额超过2000元时，由部门负责人审批。"
        candidate = {
            "question": "金额超过2000元由谁审批？",
            "reference_answer": "由部门负责人审批。",
            "evidence": content,
            "answer_key_phrases": ["部门负责人", "财务总监"],
            "category": "procedure",
            "confidence": 0.93,
        }

        result = self.module.validate_candidate(candidate, content)

        self.assertTrue(result.accepted)
        self.assertEqual(result.supported_key_phrases, ["部门负责人"])

    def test_evidence_match_ignores_pdf_layout_whitespace_and_keeps_page_locator(self):
        content = "--- Page 1 ---\n目录\n--- Page 2 ---\n31. 销毁载体应由工作机\n构处理，并由专人押运监销。"
        candidate = {
            "question": "销毁载体应由谁处理？",
            "reference_answer": "销毁载体应由工作机构处理，并由专人押运监销。",
            "evidence": "31. 销毁载体应由工作机构处理，并由专人押运监销。",
            "answer_key_phrases": ["工作机构", "专人押运监销"],
            "category": "procedure",
            "confidence": 0.96,
        }

        result = self.module.validate_candidate(candidate, content)

        self.assertTrue(result.accepted)
        self.assertEqual(result.evidence_locator, "Page 2")

    def test_low_confidence_candidate_is_retained_for_review(self):
        content = "制度规定报销需要提交发票。"
        candidate = {
            "question": "报销需要提交什么？",
            "reference_answer": "需要提交发票。",
            "evidence": "制度规定报销需要提交发票。",
            "answer_key_phrases": ["发票"],
            "category": "fact",
            "confidence": 0.61,
        }

        result = self.module.validate_candidate(candidate, content)

        self.assertTrue(result.accepted)
        self.assertTrue(result.requires_review)
        self.assertEqual(result.reason, "low_confidence")

    def test_candidate_that_repeats_pii_is_rejected(self):
        content = "员工联系电话为13800138000，仅用于紧急联络。"
        candidate = {
            "question": "该员工的联系电话是什么？",
            "reference_answer": "联系电话为13800138000。",
            "evidence": content,
            "answer_key_phrases": ["13800138000"],
            "category": "fact",
            "confidence": 0.99,
        }

        result = self.module.validate_candidate(candidate, content)

        self.assertFalse(result.accepted)
        self.assertEqual(result.reason, "candidate_contains_pii")


if __name__ == "__main__":
    unittest.main()
