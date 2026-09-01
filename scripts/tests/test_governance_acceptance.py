import importlib.util
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


def load_driver():
    path = ROOT / "scripts" / "governance-acceptance.py"
    spec = importlib.util.spec_from_file_location("governance_acceptance", path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


class GovernanceAcceptanceContractTests(unittest.TestCase):
    def test_matrix_maps_governance_and_recovery_scenarios_to_public_evidence(self):
        matrix = (ROOT / "docs" / "governance-acceptance.md").read_text(
            encoding="utf-8"
        )

        for scenario in (
            "Independent approval",
            "Replacement continuity",
            "Approved cutover",
            "Recoverable deletion",
            "Dependency interruption",
            "Audit correlation",
        ):
            self.assertIn(f"| {scenario} |", matrix)
        self.assertIn("scripts/governance-acceptance.sh", matrix)
        self.assertIn("public HTTP", matrix)

    def test_runner_contract_is_isolated_and_retains_secret_free_json(self):
        runner = (ROOT / "scripts" / "governance-acceptance.sh").read_text(
            encoding="utf-8"
        )
        driver = (ROOT / "scripts" / "governance-acceptance.py").read_text(
            encoding="utf-8"
        )

        self.assertIn("COMPOSE_PROJECT_NAME", runner)
        self.assertIn("docker compose down -v --remove-orphans", runner)
        self.assertIn("GOVERNANCE_REPORT_DIR", runner)
        self.assertIn("--report", runner)
        self.assertIn(
            "/artifacts/governance-acceptance/",
            (ROOT / ".gitignore").read_text(encoding="utf-8"),
        )
        self.assertIn('"schema_version"', driver)
        self.assertIn('"scenarios"', driver)
        self.assertIn("Authorization", driver)

    def test_full_stack_uses_ephemeral_minio_storage(self):
        compose = (ROOT / "docker-compose.governance.yml").read_text(
            encoding="utf-8"
        )

        self.assertIn("minio:", compose)
        self.assertIn("volumes: !reset []", compose)
        self.assertIn("tmpfs:", compose)
        self.assertIn("WORKER_METRICS_HOST_PORT", compose)
        self.assertIn("docker-compose.governance.yml", (
            ROOT / "scripts" / "governance-acceptance.sh"
        ).read_text(encoding="utf-8"))

    def test_report_sanitizer_removes_nested_credentials_and_bearer_values(self):
        driver = load_driver()

        sanitized = driver.sanitize(
            {
                "status": "failed",
                "password": "not-for-a-report",
                "nested": {
                    "token": "jwt-value",
                    "Authorization": "Bearer secret-token-value",
                    "message": "request used Bearer another-secret-value",
                },
            }
        )

        self.assertNotIn("password", sanitized)
        self.assertNotIn("token", sanitized["nested"])
        self.assertNotIn("Authorization", sanitized["nested"])
        self.assertEqual(
            sanitized["nested"]["message"], "request used Bearer [REDACTED]"
        )

    def test_http_decoder_preserves_plain_text_failure_diagnostics(self):
        driver = load_driver()

        self.assertEqual(
            driver.decode_http_payload(b"service temporarily unavailable\n"),
            "service temporarily unavailable\n",
        )

    def test_managed_upload_encodes_required_governance_fields(self):
        driver = load_driver()

        body = driver.encode_multipart(
            "fixed-boundary",
            {
                "doc_status": "active",
                "effective_date": "2026-08-29",
                "owner": "Enterprise Knowledge Office",
            },
            "policy.txt",
            "controlled policy",
        ).decode("utf-8")

        self.assertIn('name="doc_status"\r\n\r\nactive', body)
        self.assertIn('name="effective_date"\r\n\r\n2026-08-29', body)
        self.assertIn(
            'name="owner"\r\n\r\nEnterprise Knowledge Office', body
        )

    def test_metric_value_reads_exact_bounded_label_series(self):
        driver = load_driver()

        value = driver.metric_value(
            '# TYPE ai_etl_deletion_outcomes_total counter\n'
            'ai_etl_deletion_outcomes_total{outcome="failed"} 2\n',
            'ai_etl_deletion_outcomes_total{outcome="failed"}',
        )

        self.assertEqual(value, 2.0)

    def test_deletion_recovery_reinitializes_ephemeral_object_store(self):
        driver = load_driver()
        runner = driver.Runner.__new__(driver.Runner)
        compose_calls = []
        runner.compose = lambda *args: compose_calls.append(args)
        runner.client = type(
            "HealthyClient",
            (),
            {"request": lambda self, *args, **kwargs: (200, {})},
        )()
        runner.wait_until = lambda _description, predicate: predicate()

        runner.restore_deletion_dependencies()

        self.assertEqual(
            compose_calls,
            [
                ("start", "qdrant", "elasticsearch", "minio"),
                ("restart", "query-api"),
            ],
        )

    def test_existing_e2e_smoke_uses_recoverable_delete_contract(self):
        smoke = (ROOT / "scripts" / "e2e-smoke.sh").read_text(
            encoding="utf-8"
        )

        self.assertIn('if [[ "${DELETE_STATUS}" != "202" ]]', smoke)
        self.assertIn("waiting for asynchronous document cleanup", smoke)
        self.assertIn('if [[ "${GET_STATUS}" == "404" ]]', smoke)
        self.assertNotIn('if [[ "${DELETE_STATUS}" != "204" ]]', smoke)

    def test_existing_e2e_smoke_uses_independent_publication_governance(self):
        smoke = (ROOT / "scripts" / "e2e-smoke.sh").read_text(
            encoding="utf-8"
        )

        self.assertNotIn('{"publication_status":"published"}', smoke)
        self.assertIn("/v1/knowledge-spaces", smoke)
        self.assertIn('-F "knowledge_space_id=${KNOWLEDGE_SPACE_ID}"', smoke)
        self.assertIn('-F "effective_date=${EFFECTIVE_DATE}"', smoke)
        self.assertIn('-F "owner=${DOCUMENT_OWNER}"', smoke)
        self.assertIn("/v1/users", smoke)
        self.assertIn("/v1/agent/runs", smoke)
        self.assertIn("/v1/agent/runs/${RUN_ID}/approvals", smoke)
        self.assertIn("/v1/agent/runs/${RUN_ID}/approve", smoke)
        self.assertIn('Authorization: Bearer ${REVIEWER_TOKEN}', smoke)

    def test_web_search_proxy_preserves_explicit_knowledge_space(self):
        route = (
            ROOT / "web" / "app" / "api" / "documents" / "search" / "route.ts"
        ).read_text(encoding="utf-8")

        self.assertIn(
            'req.nextUrl.searchParams.get("knowledge_space_id")', route
        )
        self.assertIn(
            'upstreamURL.searchParams.set("knowledge_space_id"', route
        )


if __name__ == "__main__":
    unittest.main()
