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
- emits JSON and Markdown reports under `docs/evals/reports/`.
