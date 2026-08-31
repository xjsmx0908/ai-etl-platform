import copy
import json
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SCHEMA_PATH = ROOT / "docs" / "enterprise-identity-acceptance.schema.json"
TEMPLATE_PATH = ROOT / "docs" / "enterprise-identity-acceptance.template.json"

import jsonschema


class EnterpriseIdentityAcceptanceSchemaTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.schema = json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))
        cls.template = json.loads(TEMPLATE_PATH.read_text(encoding="utf-8"))
        jsonschema.Draft202012Validator.check_schema(cls.schema)
        cls.validator = jsonschema.Draft202012Validator(
            cls.schema, format_checker=jsonschema.FormatChecker()
        )

    def test_pending_public_template_is_valid(self):
        self.validator.validate(self.template)

    def test_approved_requires_complete_bound_evidence(self):
        manifest = approved_manifest(self.template)
        self.validator.validate(manifest)

        invalid_mutations = (
            lambda value: value["provider"].update(product=""),
            lambda value: value["platform"].update(commit=""),
            lambda value: value["approved_policy"].update(connector_id=""),
            lambda value: value["feature_revisions"].update(session=""),
            lambda value: value["scores"].update(oidc_interoperability=None),
            lambda value: value["maturity"].update(p2_5_f_session="implemented"),
            lambda value: value["reconciliation"].update(run_id=""),
            lambda value: value["evidence"].update(bundle_sha256=""),
            lambda value: value["signature"]["signatures"]["security_owner"].update(
                signer_evidence_id=""
            ),
        )
        for mutate in invalid_mutations:
            with self.subTest(mutation=mutate):
                invalid = copy.deepcopy(manifest)
                mutate(invalid)
                with self.assertRaises(jsonschema.ValidationError):
                    self.validator.validate(invalid)

    def test_changing_only_decision_cannot_promote_template(self):
        manifest = copy.deepcopy(self.template)
        manifest["decision"] = "approved"
        with self.assertRaises(jsonschema.ValidationError):
            self.validator.validate(manifest)

    def test_approved_risk_acceptance_requires_identity_owner_and_evidence(self):
        manifest = approved_manifest(self.template)
        manifest["risk_acceptances"] = [
            {
                "risk_id": "",
                "severity": "low",
                "owner_role": "",
                "expires_at": "2026-09-01T00:00:00Z",
                "evidence_id": "",
            }
        ]
        with self.assertRaises(jsonschema.ValidationError):
            self.validator.validate(manifest)

    def test_approved_requires_complete_invalidation_trigger_set(self):
        manifest = approved_manifest(self.template)
        manifest["invalidation_triggers"] = [f"x{index}" for index in range(8)]
        with self.assertRaises(jsonschema.ValidationError):
            self.validator.validate(manifest)

    def test_approved_requires_rfc8785_canonicalization(self):
        manifest = approved_manifest(self.template)
        manifest["signature"]["canonicalization"] = "anything-even-noncanonical"
        with self.assertRaises(jsonschema.ValidationError):
            self.validator.validate(manifest)


def approved_manifest(template):
    value = copy.deepcopy(template)
    digest = "a" * 64
    timestamp = "2026-08-31T00:00:00Z"
    value.update(
        decision="approved",
        acceptance_id="acceptance-1",
        provider_profile_revision="provider-profile-1",
        policy_revision="policy-1",
        rollback_rehearsed_at=timestamp,
        rollback_owner_role="operations-owner",
        approved_at=timestamp,
        expires_at="2026-11-29T00:00:00Z",
    )
    for key in value["feature_revisions"]:
        value["feature_revisions"][key] = f"{key}-1"
    for key in value["maturity"]:
        value["maturity"][key] = "staging_passed"
    value["provider"].update(
        product="provider",
        version_or_sku="sku",
        private_tenant_or_realm_evidence_id="private:tenant-1",
        oidc_issuer="https://idp.example.test/tenant",
        oidc_profile_sha256=digest,
        scim_users_profile_sha256=digest,
        scim_groups_profile_sha256=digest,
        workload_profile_sha256=digest,
        sanitized_configuration_sha256=digest,
    )
    value["platform"].update(
        commit="b" * 40,
        query_api_image_digest="sha256:" + digest,
        web_image_digest="sha256:" + digest,
        parser_image_digest="sha256:" + digest,
        reranker_image_digest="sha256:" + digest,
        worker_image_digest="sha256:" + digest,
        database_migration_revision="0025",
    )
    for key in value["scores"]:
        value["scores"][key] = 4
    value["approved_policy"].update(
        connector_id="connector-1",
        internal_tenant_id="tenant-1",
        stored_profile=["userName"],
        group_authority="scim-groups",
        group_semantics="direct",
        workload_trust_domain="spiffe://staging.example.test",
        session_policy_revision="session-1",
        emergency_policy_revision="emergency-1",
        deactivation_sla_seconds=300,
        reconciliation_interval_seconds=3600,
        reconciliation_max_staleness_seconds=7200,
        group_max_staleness_seconds=7200,
        tombstone_retention_days=365,
        credential_rotation_days=90,
        credential_overlap_seconds=3600,
    )
    for result in value["acceptance_results"].values():
        result.update(
            outcome="pass",
            evidence_id="private:result",
            sha256=digest,
            started_at=timestamp,
            completed_at=timestamp,
        )
    value["reconciliation"].update(
        run_id="run-1",
        source_watermark_sha256=digest,
        snapshot_digest_sha256=digest,
        plan_digest_sha256=digest,
        snapshot_count=1,
        drift_count=0,
        applied_count=0,
        quarantined_count=0,
        last_attempted_at=timestamp,
        last_completed_at=timestamp,
    )
    for key in value["sla_summary"]:
        value["sla_summary"][key] = 1
    value["evidence"].update(
        bundle_sha256=digest,
        decision_matrix_evidence_id="private:matrix",
        decision_matrix_sha256=digest,
        correlation_index_evidence_id="private:correlations",
        cleanup_evidence_id="private:cleanup",
        rollback_evidence_id="private:rollback",
    )
    value["cleanup"].update(
        outcome="pass",
        test_identities_removed=True,
        test_groups_removed=True,
        test_workloads_revoked=True,
        test_credentials_revoked=True,
        completed_at=timestamp,
    )
    for key in value["approvals"]:
        value["approvals"][key] = f"private:{key}"
    value["signature"].update(
        canonicalization="RFC8785",
        payload_sha256=digest,
    )
    for signer in value["signature"]["signatures"].values():
        signer.update(
            mechanism="approved-detached-signature",
            signer_evidence_id="private:signer",
            detached_signature_sha256=digest,
            signed_at=timestamp,
        )
    return value


if __name__ == "__main__":
    unittest.main()
