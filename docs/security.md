# Security Notes

This document records the security posture of the platform: what is hardened and
what is deliberately deferred. It is not an exhaustive threat model.

The executable hardening sequence is
[`security-hardening-plan.md`](security-hardening-plan.md). P-SEC-0 through
P-SEC-5 are implemented in the default Compose stack. That does not make the
demo stack a public bastion: production still needs host Nginx, `COOKIE_SECURE=true`,
and real secrets rather than `secrets/examples/`.

## What is hardened

- Default Compose publishes host ports on `127.0.0.1` via `COMPOSE_BIND`. Parser,
  Grafana, Alertmanager, and reranker stay loopback-only even if `COMPOSE_BIND` is
  widened. Lab overlay can publish only Web/Query API to `0.0.0.0`. Production
  overlay unpublishes Query API and data-plane ports; public traffic is Nginx
  `:443` -> `127.0.0.1:3100`.
- Redis requires the `redis_password` secret. MinIO credentials come from
  `s3_access_key` / `s3_secret_key` files, not `minioadmin`. Qdrant uses
  `store_api_key`. Elasticsearch remains unauthenticated on the Docker network
  by design.
- `/v1/auth/login` is rate-limited and lockable before bcrypt. JSON bodies are
  capped. `/metrics` and `/version` require `METRICS_TOKEN` outside development.
  `/healthz` and `/readyz` stay anonymous and return no config.
- Authenticated query/upload/agent runs have per-tenant concurrency caps. Parser
  has a 2G memory limit. Question length and query JSON size are bounded.
- Production validation rejects weak JWT/bootstrap secrets, empty Redis/Qdrant
  keys, default MinIO keys, wildcard CORS, disabled login rate limits, and a
  missing metrics token (the last two also apply outside development as noted
  in `ValidateAPI`).
- Upload size is capped; multipart memory spills to disk.
- JWT or platform session is required for upload, query, agent, and admin paths.
- Parser internal token is required outside development.
- Tenant isolation is enforced on query, document delete, and audit listing.
- Agent tools are allowlisted; review documents are treated as untrusted data.
- Publish-time deterministic scans can block prompt-injection and sensitive-data
  patterns. Generated answers that match the same secret/id/phone rules are
  replaced with a refusal and audited.
- HTTP timeouts, per-tenant RPS limiter, and LLM/embed circuit breakers exist.
- Production Nginx applies login `limit_req`, `nosniff`, `Referrer-Policy`, and
  `frame-ancestors 'none'`.

## Current residual risk

- Prompt-injection defense is heuristic plus system prompt, not a hard sandbox.
  Malicious documents can still try to steer the model inside a tenant's visible
  corpus, especially on unpublished preview paths.
- Elasticsearch has `xpack.security.enabled=false`; Kafka is PLAINTEXT. Do not
  publish those ports off-loopback.
- `secrets/examples/` values are placeholders. Changing MinIO credentials on an
  existing volume requires recreating `minio_data`.
- Grafana and Kafka UI remain internal/loopback tools, not public apps.

## What is intentionally not added

- Full enterprise IAM (tracked as P2.5, not this hardening plan).
- Service mesh.
- Kubernetes-native policy stack.
- Centralized compliance tooling.
- Kafka SASL or Elasticsearch xpack in the demo default stack.
