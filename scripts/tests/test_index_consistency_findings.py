"""契约：跨层索引一致性对账的判定规则。

`scripts/check-index-consistency.py` 是 2026-09-24 加上的诊断脚本。加它的理由是一个
实测出来的盲区：同一份文档在四个层里各说各话，而仓库里**没有任何东西**比较它们。

    Postgres        documents / index_manifests     产品认为存在什么
    Qdrant          documents-v2                    向量检索能返回什么
    Elasticsearch   documents_text_v2               关键词检索能返回什么
    Query API       GET /v1/documents/{id}/chunks   页面能显示什么

已发布文档可以在一层可见、另一层不可见，而没有测试、告警或对账任务会注意到。现有
的 manifest 对账只覆盖**有 manifest 行**的文档，所以从来没有拿到过 manifest 的文档
在构造上就在它的射程之外。

这里只测判定规则（纯函数 classify_document），不连服务 —— 规则写错时症状是「对账
永远报绿」，光靠跑一次正例发现不了。

三条判定：
  keyword_unsearchable   ES 里没有块而 Qdrant 有 → 全文召回静默丢掉这份文档
  count_mismatch         ES 与 Qdrant 的块数不一致
  citation_unverifiable  端点隐藏了 Qdrant 仍在提供的块 → 引用里的 chunk id
                         在文档页面上找不到
"""

import importlib.util
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "check-index-consistency.py"


def load_module():
    spec = importlib.util.spec_from_file_location("check_index_consistency", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


MODULE = load_module()


def kinds(findings):
    return [finding["kind"] for finding in findings]


class ClassifyDocumentTest(unittest.TestCase):
    def test_consistent_document_reports_nothing(self):
        findings = MODULE.classify_document(
            "d1", {"file_name": "a.txt"}, ["d1_0000"], 1, ["d1_0000"]
        )
        self.assertEqual(findings, [])

    def test_document_absent_from_the_text_index(self):
        findings = MODULE.classify_document(
            "d1", {"file_name": "a.txt"}, ["d1_0000", "d1_0001"], 0, ["d1_0000", "d1_0001"]
        )
        self.assertEqual(kinds(findings), ["keyword_unsearchable"])
        self.assertEqual(findings[0]["qdrant_chunks"], 2)
        self.assertEqual(findings[0]["es_chunks"], 0)

    def test_chunk_count_disagreement(self):
        findings = MODULE.classify_document(
            "d1", {"file_name": "a.txt"}, ["d1_0000", "d1_0001"], 1, ["d1_0000", "d1_0001"]
        )
        self.assertEqual(kinds(findings), ["count_mismatch"])

    def test_endpoint_hides_a_chunk_search_still_serves(self):
        findings = MODULE.classify_document(
            "d1", {"file_name": "a.txt"}, ["d1_0000", "d1_0001"], 2, ["d1_0000"]
        )
        self.assertEqual(kinds(findings), ["citation_unverifiable"])
        self.assertEqual(findings[0]["hidden_chunk_ids"], ["d1_0001"])
        self.assertEqual(findings[0]["endpoint_chunks"], 1)

    def test_missing_text_index_and_hidden_chunk_are_both_reported(self):
        findings = MODULE.classify_document(
            "d1", {"file_name": "a.txt"}, ["d1_0000", "d1_0001"], 0, ["d1_0000"]
        )
        self.assertEqual(sorted(kinds(findings)), ["citation_unverifiable", "keyword_unsearchable"])

    def test_document_without_stored_chunks_reports_nothing(self):
        # 没有块的文档不是不一致 —— 它只是没有内容，对账不该在这里造出噪声。
        findings = MODULE.classify_document("d1", {"file_name": "a.txt"}, [], 0, [])
        self.assertEqual(findings, [])

    def test_hidden_ids_are_sorted_and_deduplicated(self):
        findings = MODULE.classify_document(
            "d1", {"file_name": "a.txt"}, ["d1_0001", "d1_0000", "d1_0001"], 3, []
        )
        self.assertEqual(findings[0]["hidden_chunk_ids"], ["d1_0000", "d1_0001"])

    def test_file_name_and_publication_status_are_carried_through(self):
        findings = MODULE.classify_document(
            "d1",
            {"file_name": "kafka-recovery.txt", "publication_status": "published"},
            ["d1_0000"],
            0,
            ["d1_0000"],
        )
        self.assertEqual(findings[0]["file_name"], "kafka-recovery.txt")
        self.assertEqual(findings[0]["publication_status"], "published")


if __name__ == "__main__":
    unittest.main()
