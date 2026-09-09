# Load Test

Run the lightweight query load test with:

```bash
python3 scripts/load-test.py --requests 40 --concurrency 5
```

The script:
- starts/stops a local deterministic mock model server by default,
- seeds one document,
- waits until it is queryable,
- runs concurrent `/v1/query` requests,
- emits JSON and Markdown reports under `docs/evals/reports/` by default.

Useful module-2 flags:

- `--scenario`: labels the report.
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

The report includes success rate, hit rate, top-hit rate, throughput, and p50/p95/p99 latency.
