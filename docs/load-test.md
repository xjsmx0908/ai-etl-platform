# Load Test

`scripts/load-test.py` is the query performance gate. Without thresholds it only
reports; with `--profile` or explicit budgets it fails when the engineering
regression envelope is missed.

Exploratory run (no gate):

```bash
python3 scripts/load-test.py --requests 40 --concurrency 5
```

Engineering gate, mock stack, cold retrieval:

```bash
python3 scripts/load-test.py --profile cold-retrieval --requests 40 --concurrency 5
```

Cached JSON/e2e path:

```bash
python3 scripts/load-test.py --profile cached-e2e --requests 40 --concurrency 5
```

Prefer `--username/--password` or `--token` against a running Query API. Isolated
eval/smoke Compose already sets `RETRIEVAL_DIAGNOSTICS_ENABLED=true`, which is
required for `--retrieval-only`. Keep this on the default `user-uploads` space;
do not fold publication-center approval into the query gate.

The script:
- starts/stops a local deterministic mock model server by default,
- seeds one document and records upload-to-task-completed time when `/v1/tasks`
  is available,
- waits until the seed is the top query hit,
- runs concurrent `/v1/query` requests,
- emits JSON and Markdown reports under `docs/evals/reports/` by default,
- exits `1` when a configured threshold is missed, `2` on setup errors.

## Profiles

These are engineering budgets for a local/mock stack, not production SLOs.

| Profile | Path | success / top-hit | p95 | p99 | min qps |
| --- | --- | --- | --- | --- | --- |
| `cold-retrieval` | `retrieval_only`, unique questions, cache bypass via distinct queries | 100% | ≤ 800ms | ≤ 1500ms | ≥ 3 |
| `cached-e2e` | repeated full `/v1/query` including mock generation | 100% | ≤ 400ms | ≤ 800ms | ≥ 5 |

Explicit `--min-*` / `--max-*` flags override the profile. Sample size 40 is too
small to treat p99 as a hard PR signal; nightly/local gates should fail on p95.

Useful flags:

- `--scenario`: labels the report. Profiles also set this when it is still `default`.
- `--retrieval-only`: skip answer generation. Honored only when diagnostics are enabled.
- `--unique-questions`: append `[loadtest-N]` so semantic cache cannot collapse the run.
- `--warmup`: untimed queries before the measured batch.
- `--noise-docs`: uploads extra distractor documents so retrieval/rerank has multiple candidates.
- `--metadata-json`: attaches business schema metadata to the seed document, for example `{"customer_ref":"lt-29q-000001"}`.
- `--report-dir`: writes reports outside the ignored eval report directory when needed.

Example schema-exact run:

```bash
REF="lt-29q-000001"
python3 scripts/load-test.py \
  --scenario module2-schema-exact-rerank-on \
  --requests 40 \
  --concurrency 5 \
  --noise-docs 3 \
  --metadata-json "{\"customer_ref\":\"$REF\"}" \
  --question "请解释客户参考号 $REF 的处理说明"
```

The report includes success rate, hit rate, top-hit rate, cache-hit rate,
throughput, p50/p95/p99, optional ingestion-ready latency, and the gate verdict.
This is not the ADR 0010 production evidence pack.
