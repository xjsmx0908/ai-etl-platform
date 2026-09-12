# Security Notes

This document records the security posture of the platform: what is hardened and
what is deliberately deferred. It is not an exhaustive threat model.

The executable hardening sequence is
[`security-hardening-plan.md`](security-hardening-plan.md). Until that plan's
five completion criteria are met, do not describe the default Compose stack as
internet-safe.

## What is hardened

- Upload size is capped; multipart memory is limited and spills to disk.
- JWT or platform session is required for upload, query, agent, and admin paths.
- Parser internal token is required outside dev; wildcard CORS is rejected outside dev.
- Production validation rejects weak JWT/bootstrap secrets, default MinIO keys, and `CORS *`.
- Tenant isolation is enforced on query, document delete, and audit listing.
- Agent tools are allowlisted; review documents are treated as untrusted data.
- Publish-time deterministic scans can block prompt-injection and sensitive-data patterns.
- HTTP timeouts, per-tenant RPS limiter, and LLM/embed circuit breakers exist.
- Parser, Grafana, Alertmanager, and reranker host ports already bind to `127.0.0.1`.
- Production Nginx is intended to reverse-proxy only `127.0.0.1:3100`.

## Current gaps (tracked)

- Default Compose still publishes Kafka, Redis, Postgres, Qdrant, Elasticsearch,
  MinIO, Jaeger, Prometheus, Kafka UI, Query API, and Web on `0.0.0.0`.
- Elasticsearch has `xpack.security.enabled=false`; Kafka is PLAINTEXT; Redis
  `command` does not pass `--requirepass` even when a secret file exists.
- `/v1/auth/login` and `/metrics` sit outside the JWT chain with no rate limit
  or metrics token. Login JSON has no `MaxBytesReader`. bcrypt cost 12 makes
  login flooding an easy CPU DoS.
- Authenticated query/upload lack per-tenant concurrency caps beyond 50 rps.
- Prompt-injection defense is heuristic plus system prompt, not a hard sandbox.

## What is intentionally not added

- Full enterprise IAM (tracked as P2.5, not this hardening plan).
- Service mesh.
- Kubernetes-native policy stack.
- Centralized compliance tooling.
- Kafka SASL or Elasticsearch xpack in the demo default stack.
