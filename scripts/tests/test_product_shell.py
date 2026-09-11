import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


class ProductShellTests(unittest.TestCase):
    def test_shell_groups_admin_nav_and_page_title(self):
        shell = (ROOT / "web/components/AppShell.tsx").read_text(encoding="utf-8")
        header = (ROOT / "web/components/ui/PageHeader.tsx").read_text(encoding="utf-8")
        self.assertIn("管理", shell)
        self.assertIn("pageTitle(pathname)", shell)
        self.assertIn("function PageHeader", header)

    def test_qa_hides_internal_metrics_and_raw_citation_score(self):
        page = (ROOT / "web/app/(app)/qa/page.tsx").read_text(encoding="utf-8")
        self.assertIn("回答依据与系统详情", page)
        self.assertIn("引用（{citations.length}）", page)
        self.assertIn("{c.file_name || c.doc_id}", page)
        self.assertNotIn("score {s.score.toFixed(4)}", page)
        self.assertNotIn("实际引用", page)

    def test_data_pipeline_and_status_are_chinese(self):
        page = (ROOT / "web/app/(app)/data/page.tsx").read_text(encoding="utf-8")
        for expected in ("解析文档", "向量化", "入库", "文档编号", "STAGE_LABELS", "切块 {prog}"):
            self.assertIn(expected, page)
        self.assertNotIn("doc: {uploadResult.doc_id}", page)
        self.assertNotIn("chunk {prog}", page)

    def test_documents_prefer_filename_and_space_name(self):
        page = (ROOT / "web/app/(app)/documents/page.tsx").read_text(encoding="utf-8")
        helper = (ROOT / "web/lib/docDisplay.ts").read_text(encoding="utf-8")
        self.assertLess(page.find("{doc.file_name}"), page.find("{doc.doc_id}"))
        self.assertIn("spaceLabel(doc.knowledge_space_id, spaces)", page)
        self.assertIn('id === "user-uploads" ? "个人上传"', helper)
        self.assertNotIn("{doc.knowledge_space_id || \"—\"}", page)

    def test_document_detail_uses_page_header_and_chinese_governance(self):
        page = (ROOT / "web/app/(app)/documents/[id]/page.tsx").read_text(encoding="utf-8")
        self.assertIn("PageHeader", page)
        self.assertIn("spaceLabel(doc.knowledge_space_id, spaces)", page)
        self.assertIn(">有效<", page)
        self.assertIn(">已替代<", page)
        self.assertIn(">归档<", page)
        self.assertIn("新文件将替换「{doc.file_name}」的内容", page)
        self.assertNotIn("active（有效）", page)

    def test_release_center_uses_page_header_without_changing_gates(self):
        page = (ROOT / "web/app/(app)/agent/page.tsx").read_text(encoding="utf-8")
        self.assertIn("PageHeader", page)
        self.assertIn("按业务状态查看预审证据，并完成管理员审批", page)
        self.assertIn("确定性门禁", page)
        self.assertIn("批准发布", page)

    def test_quality_copy_does_not_expose_report_path(self):
        page = (ROOT / "web/app/(app)/quality/page.tsx").read_text(encoding="utf-8")
        self.assertIn("离线评测回归", page)
        self.assertNotIn("docs/evals/reports", page)


if __name__ == "__main__":
    unittest.main()
