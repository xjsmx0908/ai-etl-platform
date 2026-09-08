import importlib.util
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


def load_driver():
    path = ROOT / "scripts" / "release-center-real-model-scenarios-acceptance.py"
    spec = importlib.util.spec_from_file_location("release_center_real_model_scenarios_acceptance", path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


class ReleaseCenterRealModelScenariosAcceptanceTests(unittest.TestCase):
    def test_driver_covers_four_real_model_document_classes(self):
        source = (ROOT / "scripts" / "release-center-real-model-scenarios-acceptance.py").read_text(encoding="utf-8")
        for expected in (
            "ordinary_managed_document",
            "sensitive_information",
            "prompt_injection",
            "insufficient_evidence",
            "LLM_ENDPOINT",
            "LLM_MODEL",
            "AGENT_PLANNER_TYPE",
            "sensitive_data_detected",
            "prompt_injection_detected",
            "human_handoff",
            "latency_ms",
            "tokens_used",
            "get_review_context",
            "scan_sensitive_data",
            "review-reports",
            "schema_version",
        ):
            self.assertIn(expected, source)

    def test_sanitize_removes_credentials_and_keeps_audit_fields(self):
        driver = load_driver()
        payload = driver.sanitize(
            {
                "token": "secret",
                "scenarios": [
                    {
                        "name": "ordinary_managed_document",
                        "tokens_used": 88,
                        "human_handoff": False,
                        "latency_ms": 1200,
                    }
                ],
            }
        )
        self.assertNotIn("token", payload)
        self.assertEqual(payload["scenarios"][0]["tokens_used"], 88)
        self.assertFalse(payload["scenarios"][0]["human_handoff"])

    def test_shell_requires_real_model_and_retains_report(self):
        source = (ROOT / "scripts" / "release-center-real-model-scenarios-acceptance.sh").read_text(encoding="utf-8")
        for expected in (
            "LLM_ENDPOINT",
            "LLM_MODEL",
            'AGENT_PLANNER_TYPE="auto"',
            "release-center-real-model-scenarios-acceptance.py",
            "GOVERNANCE_REPORT_DIR",
            "docker compose down -v --remove-orphans",
        ):
            self.assertIn(expected, source)
        gitignore = (ROOT / ".gitignore").read_text(encoding="utf-8")
        self.assertIn("/artifacts/release-center-real-model-scenarios-acceptance/", gitignore)


if __name__ == "__main__":
    unittest.main()
