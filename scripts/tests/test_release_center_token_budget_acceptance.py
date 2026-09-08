import importlib.util
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


def load_driver():
    path = ROOT / "scripts" / "release-center-token-budget-acceptance.py"
    spec = importlib.util.spec_from_file_location("release_center_token_budget_acceptance", path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


class ReleaseCenterTokenBudgetAcceptanceTests(unittest.TestCase):
    def test_driver_requires_real_model_and_checks_failed_closed_budget(self):
        source = (ROOT / "scripts" / "release-center-token-budget-acceptance.py").read_text(encoding="utf-8")
        for expected in (
            "LLM_ENDPOINT",
            "LLM_MODEL",
            "AGENT_REVIEW_MAX_TOKEN_BUDGET",
            "token_budget_exceeded",
            "tokens_used",
            "max_token_budget",
            "manual_exception",
            "agent_run_id",
            "review-reports",
            "token_budget_exceeded",
            "schema_version",
        ):
            self.assertIn(expected, source)

    def test_sanitize_removes_credentials_and_preserves_budget_fields(self):
        driver = load_driver()
        payload = driver.sanitize(
            {
                "token": "secret",
                "run": {
                    "tokens_used": 45,
                    "max_token_budget": 32,
                    "error": "token_budget_exceeded",
                },
            }
        )
        self.assertNotIn("token", payload)
        self.assertEqual(payload["run"]["tokens_used"], 45)
        self.assertEqual(payload["run"]["max_token_budget"], 32)

    def test_shell_requires_external_model_configuration_and_retains_report(self):
        source = (ROOT / "scripts" / "release-center-token-budget-acceptance.sh").read_text(encoding="utf-8")
        for expected in (
            'LLM_ENDPOINT',
            'LLM_MODEL',
            'AGENT_PLANNER_TYPE="auto"',
            'AGENT_REVIEW_MAX_TOKEN_BUDGET',
            "release-center-token-budget-acceptance.py",
            "GOVERNANCE_REPORT_DIR",
            "docker compose down -v --remove-orphans",
        ):
            self.assertIn(expected, source)


if __name__ == "__main__":
    unittest.main()
