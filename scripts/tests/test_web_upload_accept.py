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
            "Agent 执行详情（审计信息）",
            "exact_candidate_unavailable",
            "索引版本尚未形成可发布候选",
            "发布阶段",
        ):
            self.assertIn(expected, legacy)
        self.assertNotIn("审计/恢复 Run ID", legacy)

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
        # Durable requests remain the approval action seam while the page uses
        # one unified business-record list.
        self.assertIn("统一业务记录", page)
        self.assertIn("getReleaseRequest", page)
        self.assertIn("approvedCount", page)

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
            "统一业务记录",
            "setSelectedOverviewID(item.document_id)",
            "apiClient.getDocument(item.document_id)",
            "overviewFilter === \"all\" || item.state === overviewFilter",
        ):
            self.assertIn(expected, page)

    def test_release_center_switching_to_request_or_draft_clears_overview_selection(self):
        page = (ROOT / "web/app/(app)/agent/page.tsx").read_text(encoding="utf-8")
        self.assertIn("setSelectedOverviewID(item.document_id)", page)

    def test_release_center_overview_selection_is_read_only_context(self):
        page = (ROOT / "web/app/(app)/agent/page.tsx").read_text(encoding="utf-8")
        self.assertIn("!selectedOverview && !selectedRequest", page)

    def test_release_center_refreshes_overview_after_decision_and_supports_sorted_pagination(self):
        page = (ROOT / "web/app/(app)/agent/page.tsx").read_text(encoding="utf-8")
        for expected in (
            "const refreshReleaseCenter",
            "await refreshReleaseCenter()",
            "overviewPage",
            "overviewPageSize",
            "overviewSorted",
            "业务状态分页",
            "最后同步",
        ):
            self.assertIn(expected, page)

    def test_release_center_uses_one_business_record_list_and_timeline(self):
        page = (ROOT / "web/app/(app)/agent/page.tsx").read_text(encoding="utf-8")
        for expected in (
            "统一业务记录",
            "发布阶段",
            "确定性门禁",
            "Agent 预审",
            "管理员审批",
            "精确版本发布",
            "Agent 预审中",
            "Agent 建议",
        ):
            self.assertIn(expected, page)
        self.assertNotIn("受管草稿", page)
        self.assertNotIn("审计/恢复 Run ID", page)

    def test_release_center_does_not_claim_second_approval_after_terminal_decision(self):
        page = (ROOT / "web/app/(app)/agent/page.tsx").read_text(encoding="utf-8")
        self.assertIn("审批已生效，发布流程已完成", page)
        self.assertIn("detail.request.state === 'approval_pending'", page)

    def test_release_center_exposes_pre_review_findings_and_evidence(self):
        page = (ROOT / "web/app/(app)/agent/page.tsx").read_text(encoding="utf-8")
        for expected in (
            "预审发现",
            "finding.code",
            "finding.evidence_ref",
            "敏感信息",
            "提示词注入",
            "预审时间",
            "Prompt 版本",
            "风险升高，仍需双人审批",
        ):
            self.assertIn(expected, page)

    def test_release_center_observability_has_dedicated_route_and_dashboard_panels(self):
        metrics = (ROOT / "services/etl-worker/internal/prometheus/metrics.go").read_text(encoding="utf-8")
        dashboard = (ROOT / "infrastructure/grafana/dashboards/ai-etl-platform.json").read_text(encoding="utf-8")
        alerts = (ROOT / "infrastructure/rules/etl-alerts.yml").read_text(encoding="utf-8")
        self.assertIn('strings.HasPrefix(path, "/v1/release-center")', metrics)
        self.assertIn('/v1/release-center/overview', dashboard)
        self.assertIn('/v1/release-center/requests', dashboard)
        self.assertIn("ReleaseCenterHighErrorRate", alerts)


if __name__ == "__main__":
    unittest.main()
