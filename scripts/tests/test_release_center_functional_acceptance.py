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
            "知识空间用途缺失",
            "知识空间不适配",
            "不能作为正式知识",
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
            "knowledge_space_fit_match",
            "knowledge_space_unfit",
            "not_formal_knowledge",
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

    def test_redis_probe_authenticates_with_the_service_secret(self):
        """redis-state needs a password, and an unauthenticated probe does not fail -- it hangs.

        `redis-cli ping` against a password-protected server prints
        "NOAUTH Authentication required." and **exits 0**. The agent-outage scenario
        restarts redis-state and waits for the probe to say PONG, so without the
        secret it could not tell "not up yet" from "up but unauthenticated" and
        reported "redis-state did not recover" on a perfectly healthy service --
        which stopped the matrix before its last nine scenarios.
        """
        driver = load_driver()
        command = driver.redis_exec_command("ping")
        joined = " ".join(command)
        self.assertIn("REDISCLI_AUTH", joined)
        self.assertIn("/run/secrets/redis_password", joined)
        self.assertLess(joined.index("REDISCLI_AUTH"), joined.index("redis-cli"))
        self.assertTrue(command[-1].rstrip().endswith("ping"), command[-1])

    def test_review_recovery_suite_reuses_the_authenticated_probe(self):
        source = (ROOT / "scripts" / "release-center-review-recovery-acceptance.py").read_text(encoding="utf-8")
        # 复用同一份实现，避免「修了一个忘了另一个」——这两个脚本曾经各有一份不带认证的探活
        self.assertIn("functional.redis_exec_command", source)
        self.assertNotIn('"redis-state", "redis-cli"', source)
        functional_source = (ROOT / "scripts" / "release-center-functional-acceptance.py").read_text(encoding="utf-8")
        self.assertNotIn('"redis-state", "redis-cli"', functional_source)


if __name__ == "__main__":
    unittest.main()
