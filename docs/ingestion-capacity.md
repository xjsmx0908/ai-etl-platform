# Ingestion Capacity

`scripts/ingestion-capacity.py` measures accepted-to-ready ingestion on a
running Query API. It is a hardware capacity envelope for parse / embed /
store, **not the L1 query gate** and **not an ADR 0010 production SLO**.

Do not reuse `scripts/load-test.py` query profiles for this. Those budgets
were calibrated on an isolated mock stack. Real CPU `bge-m3` embedding is
the ingestion bottleneck and will dominate accepted-to-ready time.

The script:

- uploads fixtures serially (never concurrent, never `/v1/query`)
- polls `/v1/tasks/{doc_id}` and records stage transitions
- writes JSON and Markdown reports under `docs/evals/reports/`
- does not start Compose or a second stack

## Run

Short text on the current demo stack:

```bash
python3 scripts/ingestion-capacity.py --profile short-text --username USER --password PASS
```

Typical multi-chunk text:

```bash
python3 scripts/ingestion-capacity.py --profile typical-doc --timeout-sec 3600
```

Existing PDF / OCR file:

```bash
python3 scripts/ingestion-capacity.py \
  --fixture-file path/to/scan.pdf \
  --timeout-sec 14400
```

Optional engineering thresholds fail the process with exit `1`. Setup errors
exit `2`. Default is report-only besides requiring a successful ingest.

Keep `EMBED_CONCURRENCY=1` on CPU. Raising it queues Ollama requests into
`EMBED_TIMEOUT`. This host must not start a second Compose project while the
demo stack is running.

## What the numbers mean

| Metric | Meaning |
| --- | --- |
| accepted-to-ready | upload accepted until task `completed` |
| stage p50 | sampled `/v1/tasks` stage transitions (`parsing` / `ocr` / `embedding`) |
| chunks/min | completed chunks divided by successful ready time |

Stage samples can miss short phases. Worker `ai_etl_embed_duration_seconds`
is the finer embed histogram when metrics are reachable.

Historical CPU `bge-m3` evidence: 25-page PDF → 78 chunks → 209s, longest
single chunk 6.76s. Treat that as a capacity hint, not a gate.

This machine's first serial envelope against the live demo stack (CPU
`bge-m3`, 2026-09-10):

| Fixture | Chunks | accepted-to-ready | Notes |
| --- | --- | --- | --- |
| `short-text` | 2 | 11.0s | Tiny txt; embedding often folded into `parsing` samples |
| `typical-doc` | 12 | 24.0s | ~2s/chunk; sampled `parsing` 20s includes queue wait |

Short generated text is not a scanned-PDF budget. Use `--fixture-file` for OCR
books. Do not raise `EMBED_CONCURRENCY` on CPU.

## Real-model quality is a different track

Retrieval / answer quality uses:

```bash
python3 scripts/run-evals.py --real-models --embed-dim 1024
```

That command needs an isolated eval Compose project and a dedicated Qdrant
collection. Do not start it beside the demo stack on this machine. Do not
feed those quality metrics into L1 p95 thresholds.

Release-center real-model scenarios exercise the review agent, not RAG
ingestion throughput.

Real full `/v1/query` latency with a live LLM is an optional observation:
5–10 serial requests after ingest, reported separately, never 40 concurrent
generations on the demo stack.

## Local embedding keep-alive

Idle Ollama `bge-m3` on this CPU host previously paid a cold load of about 8s
on the first chunk. Worker and Query API now send `keep_alive=24h` and warm
the model at process start.

After rebuilding the live demo stack on 2026-09-10:

| Path | Before keep-alive | After keep-alive |
| --- | --- | --- |
| `/v1/upload` HTTP | already `202` | `202` in 28ms |
| short-text embed | 7.9s (`doc-1789008297108283450`) | 487ms (`doc-1789013217876229964`) |
| short-text accepted-to-ready | 8.2s–11s | 2.0s |
| query retrieval | folded into 4–10s total | 350–430ms |
| query TTFT / total | first token waited for full answer | progress at 2ms; TTFT 3.1–4.9s, almost all remote LLM |

Keep `EMBED_CONCURRENCY=1` on CPU. Keep-alive does not change per-chunk
transformer cost on large PDFs.
