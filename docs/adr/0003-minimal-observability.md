# ADR 0003: Minimal alerting surface

## Status
Accepted

## Context

We only need the observability that helps a small team detect real incidents.

## Decision

Add a small Prometheus rule set for:
- HTTP 5xx rate
- p95 latency
- DLQ growth
- circuit breaker open state

## Consequences

- Faster incident triage.
- No large alert taxonomy or enterprise-wide SLO framework.
