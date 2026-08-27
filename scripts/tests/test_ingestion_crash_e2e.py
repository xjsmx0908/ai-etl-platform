import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


class IngestionCrashE2EContractTests(unittest.TestCase):
    def test_script_exercises_post_202_recovery_through_public_apis(self):
        script = (ROOT / "scripts" / "e2e-ingestion-crash.sh").read_text(encoding="utf-8")

        self.assertIn('[[ "${UPLOAD_STATUS}" == "202" ]]', script)
        self.assertIn("docker compose stop query-api", script)
        self.assertIn("docker compose stop kafka", script)
        self.assertIn("docker compose up -d kafka", script)
        self.assertIn("/v1/tasks/${DOC_ID}", script)
        self.assertIn("/v1/documents/${DOC_ID}", script)
        self.assertIn("ai_etl_ingestion_outbox_pending", script)

    def test_script_uses_an_isolated_compose_project_and_volume_cleanup(self):
        script = (ROOT / "scripts" / "e2e-ingestion-crash.sh").read_text(encoding="utf-8")

        self.assertIn("ai-etl-ingestion-crash", script)
        self.assertIn("docker compose down -v --remove-orphans", script)

    def test_e2e_workflow_executes_the_crash_recovery_gate(self):
        workflow = (ROOT / ".github" / "workflows" / "e2e-smoke.yml").read_text(
            encoding="utf-8"
        )

        self.assertIn("scripts/e2e-ingestion-crash.sh", workflow)


if __name__ == "__main__":
    unittest.main()
