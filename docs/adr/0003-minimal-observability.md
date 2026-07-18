# ADR 0003: Minimal alerting surface

## Status
Accepted

## Context

We only need the observability that helps a small team detect real incidents.

## Decision

Add a focused Prometheus rule set for:
- HTTP 5xx rate
- p95 latency
- DLQ growth
- circuit breaker open state
- Agent failures, lifecycle timeouts, and p95 duration
- LLM consecutive failures, rolling error rate, and p95 latency

Use Alertmanager for grouping, inhibition, retry, and routing. Keep enterprise
webhook credentials in Docker secrets and translate Alertmanager payloads in a
small internal adapter. Provision one read-only Grafana dashboard from source
control. Do not expose a Token/s metric until a real SSE endpoint exists.

## Consequences

- Faster incident triage.
- The alert surface stays intentionally small and actionable.
- Alert delivery is testable without storing third-party secrets in Git.
- Token generation speed remains out of scope while Query API is non-streaming.
