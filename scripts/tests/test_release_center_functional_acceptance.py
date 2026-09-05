import importlib.util
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


def load_driver():
    path = ROOT / "scripts" / "release-center-functional-acceptance.py"
    spec = importlib.util.spec_from_file_location("release_center_functional_acceptance", path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


class ReleaseCenterFunctionalAcceptanceTests(unittest.TestCase):
    def test_matrix_documents_all_agent_pre_review_business_branches(self):
        matrix = (ROOT / "docs" / "release-center-functional-acceptance.md").read_text(encoding="utf-8")
        for scenario in (
            "普通受管文档",
            "机密文档",
            "内容级敏感信息",
            "内容级提示词注入",
            "确定性门禁阻塞",
            "版本在审批期间变化",
            "非管理员或发起人自审",
        ):
            self.assertIn(scenario, matrix)

    def test_runner_exposes_http_matrix_and_secret_free_report(self):
        driver = load_driver()
        source = (ROOT / "scripts" / "release-center-functional-acceptance.py").read_text(encoding="utf-8")
        for expected in (
            "ordinary_managed_document",
            "confidential_two_admins",
            "confidential_sensitive_two_admins",
            "agent_unavailable_manual_exception",
            "internal_sensitive_content",
            "prompt_injection_content",
            "deterministic_gate_blocker",
            "stale_request_after_replacement",
            "rejected_request_terminal",
            "cross_tenant_isolation",
            "review-reports",
            "evidence_ref",
            "redis-state",
            '"schema_version"',
            '"scenarios"',
            'runner.write("failed")',
            'first_approval_state',
            'idempotent_replay',
            'non_admin_rejected',
            'conflict_rejected',
        ):
            self.assertIn(expected, source)
        sanitized = driver.sanitize({"password": "secret", "nested": {"Authorization": "Bearer abc"}})
        self.assertNotIn("password", sanitized)
        self.assertNotIn("Authorization", sanitized["nested"])

    def test_shell_uses_isolated_compose_project(self):
        runner = (ROOT / "scripts" / "release-center-functional-acceptance.sh").read_text(encoding="utf-8")
        self.assertIn("COMPOSE_PROJECT_NAME", runner)
        self.assertIn("docker compose down -v --remove-orphans", runner)
        self.assertIn("release-center-functional-acceptance.py", runner)


if __name__ == "__main__":
    unittest.main()
