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
        self.assertIn('user-uploads": "个人上传"', helper)
        self.assertIn('production: "生产库"', helper)
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

    def test_release_center_uses_chinese_space_and_permission(self):
        page = (ROOT / "web/app/(app)/agent/page.tsx").read_text(encoding="utf-8")
        helper = (ROOT / "web/lib/docDisplay.ts").read_text(encoding="utf-8")
        self.assertIn("spaceLabel(item.knowledge_space_id, spaces)", page)
        self.assertIn("permissionLabel(selectedDocument?.permission || selectedOverview?.permission)", page)
        self.assertIn("listKnowledgeSpaces", page)
        self.assertIn('internal: "内部"', helper)
        self.assertNotIn("{item.knowledge_space_id} ·", page)

    def test_audit_prefers_username_over_raw_uuid(self):
        page = (ROOT / "web/app/(app)/audit/page.tsx").read_text(encoding="utf-8")
        self.assertIn("actorDisplay(entry.actor_user_id, users)", page)
        self.assertIn("listUsers", page)
        self.assertNotIn("{entry.actor_user_id || \"—\"}", page)

    def test_file_type_covers_office_docs(self):
        helper = (ROOT / "web/lib/docDisplay.ts").read_text(encoding="utf-8")
        self.assertIn('xls: { label: "XLS"', helper)
        self.assertIn('pptx: { label: "PPTX"', helper)

    def test_login_copy_matches_product_draft(self):
        page = (ROOT / "web/app/login/page.tsx").read_text(encoding="utf-8")
        self.assertIn("可检索、可问答、可发布的知识资产", page)
        self.assertIn("多路召回 · 只回答已发布知识", page)
        self.assertIn("权限隔离 · 引用可追溯", page)
        self.assertNotIn("可量化", page)
        self.assertNotIn("语义检索", page)

    def test_documents_show_uploader_name_not_truncated_uuid(self):
        """The registry renders a username; a truncated UUID is not a name.

        The name has to arrive with the row. GET /v1/users is admin-only while
        /documents serves every role, so resolving it in the browser (the way
        /audit does) would 403 for readonly and user accounts.
        """
        list_page = (ROOT / "web/app/(app)/documents/page.tsx").read_text(encoding="utf-8")
        detail_page = (ROOT / "web/app/(app)/documents/[id]/page.tsx").read_text(encoding="utf-8")
        helper = (ROOT / "web/lib/docDisplay.ts").read_text(encoding="utf-8")
        for page in (list_page, detail_page):
            self.assertIn("formatUploader(doc.uploaded_by, doc.uploaded_by_name)", page)
        self.assertIn("if (name) return { label: name", helper)
        # Dead branches: uploaded_by always holds auth.GetUserID(), a UUID, so a
        # literal comparison against a username can never match.
        self.assertNotIn('id === "demo-user"', helper)
        self.assertNotIn('id === "demo-admin"', helper)

    def test_document_api_resolves_uploader_name_server_side(self):
        store = (ROOT / "services/etl-worker/internal/docstore/docstore.go").read_text(encoding="utf-8")
        handler = (ROOT / "services/etl-worker/cmd/api/doc_handlers.go").read_text(encoding="utf-8")
        types = (ROOT / "web/lib/types.ts").read_text(encoding="utf-8")
        self.assertIn("u.id::text = documents.uploaded_by", store)
        self.assertIn("uploaded_by_name", handler)
        self.assertIn("uploaded_by_name?: string", types)

    def test_demo_seed_owner_is_a_business_role(self):
        """The owner column is "business role or user", not a user id column.

        The behavioural evidence is the deployed stack (the demo documents must
        render a role after a restart), so this only pins the two things a
        refactor could silently drop: the readable value, and the convergence
        clause that reaches an already-seeded database.
        """
        seed = (ROOT / "services/etl-worker/cmd/api/demo_showcase.go").read_text(encoding="utf-8")
        self.assertIn("userID, now, doc.owner, demoSpaceID", seed)
        # Without the conflict branch an already-seeded deployment keeps the UUID
        # forever: DO UPDATE only runs when a column named in its WHERE moves.
        self.assertIn("documents.owner=$14", seed)
        self.assertIn('owner: "人力资源部"', seed)
        self.assertIn('owner: "财务部"', seed)


if __name__ == "__main__":
    unittest.main()
