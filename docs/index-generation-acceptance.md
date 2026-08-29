# Index Generation Acceptance Matrix

This matrix closes the P2.3 generation acceptance gate. CI uses deterministic
module tests plus a disposable PostgreSQL 16 service; external search systems
remain isolated behind their public projection interfaces. Production rollout
still requires an approved retention window and the deployment gates in ADR
0010.

| Scenario | Expected behavior | Automated evidence |
| --- | --- | --- |
| Crash replay | Redelivery preserves the deterministic generation and its original expected-active CAS; a partial dual-backend write never activates. | `TestBuilderGenerationIsStableForRedeliveryAndChangesWithConfiguration`, `TestBuilderRedeliveryCannotAdoptAnewerActiveGeneration`, `TestBuilderDoesNotPublishWhenElasticsearchWriteFails` |
| Reindex | A build-definition change creates a new generation; the old active generation remains readable until dual verification and atomic activation complete. Active repair also requires fresh observations from both projections. | `TestBuilderPersistsBeforeProjectionAndPublishesOnlyAfterDualVerification`, `TestBuilderActiveRepairCompletesOnlyAfterDualVerification`, `TestPostgresConcurrentActivation` |
| Rollback | Only an explicitly named, unexpired retired generation can replace the caller's expected active generation. Fresh Qdrant and Elasticsearch identity digests must match. | `TestRollbackerVerifiesTargetBeforeAtomicPromotion`, `TestRollbackerRejectsDivergedTargetWithoutPromotion`, `TestPostgresRollbackAndRetentionLifecycle` |
| Garbage collection | Only expired retired generations are claimed. Exact-generation projection deletion is idempotent, partial progress survives retry, and the manifest is removed last. | `TestRetentionCollectorPersistsPartialProgressForRetry`, `TestRetentionCollectorRetriesOnlyIncompleteProjection`, `TestPostgresRollbackAndRetentionLifecycle` |

Run the focused local gate with:

```bash
INDEX_MANIFEST_TEST_DSN='postgres://test:test@localhost:5432/test?sslmode=disable' \
  go test ./internal/indexmanifest -count=1
```

Failure-path coverage also includes equal-count identity mismatch, stale claims,
repair exhaustion, Qdrant/Elasticsearch deletion errors, and concurrent
activation. Prometheus alerts cover stalled builds, failed manifests, sustained
backend divergence, exhausted repair, and persistent retention cleanup errors.
