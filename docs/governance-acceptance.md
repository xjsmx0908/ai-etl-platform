# Governance Acceptance Matrix

This matrix closes the P2.4 governance acceptance gate through authenticated
public HTTP observations against an isolated full stack. PostgreSQL, Kafka,
Qdrant, Elasticsearch, and MinIO are real containers; only model inference is
deterministic. Docker Compose controls faults, but database queries are never
used as evidence that user-visible behavior succeeded.

| Scenario | Expected behavior | Public evidence |
| --- | --- | --- |
| Independent approval | A managed document remains invisible until a different administrator approves its exact version and generation. | Agent run/approval and query HTTP responses recorded by `scripts/governance-acceptance.sh`. |
| Replacement continuity | An admitted and indexed replacement cannot displace the last approved release while it awaits review. | Retrieval-only query still returns the old release marker. |
| Approved cutover | Approval atomically changes query authority to the reviewed replacement. | Retrieval-only query returns the new marker and excludes the old marker. |
| Recoverable deletion | HTTP `202` immediately removes query authority; physical cleanup retries and the registry disappears only after all dependencies recover. | Delete, query, metrics, document-detail, and audit HTTP responses. |
| Dependency interruption | Accepted ingestion survives Kafka/API/worker interruption; deletion survives unavailable Qdrant, Elasticsearch, and MinIO. | Task completion and deletion recovery observed through public HTTP after Compose restarts. |
| Audit correlation | Publication, deletion acceptance, and deletion completion remain attributable and share the deletion job correlation. | Tenant-scoped `/v1/audit` responses contain the document and deletion job identities. |

Run the explicit full-stack gate from the repository root:

```bash
bash scripts/governance-acceptance.sh
```

Set `GOVERNANCE_REPORT_DIR` to retain results elsewhere. The command writes a
timestamped JSON report with scenario status, duration, bounded public
observations, and diagnostics. Authentication tokens, passwords, cookies,
authorization headers, and configured secrets are never persisted. The stack
uses its own Compose project and volumes and is removed on exit unless
`GOVERNANCE_KEEP_SERVICES=1` is explicitly set for diagnosis.
`GOVERNANCE_BUILD=0` may reuse already-built local images during harness
development; retained release evidence should use the default clean rebuild.

This acceptance does not deploy production, enable generation retention, or
substitute deterministic model output for the signed business Gold evaluation
required by ADR 0010.
