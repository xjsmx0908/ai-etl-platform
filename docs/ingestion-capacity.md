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
generations on the demo stack. Measured — see
[Real `/v1/query` latency (P-CAP-3)](#real-v1query-latency-p-cap-3) below.

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

The two `query` rows were taken on 2026-09-10, **before the optional reranker was
running on this stack**: `reranker-service` is profile-gated
(`profiles: [rerank]` in `docker-compose.yml`) and reported 10 days of uptime on
2026-09-23. Read them as the rerank-off configuration; the P-CAP-3 section below
records what the same paths become with reranking on.

Keep `EMBED_CONCURRENCY=1` on CPU. Keep-alive does not change per-chunk
transformer cost on large PDFs.

## Real `/v1/query` latency (P-CAP-3)

Serial, one request at a time, against the live demo stack. A separate track
from the ingest envelope above and from the isolated-stack profiles in
[`load-test.md`](load-test.md). **Not the L1 query gate** and not an ADR 0010
SLO; nothing here writes a threshold.

Instrument: [`scripts/query-latency-observation.py`](../scripts/query-latency-observation.py).

### 口径 — what `total` and `ttft` include

- `total` is request start to the SSE `done` event: retrieval + generation +
  grounding.
- `ttft` is request start to the first non-empty `delta`. The `sources` event is
  emitted before generation begins, so **`ttft` includes retrieval**.
  "Generation time" is `total - ttft`, not `total`.
- `done.ttft` is the platform's own measurement of the same interval. It agrees
  with the client-measured `ttft` to within 0.02s, so nothing is buffered
  between the API and the caller.
- With n rounds, a nearest-rank p95 **is the maximum**. It is not an
  interpolated tail, and with n=8 there is no tail to interpolate.

### The observation

`default` tenant, `px-admin`, 8 distinct questions, `deepseek-v4-flash` via the
configured remote endpoint, CPU `bge-m3`, CPU `bge-reranker-base`,
2026-09-23 02:01–02:02 UTC. Retrieval cache cold — the shape a real user
asking a new question sees:

| # | total | retrieval | LLM first token | rest of generation | answer chars |
| --- | --- | --- | --- | --- | --- |
| 1 | 27.61s | 20.41s | 2.12s | 5.08s | 451 |
| 2 | 27.85s | 21.43s | 2.35s | 4.07s | 389 |
| 3 | 28.01s | 21.80s | 2.45s | 3.76s | 349 |
| 4 | 26.05s | 23.48s | 2.40s | 0.18s | 54 |
| 5 | 29.28s | 23.90s | 2.41s | 2.96s | 209 |
| 6 | 32.97s | 26.17s | 5.08s | 1.72s | 515 |
| 7 | 27.50s | 24.81s | 1.88s | 0.82s | 260 |
| 8 | 27.05s | 23.17s | 2.23s | 1.65s | 344 |
| **p50** | **27.61s** | **23.17s** | **2.35s** | **1.72s** | |
| **p95** | **32.97s** | **26.17s** | **5.08s** | **5.08s** | |

Retrieval is 74–90% of `total` (p50 79%). The model's first token costs
1.9–5.1s, which matches a direct streaming call to the same endpoint with the
same model and a 20 000-character prompt (2.0–3.4s). So the model is not what
makes this slow.

Warm retrieval cache — the same command, the same 8 questions, two minutes
later:

| | total | ttft | retrieval |
| --- | --- | --- | --- |
| p50 | 5.93s | 3.09s | 235ms |
| p95 | 18.65s | 14.44s | 278ms |

Everything except retrieval is configured identically, so the ~22s gap between
the two runs is retrieval. The spread inside the warm run is upstream model
variance — `retrieval_ms` stays inside a 68ms band while `ttft` moves 1.9–14.4s.
The warm run also reproduces the P-CAP-6 row above: a `ttft` of 1.9–4.8s is the
same shape as the recorded 3.1–4.9s.

The semantic cache is why the two runs differ. It is why the observation rotates
its question every round: a repeated question measures the cache, not the
system, and the instrument prints a warning when it sees one.

### Why this does not contradict the P-CAP-6 numbers above

P-CAP-6 recorded retrieval at 350–430ms and TTFT at 3.1–4.9s on 2026-09-10,
**before the optional reranker was running on this stack**. `reranker-service`
is profile-gated (`profiles: [rerank]`) and reported 10 days of uptime on
2026-09-23. With reranking off, retrieval is the ~0.2s embedding call plus the
backend queries, which is where 350–430ms came from. The two sets of rows
describe two configurations, not a regression between them.

### Where the 23s of retrieval goes (P-CAP-7)

All of it is the reranker. The `Retrieval.Rerank` span in Jaeger reports 37–50
candidates and 19.5–27.0s per query across 11 sampled traces — the same range as
`retrieval_ms`. Measured directly, the CPU cross-encoder costs ~380ms per pair
at 388 characters and ~490ms per pair at the 512-character
`RERANKER_MAX_DOCUMENT_CHARS` cap; 49 pairs × 490ms ≈ 24s, which is what the
span reports. Embedding the question costs 0.2s.

#### The output cap and the work budget are two numbers

`internal/retrieval/engine.go` used to compute both from one expression:
`rerankTopK := len(stabilized); if rerankTopK > 20 { rerankTopK = 20 }`. That
value was passed down as `top_n`, while `documents` carried **every** stabilized
candidate. `services/reranker-service/app/reranker.py` scores each document and
`top_n` only trims the returned list, so the cross-encoder scored 37–50 pairs to
return 20 — and a reader of that one expression would reasonably conclude the cap
bounded the cost. It did not.

2026-09-23 separated the two numbers and made the choice measurable:

- `RETRIEVAL_RERANK_INPUT_K` bounds how many candidates are handed to the
  reranker, taken from the head of the fused order. `0` = no cap, which is the
  behaviour before the knob existed and the shipped default.
- `rerank.deepest_used_input_rank` (on the `Retrieval.Rerank` span) reports how
  deep the reranker's own output reaches.
- `retrieval.deepest_selected_input_rank` (on the `RetrievalEngine.Retrieve`
  span) reports how deep the candidates that become context reach. This is the
  number that decides the knob: a candidate deeper than the cap is one the cap
  removes before it is ever scored.

#### The pool tail is load-bearing, so the cap ships at 0

Eight questions on the demo stack, semantic cache flushed before each run so no
answer is served without reranking (2026-09-23):

| run | `RETRIEVAL_RERANK_INPUT_K` | pool sizes | deepest used (reranker output) | deepest selected (context) | rerank step |
| --- | --- | --- | --- | --- | --- |
| no cap | 0 | 50, 44, 50, 50, 38, 34, 50, 49 | 43, 41, 30, 40, 33, 33, 47, 42 (max 47) | **34**, 7, 29, 8, 30, 21, 33, 27 (max 34) | 15.4–24.7s (median 22.7s) |
| capped | 35 | same | max 34 | 34, 7, 29, 8, 30, 21, 33, 27 — unchanged | 14.2–17.7s (median 17.1s) |
| capped | 5 | same | max 4 | 3, 4, 4, 4, 4, 3, 4, 4 | 1.8–2.5s (median 2.2s) |

The row that settles the question is the no-cap `deepest selected` list. A
candidate that fusion ranked **34th of 50** was promoted by the cross-encoder into
the 5 chunks the model reads. The fused order is a weak proxy for relevance: the
tail is not scored and thrown away, it is scored and sometimes promoted. That is
what an earlier reading of this section got wrong: it inferred from the output cap
alone that the tail was wasted, which is the same conflation the knob exists to
remove.

Capping at the measured maximum (`35`) returns **the same sources for all eight
questions** at 75% of the rerank cost, so the knob is not a no-op. It is still not
shipped as a default: the margin is one position (34 against a cap of 35) on eight
questions, and the saving is proportional to the cap rather than free. The `5` row
is the reverse check — it changes the sources for 8/8 questions and drops the
rerank step to 9.7% of the uncapped median, which is what a cap that actually
bounds the work looks like.

The remaining cost lever is the pool itself. `RETRIEVAL_CANDIDATE_K` (50 here)
sets the fusion pool and the rerank cost is linear in it at ~0.5s per pair; this
measurement is what shows that shrinking it trades recall rather than only time.
`RETRIEVAL_RERANK_INPUT_K` must be `0` or at least `RETRIEVAL_FINAL_TOP_K`: a
lower value is refused at startup, because the reranker cannot return more
candidates than it was handed and the context would shrink silently.

Backlog entry and status: P-CAP-7 in [`backlog.md`](backlog.md). The measurement
harness is `measure_rerank_depth.py` (runs on the deployment host; `--compare`
diffs two runs source by source).
