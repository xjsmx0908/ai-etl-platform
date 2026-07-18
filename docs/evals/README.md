# Evals

本目录用于维护 AI-ETL 的可重复评测闭环。确定性 retrieval/答案断言是 CI 硬门禁；LLM-as-a-Judge 是可选的离线质量评测，不替代确定性断言。

## 文件说明

- `golden-set.json`: 黄金样本集（当前 47 条）。
  - 可选字段：`acceptable_doc_ids`，允许一个 query 命中多个等价文档（可写 case id 或 doc_id）。
  - 可选字段：`expect_hit`，默认 `true`；`false` 表示负样本（断言不应命中目标文档）。
  - 可选字段：`max_strict_rank`，要求严格命中的最小排名上界（如 `1`/`3`）。
  - 可选字段：`query_top_k`，单条样本覆盖默认 `top_k`。
  - 可选字段：`must_not_hit_doc_ids`，断言禁止命中的文档（可写 case id 或 doc_id）。
  - 可选字段：`require_source_citation`，答案中必须包含来源 `doc_id`（默认正样本 `true`）。
  - 可选字段：`answer_must_include` / `answer_must_not_include`，答案文本包含/禁止短语断言。
  - 可选字段：`reference_answer`，作为 Judge 的标准答案；未提供时使用样本文档 `content` 作为参考材料。
- `historical-golden-set.template.json`: 私有历史评测集的空模板。
- `historical-data-intake.md`: 历史数据脱敏、审核、校验和受控运行流程。
- `engineering-learning-golden-set.json`: 当前项目架构衍生的 100 条合成学习案例，不是企业历史数据。
- `reports/`: 评测报告输出目录（已在 `.gitignore` 中忽略）。

## 运行方式

```bash
python3 scripts/run-evals.py
```

运行器默认创建唯一的 `ai-etl-eval-*` Compose 项目，并加载 `docker-compose.eval.yml`。除 Query API 的 Docker 随机宿主机端口外，其余服务只在隔离 Compose 网络内监听；报告会记录项目名和实际 Query API 地址。完成后只会执行该项目的 `docker compose down -v`，不会停止默认 `ai-etl-platform` 开发栈。需要保留隔离环境排查时使用 `--keep-services`，并按报告中的项目名手动清理。

## LLM-as-a-Judge

Judge 默认关闭。启用后，脚本会在 Query 响应仍在内存中时，将问题、标准答案或参考材料、系统答案和召回上下文发送给独立 Judge。Judge 使用严格 JSON Schema 返回：

- `faithfulness_score`：答案声明是否被召回上下文支持，1–5 分。
- `correctness_score`：答案是否符合标准答案或预期拒答行为，1–5 分。
- `relevance_score`：答案是否直接回答问题，1–5 分。
- `overall_pass`、理由和不受支持的声明列表。

```bash
JUDGE_API_KEY=... \
JUDGE_MODEL=gpt-4o \
python3 scripts/run-evals.py \
  --judge \
  --judge-max-cases 10 \
  --judge-min-pass-rate 0.80 \
  --judge-min-faithfulness 4.0
```

`JUDGE_ENDPOINT` 默认为 `https://api.openai.com/v1`，也可以指向支持 Chat Completions 与 `json_schema` Structured Outputs 的兼容服务。Judge 对 429、5xx 和网络错误进行有限重试；任何 Judge 请求错误都会写入报告并使本次 Judge 评测失败。

常规 CI 仍只运行确定性评测。`.github/workflows/judge-eval.yml` 提供手动 Judge 工作流，需要仓库 Secret `JUDGE_API_KEY`，可用仓库变量 `JUDGE_ENDPOINT` 覆盖接口地址。评测报告可能包含 Judge 理由中的业务片段，必须按敏感测试数据管理。

当前黄金集为 47 条工程样本。真实企业验收应通过 `--golden-set` 接入至少 100 条经过脱敏、带 `reference_answer` 的历史问题，而不是人工复制样本凑数。

若目标是学习和回归而不是生产验收，可使用合成学习集：

```bash
python3 scripts/validate_eval_dataset.py \
  --golden-set docs/evals/engineering-learning-golden-set.json

python3 scripts/run-evals.py \
  --golden-set docs/evals/engineering-learning-golden-set.json
```

该集合覆盖 ETL、检索、Agent、可观测性四个模块，各有 22 条可回答样本和 3 条权限拒答样本。它只验证项目中的工程概念、权限边界与回归能力，不能用于证明真实企业用户体验或业务检索效果。

将真实数据放在被 Git 忽略的 `docs/evals/private/` 后，先运行严格校验：

```bash
python3 scripts/validate_eval_dataset.py \
  --golden-set docs/evals/private/historical-golden-set.json
```

该校验默认要求至少 100 条 case、唯一 ID、必填字段和 `reference_answer`，并拒绝邮箱、手机号、身份证号、卡号、JWT、API key、企业 webhook URL 等高置信敏感模式。它不会回显匹配的原始文本；但自动扫描不能替代数据所有者审核。详细流程见 `historical-data-intake.md`。

私有历史集和 Judge 报告不应交给公共 CI 或未获批准的外部模型端点。运行器的默认 Compose 项目隔离保护本机共享服务；真实生产数据仍应在受控 self-hosted runner 或独立评测主机中运行，并按敏感数据处理产物。

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
