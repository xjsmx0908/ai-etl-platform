# 模块二学习计划：Hybrid Retrieval & Reranking Engine

目标：能独立设计、解释、调试和验证一个企业级 Hybrid Retrieval + Reranker 检索网关。

合格不是“看懂代码”，而是能讲清楚为什么这样设计，并能用测试、评测和压测证明它没有退化。

## 学习路径

### 第 1 阶段：建立全局地图

建议用时：1 天

你需要先搞清楚一次 `/v1/query` 请求完整经过哪里：

```text
Query API
-> Semantic Cache
-> Query Router
-> Qdrant / Elasticsearch 并发召回
-> Fusion
-> Reranker / Exact Pinning
-> LLM Answer
```

重点文件：

- `services/etl-worker/internal/query/service.go`
- `services/etl-worker/internal/retrieval/engine.go`
- `services/etl-worker/internal/retrieval/router.go`
- `services/etl-worker/internal/retrieval/fusion.go`

合格标准：

- 能画出模块二架构图。
- 能说明 `Query API` 和 `Retrieval Engine` 的边界。
- 能说明为什么不是直接查 Qdrant 后返回。

### 第 2 阶段：理解多路召回和融合排序

建议用时：2 天

重点学习：

- 为什么企业 RAG 不能只用向量检索。
- Qdrant 负责什么，Elasticsearch 负责什么。
- 为什么要并发 scatter-gather。
- 为什么融合排序不能直接混用 Qdrant score 和 ES score。
- RRF / weighted fusion 的作用。

重点文件：

- `services/etl-worker/internal/retrieval/qdrant.go`
- `services/etl-worker/internal/retrieval/elastic.go`
- `services/etl-worker/internal/retrieval/fusion.go`
- `services/etl-worker/internal/retrieval/fusion_test.go`

合格标准：

- 能解释 exact / semantic / hybrid 三种 route 的差异。
- 能解释为什么 exact 查询更依赖 ES/BM25。
- 能手动推演两个候选文档经过 fusion 后谁排第一。

### 第 3 阶段：理解 Reranker 和退化风险

建议用时：2 天

重点学习：

- Cross-Encoder reranker 和 embedding 检索的区别。
- reranker 为什么提升语义查询。
- reranker 为什么可能伤害订单号、合同号、trace id 这类精确查询。
- `auto` 策略为什么比 `always` 更适合企业环境。

重点文件：

- `services/etl-worker/internal/retrieval/reranker.go`
- `services/etl-worker/internal/retrieval/rerank_policy.go`
- `services/etl-worker/internal/retrieval/engine_rerank_policy_test.go`

合格标准：

- 能说清楚哪些查询适合 rerank，哪些查询不适合。
- 能解释 `auto` 和 `always` 的区别。
- 能讲清楚 exact route pinning 的 bug：exact route 检测到 exact evidence，但原来没有 pin，导致噪声文档可能排第一。

### 第 4 阶段：学习企业级 Guardrail

建议用时：2 天

重点学习五层保护：

1. 规则层：识别明显 ID / 精确查询。
2. Schema 层：`customer_ref`、`contract_no` 等业务字段进入 metadata。
3. 召回信号层：候选是否包含 exact evidence。
4. 保护性 rerank：pin exact match。
5. 观测和评测层：用日志和 eval 证明没有退化。

重点文件：

- `services/etl-worker/internal/retrieval/metadata.go`
- `services/etl-worker/internal/retrieval/rerank_policy.go`
- `docs/evals/golden-set.json`
- `docs/module2-load-test-report.md`

合格标准：

- 能解释为什么不能只靠正则识别精确查询。
- 能解释 metadata-only 的 `customer_ref` 为什么必须支持。
- 能解释 `pin exact match` 的企业价值。

### 第 5 阶段：缓存、性能和压测

建议用时：1-2 天

重点学习：

- semantic cache 为什么能显著降低延迟。
- reranker 为什么是性能瓶颈。
- 怎么看 p50 / p95 / p99、QPS、top hit rate。
- 为什么压测不只是性能测试，也能发现排序 bug。

重点文件：

- `services/etl-worker/internal/retrieval/cache.go`
- `scripts/load-test.py`
- `docs/load-test.md`
- `docs/module2-load-test-report.md`

合格标准：

- 能解释以下压测数据：

```text
reranker off + cache off: p95 49.67ms
reranker on + cache off:  p95 321.26ms
reranker on + cache hit:  p95 15.83ms
schema exact:             p95 48.79ms
```

- 能说明为什么 reranker on 变慢，但 cache hit 又变快。
- 能独立跑一次 `scripts/load-test.py` 并读懂报告。

## 实践命令

运行 retrieval 策略定向测试：

```bash
docker run --rm \
  -v /home/ubuntu/ai-projects/ai-etl-platform/services/etl-worker:/app \
  -w /app \
  golang:1.24 \
  go test ./internal/retrieval \
  -run 'TestEngineRerankPolicy|TestPlanRerankPolicy|TestProtectExactMatches|TestExactCandidateEvidence' \
  -v
```

运行 Go 全量测试：

```bash
docker run --rm \
  -v /home/ubuntu/ai-projects/ai-etl-platform/services/etl-worker:/app \
  -w /app \
  golang:1.24 \
  go test ./...
```

运行 deterministic eval：

```bash
RETRIEVAL_ENABLE_RERANK=false \
RETRIEVAL_RERANK_POLICY=auto \
RERANK_ENDPOINT= \
python3 scripts/run-evals.py \
  --keep-services \
  --report-dir /tmp/ai-etl-module2-learning-eval \
  --max-wait 180
```

运行 schema exact 轻量压测：

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

## 最终考核

学完模块二后，需要能不看答案讲清楚以下问题：

1. 为什么企业 RAG 不能只用向量检索？
2. Qdrant 和 Elasticsearch 在模块二里分别负责什么？
3. Query Router 如何区分 exact / semantic / hybrid？
4. Fusion 为什么用 rank-based，而不是直接加 raw score？
5. Reranker 为什么有时提升效果，有时导致退化？
6. `auto` rerank 策略完整决策链是什么？
7. `customer_ref` 只存在 metadata 时，系统如何召回并保护它？
8. 如何用 eval 和压测证明模块二没有退化？

如果你能清楚回答这 8 个问题，并能独立跑通测试、eval 和轻量压测，就可以认为模块二学习合格。

