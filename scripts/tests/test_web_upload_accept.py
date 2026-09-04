import re
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


class WebUploadAcceptTests(unittest.TestCase):
    def test_all_upload_inputs_share_the_supported_office_extensions(self):
        file_types = (ROOT / "web" / "lib" / "fileTypes.ts").read_text(encoding="utf-8")
        match = re.search(r'UPLOAD_ACCEPT\s*=\s*"([^"]+)"', file_types)
        self.assertIsNotNone(match)
        accepted = set(match.group(1).split(","))
        self.assertTrue({".doc", ".docx", ".xls", ".xlsx", ".pptx"} <= accepted)

        for relative_path in (
            "web/app/(app)/data/page.tsx",
            "web/app/(app)/documents/[id]/page.tsx",
        ):
            source = (ROOT / relative_path).read_text(encoding="utf-8")
            self.assertIn("accept={UPLOAD_ACCEPT}", source)

    def test_managed_upload_exposes_space_creation_and_governance_fields(self):
        source = (ROOT / "web/app/(app)/data/page.tsx").read_text(encoding="utf-8")
        self.assertIn("新建受管空间", source)
        self.assertIn("创建 production 受管空间", source)
        self.assertIn("责任人", source)
        self.assertIn('type="date"', source)
        self.assertIn("effectiveDate", source)

    def test_upload_client_sends_governance_fields(self):
        source = (ROOT / "web/lib/apiClient.ts").read_text(encoding="utf-8")
        for field in ("doc_status", "owner", "effective_date"):
            self.assertIn(f'fd.append("{field}"', source)
        self.assertIn("createKnowledgeSpace", source)

    def test_release_center_is_the_primary_publication_entry(self):
        source = (ROOT / "web/app/(app)/release-center/page.tsx").read_text(encoding="utf-8")
        shell = (ROOT / "web/components/AppShell.tsx").read_text(encoding="utf-8")
        legacy = (ROOT / "web/app/(app)/agent/page.tsx").read_text(encoding="utf-8")
        detail = (ROOT / "web/app/(app)/documents/[id]/page.tsx").read_text(encoding="utf-8")
        upload = (ROOT / "web/app/(app)/data/page.tsx").read_text(encoding="utf-8")

        self.assertIn('export { default } from "../agent/page"', source)
        self.assertIn('{ href: "/release-center", label: "知识发布中心"', shell)
        # `/agent` remains a compatibility alias while the new route is primary.
        self.assertIn("知识发布中心", legacy)
        self.assertIn('href="/release-center"', detail)
        self.assertNotIn("另一位管理员审批", detail)
        self.assertNotIn("另一位管理员审批", upload)
        for expected in (
            "审计/恢复 Run ID",
            "Agent 执行详情（审计信息）",
            "exact_candidate_unavailable",
            "索引版本尚未形成可发布候选",
            "开始 Agent 检查",
        ):
            self.assertIn(expected, legacy)

    def test_release_center_client_reads_durable_request_queue(self):
        client = (ROOT / "web/lib/apiClient.ts").read_text(encoding="utf-8")
        proxy = (ROOT / "web/app/api/release-center/requests/route.ts").read_text(encoding="utf-8")
        overview_proxy = (ROOT / "web/app/api/release-center/overview/route.ts").read_text(encoding="utf-8")
        page = (ROOT / "web/app/(app)/agent/page.tsx").read_text(encoding="utf-8")
        self.assertIn("listReleaseRequests", client)
        self.assertIn('request<ReleaseRequestsResponse>("/release-center/requests")', client)
        self.assertIn("/v1/release-center/requests", proxy)
        self.assertIn("/v1/release-center/overview", overview_proxy)
        self.assertIn("listReleaseOverview", client)
        # Durable requests are independently selectable; they must not disappear
        # merely because the document is no longer in the draft-only list.
        self.assertIn("发布申请", page)
        self.assertIn("setSelectedRequestID(request.request_id)", page)
        self.assertIn("已批准 {approvedDecisionCount} / {requestDetail.request.required_approvals}", page)

    def test_release_center_renders_business_state_filters_and_blockers(self):
        page = (ROOT / "web/app/(app)/agent/page.tsx").read_text(encoding="utf-8")
        for expected in (
            "状态筛选",
            "全部状态",
            "needs_info",
            "review_blocked",
            "approval_pending",
            "blockers",
            "确定性门禁阻塞",
        ):
            self.assertIn(expected, page)

    def test_release_center_overview_rows_are_selectable_without_request_history(self):
        page = (ROOT / "web/app/(app)/agent/page.tsx").read_text(encoding="utf-8")
        for expected in (
            "业务状态记录",
            "setSelectedOverviewID(item.document_id)",
            "apiClient.getDocument(item.document_id)",
            "overviewFilter === \"all\" || item.state === overviewFilter",
        ):
            self.assertIn(expected, page)

    def test_release_center_switching_to_request_or_draft_clears_overview_selection(self):
        page = (ROOT / "web/app/(app)/agent/page.tsx").read_text(encoding="utf-8")
        self.assertGreaterEqual(page.count("setSelectedOverviewID(\"\")"), 2)

    def test_release_center_overview_selection_is_read_only_context(self):
        page = (ROOT / "web/app/(app)/agent/page.tsx").read_text(encoding="utf-8")
        self.assertIn("selected && !run && !selectedRequest && !selectedOverview", page)
        self.assertIn("!run && selected && !selectedOverview", page)


if __name__ == "__main__":
    unittest.main()
