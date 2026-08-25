# ADR 0004: Separate Redis instances for evictable cache and durable state

## Status
Accepted (2026-08-08)

## Context

A single Redis instance backed seven kinds of data with `--maxmemory-policy allkeys-lru`:

| Key prefix | Nature |
| --- | --- |
| `retrieval:cache` | Semantic retrieval cache — evictable by design |
| `agent:run` | Durable Agent run state |
| `agent:approval` | Approval audit records |
| `agent:lock` | Distributed lock owner + monotonic fencing token |
| `taskstatus` | Task lifecycle status |
| `idempotency` | Upload idempotency keys |
| ES retry ZSET | Eventual-consistency retry queue for Elasticsearch |

`allkeys-lru` evicts *any* key under memory pressure, with no regard for semantics. That
turns two correctness guarantees into probabilistic ones:

1. **Fencing tokens.** `agent.Orchestrator` rejects stale writes via
   `lease.FencingToken > run.FencingToken` (`internal/agent/orchestrator.go`). The token is
   documented to grow monotonically across release and reacquire. If the token key is
   evicted, it restarts from zero and a stale owner's write is no longer rejected — silent
   data corruption in a module whose entire design premise is durability.
2. **Approval audit.** Records of who approved which side-effecting tool call are
   compliance artifacts. Eviction destroys them with no trace.

The ES retry queue has the same problem in a different shape: an evicted retry job means a
chunk stays in Qdrant but never reaches Elasticsearch, and nothing detects the divergence.

The cache, by contrast, *should* be evicted. Losing a cached result costs one extra
retrieval.

## Decision

Split into two instances with opposite eviction semantics.

- **`redis-cache`** — `maxmemory 256mb`, `allkeys-lru`. Holds only `retrieval:cache`.
- **`redis-state`** — `maxmemory 512mb`, `noeviction`, `appendonly yes`,
  `appendfsync everysec`. Holds everything else.

Configuration resolves as `REDIS_CACHE_*` / `REDIS_STATE_*` first, falling back to shared
`REDIS_*`. The fallback keeps single-instance local development working and avoids a
breaking change for existing `.env` files.

Production validation (`Config.Validate`) rejects:
- `REDIS_STATE_ADDR` or `REDIS_CACHE_ADDR` left at the `localhost:6379` default
- `REDIS_STATE_ADDR`/`DB` equal to `REDIS_CACHE_ADDR`/`DB`

The last check is the one that matters: it makes the mistake this ADR fixes unrepresentable
in production, rather than relying on operators reading documentation.

## Consequences

- Durable state can no longer be evicted under memory pressure. `noeviction` means writes
  fail loudly with an OOM error instead of state disappearing silently — the correct
  trade-off for data whose loss breaks correctness.
- AOF with `everysec` bounds crash loss to roughly one second. Full `appendfsync always`
  was not chosen: the fencing-token path is not hot enough to justify a per-write fsync,
  and one second of exposure is acceptable for a dev/staging profile.
- Two instances instead of one: slightly more memory overhead and one more container.
  Accepted — the alternative is a correctness bug.
- `REDIS_STATE_HOST_PORT` defaults to 6380 to avoid colliding with the cache instance on 6379.
- Local development can still point both at one instance via `REDIS_ADDR`. This is
  deliberate: the guardrail applies where it matters (production), without adding friction
  to `docker compose up`.

## Alternatives considered

**Separate logical DBs on one instance.** Rejected: `maxmemory-policy` is instance-wide in
Redis, so `allkeys-lru` would still evict state keys in DB 1. Logical separation does not
solve an eviction-policy problem.

**Keep one instance, switch to `noeviction`.** Rejected: the cache then fills memory and
blocks state writes. Cache growth is unbounded by nature; that is exactly why it needs LRU.

**Move all state to PostgreSQL.** Still deferred for Agent runs, locks, fencing tokens, and
active recovery state. P2.1 moved durable approval records to PostgreSQL and removed their
former 24-hour Redis TTL; Redis State continues to own the remaining state-machine data.
