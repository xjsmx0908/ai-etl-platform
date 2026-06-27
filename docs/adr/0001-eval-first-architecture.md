# ADR 0001: Eval-first, keep the stack lean

## Status
Accepted

## Context

This project is used as a learning and portfolio project for an AI application engineer role. The goal is to demonstrate production-oriented engineering habits without turning the system into a heavy enterprise platform.

## Decision

We prioritize the following upgrades in order:
1. Deterministic evaluation loop
2. Minimal observability
3. Targeted security hardening
4. Concise documentation
5. Lightweight load testing

We intentionally avoid introducing Kubernetes, multi-cluster deployment, or local model hosting.

## Consequences

- CI can detect retrieval regressions quickly.
- The current single-server setup remains runnable.
- The system demonstrates production practices without excessive operational complexity.
