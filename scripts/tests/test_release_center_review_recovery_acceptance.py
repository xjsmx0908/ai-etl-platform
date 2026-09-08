import importlib.util
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


def load_driver():
    path = ROOT / "scripts" / "release-center-review-recovery-acceptance.py"
    spec = importlib.util.spec_from_file_location("release_center_review_recovery_acceptance", path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


class ReleaseCenterReviewRecoveryAcceptanceTests(unittest.TestCase):
    def test_driver_requires_compose_faults_and_same_run_resume(self):
        source = (ROOT / "scripts" / "release-center-review-recovery-acceptance.py").read_text(encoding="utf-8")
        for expected in (
            "redis-state",
            "query-api",
            '"kill"',
            "agent:run:review-run-",
            "idempotency_key",
            "approval_pending",
            "get_review_context",
            "scan_sensitive_data",
            "scan_prompt_injection",
            "assess_knowledge_fitness",
            "schema_version",
            "REVIEW_PLANNER_HOLD_PATH",
        ):
            self.assertIn(expected, source)

    def test_public_run_strips_observations_and_preserves_audit_fields(self):
        driver = load_driver()
        payload = driver.public_run(
            {
                "id": "review-run-1",
                "state": "running",
                "token": "secret",
                "steps": [
                    {
                        "index": 1,
                        "type": "tool_call",
                        "state": "completed",
                        "tool_name": "get_review_context",
                        "idempotency_key": "agent:review-run-1:1:get_review_context",
                        "observation": "document contents must not be stored",
                    }
                ],
            }
        )
        self.assertEqual(payload["id"], "review-run-1")
        self.assertEqual(payload["steps"][0]["idempotency_key"], "agent:review-run-1:1:get_review_context")
        self.assertNotIn("observation", payload["steps"][0])
        sanitized = driver.sanitize({"token": "secret", "run": payload})
        self.assertNotIn("token", sanitized)

    def test_shell_uses_isolated_stack_and_held_planner_mock(self):
        source = (ROOT / "scripts" / "release-center-review-recovery-acceptance.sh").read_text(encoding="utf-8")
        for expected in (
            "COMPOSE_PROJECT_NAME",
            'AGENT_PLANNER_TYPE="auto"',
            "review-agent-planner-mock.py",
            "--host 0.0.0.0",
            "REVIEW_PLANNER_HOLD_PATH",
            "release-center-review-recovery-acceptance.py",
            "GOVERNANCE_REPORT_DIR",
            "docker compose down -v --remove-orphans",
        ):
            self.assertIn(expected, source)
        gitignore = (ROOT / ".gitignore").read_text(encoding="utf-8")
        self.assertIn("/artifacts/release-center-review-recovery-acceptance/", gitignore)


if __name__ == "__main__":
    unittest.main()
