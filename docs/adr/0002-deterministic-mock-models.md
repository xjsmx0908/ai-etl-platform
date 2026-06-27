# ADR 0002: Deterministic mock model endpoints for CI

## Status
Accepted

## Context

CI needs stable retrieval and smoke checks. Time-based or random mock outputs make regressions noisy.

## Decision

Mock embedding and mock chat responses are derived deterministically from request content.

## Consequences

- Eval and smoke tests are repeatable.
- Retrieval metrics are comparable across runs.
- The mock server does not represent real model quality; it only validates integration paths.
