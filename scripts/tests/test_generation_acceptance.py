import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


class GenerationAcceptanceContractTests(unittest.TestCase):
    def test_go_ci_runs_generation_lifecycle_against_real_postgres(self):
        workflow = (ROOT / ".github" / "workflows" / "ci.yml").read_text(
            encoding="utf-8"
        )

        self.assertIn("INDEX_MANIFEST_TEST_DSN", workflow)
        self.assertIn("EXTERNAL_IDENTITY_TEST_DSN", workflow)
        self.assertIn("postgres:16-alpine", workflow)

    def test_acceptance_matrix_maps_every_required_scenario_to_automated_evidence(self):
        matrix = (ROOT / "docs" / "index-generation-acceptance.md").read_text(
            encoding="utf-8"
        )

        for scenario in ("Crash replay", "Reindex", "Rollback", "Garbage collection"):
            self.assertIn(f"| {scenario} |", matrix)
        self.assertIn("TestPostgresRollbackAndRetentionLifecycle", matrix)
        self.assertIn("TestBuilderGenerationIsStableForRedeliveryAndChangesWithConfiguration", matrix)


if __name__ == "__main__":
    unittest.main()
