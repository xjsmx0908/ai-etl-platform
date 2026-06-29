# Evals

本目录用于维护 AI-ETL 的可重复评测闭环（以 retrieval 为主，不使用 LLM-as-judge）。

## 文件说明

- `golden-set.json`: 黄金样本集（当前 46 条）。
  - 可选字段：`acceptable_doc_ids`，允许一个 query 命中多个等价文档（可写 case id 或 doc_id）。
  - 可选字段：`expect_hit`，默认 `true`；`false` 表示负样本（断言不应命中目标文档）。
  - 可选字段：`max_strict_rank`，要求严格命中的最小排名上界（如 `1`/`3`）。
  - 可选字段：`query_top_k`，单条样本覆盖默认 `top_k`。
  - 可选字段：`must_not_hit_doc_ids`，断言禁止命中的文档（可写 case id 或 doc_id）。
  - 可选字段：`require_source_citation`，答案中必须包含来源 `doc_id`（默认正样本 `true`）。
  - 可选字段：`answer_must_include` / `answer_must_not_include`，答案文本包含/禁止短语断言。
- `reports/`: 评测报告输出目录（已在 `.gitignore` 中忽略）。

## 运行方式

```bash
python3 scripts/run-evals.py
```

Reranker 策略专项验证：

```bash
docker run --rm \
  -v /home/ubuntu/ai-projects/ai-etl-platform/services/etl-worker:/app \
  -w /app golang:1.24 \
  go test ./internal/retrieval -run TestEngineRerankPolicy -v
```

该测试构造两类确定性场景：

- 语义查询：融合排序把干扰文档排第一，`RETRIEVAL_RERANK_POLICY=auto` 调用 reranker 后把目标文档提升到第一。
- 精确查询：BM25/向量融合把精确订单文档排第一，`auto` 对明确精确路由跳过 reranker；若改成 `always`，同一个 reranker 会把干扰文档排第一，用于证明裸 rerank 的退化风险。
- 候选证据保护：即使 query 没有被关键词/旧正则路由为精确查询，只要 query 中的疑似 ID/token 在候选 `doc_id`、`chunk_id`、正文或配置的业务 `metadata` 字段中完整出现，`auto` 会调用 reranker 后 pin exact-match 候选，保证非 exact 候选不能超过 exact 候选。
- Schema eval：`golden-set.json` 的 `case-047` 使用 metadata-only 的 `customer_ref`，正文不包含该参考号，用于验证业务 schema 字段能参与召回和保护性 rerank。

常用参数：

```bash
python3 scripts/run-evals.py \
  --min-hit-rate 0.90 \
  --min-answer-pass-rate 1.00 \
  --min-pass-rate 1.00 \
  --required-consecutive-hits 2 \
  --negative-max-wait 30 \
  --max-wait 180 \
  --tenant-id tenant-eval
```

说明：

- 默认会自动生成唯一 `tenant_id`（如 `tenant-eval-...`），避免多次评测复用同一租户导致历史数据污染排序。
- 仅在需要复现实验时再显式传 `--tenant-id` 固定值。
- 正样本默认要求“连续 2 次严格命中”才算通过（可用 `--required-consecutive-hits` 调整）。
- 可通过 `--disable-answer-assertions` 暂时关闭答案层断言（默认开启）。

## 判定规则

- 每条样本会先上传文档，再以该样本 query 轮询 `/v1/query`。
- 正样本（`expect_hit=true`）：
  - 严格命中：返回 `sources` 中包含该样本对应 `doc_id`。
  - 若设置 `max_strict_rank`，则要求严格命中排名不高于该阈值（例如 Top1/Top3）。
  - 需满足连续命中门槛（默认 2 次）才判定通过。
- 负样本（`expect_hit=false`）：
  - 在观察窗口内不允许命中目标文档。
  - 若配置 `must_not_hit_doc_ids`，这些文档也不允许出现在返回结果中。
- 答案层断言（默认开启）：
  - 正样本默认要求答案包含问题锚点（如 `alpha012`）与来源 `doc_id`。
  - 负样本默认要求答案包含拒答标记（`未找到相关文档，无法回答该问题。` 或 `未在参考文档中直接定位锚点`）。
  - 可通过 `answer_must_include` / `answer_must_not_include` 增加业务短语断言。
- 汇总指标：
  - `retrieval_pass_rate = 检索断言通过样本数 / 总样本数`
  - `answer_pass_rate = 答案断言通过样本数 / 总样本数`
  - `pass_rate = 断言通过样本数 / 总样本数`（覆盖正负样本）
  - `hit_rate = 正样本通过数 / 正样本总数`
  - `acceptable_hit_rate = 正样本可接受命中数 / 正样本总数`
  - `Recall@1/3/5 = 正样本可接受文档在前 K 条检索结果中出现的比例`
  - `avg_score = 命中样本的平均检索分数`

## 设计约束

- Mock embedding 与 mock chat 都是确定性的，保证 CI 可重复。
- CI 门禁可同时使用 `hit_rate` 与 `pass_rate`（建议 `hit_rate >= 0.90` 且 `pass_rate = 1.00`）。
- 该评测主要用于回归检测检索路径，不代表最终答案质量上限。
