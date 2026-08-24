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
- `public-datasets.json`: 经审核的公共数据集、固定 revision、许可证、规模与内容校验和。
- `reports/`: 评测报告输出目录（已在 `.gitignore` 中忽略）。

## 运行方式

```bash
python3 scripts/run-evals.py
```

P1.8 跨文档诊断可只执行 `cross_document` cohort，同时保留完整文档集作为干扰语料；
`--retrieval-only` 会在隔离评测服务启用诊断开关时跳过答案 LLM，仅观测检索来源和
聚合阶段覆盖率：

```bash
python3 scripts/run-evals.py \
  --golden-set docs/evals/private/p1.7-enterprise/gold-candidate-v2.json \
  --cohort cross_document \
  --retrieval-only \
  --real-models
```

该模式仍上传并索引数据集中的全部文档，报告只在聚合诊断表中分别显示 Qdrant、
Elasticsearch、融合和最终选择阶段的必要来源命中率。运行必须使用独立 Compose
项目，并在 `run_valid=true` 后才可用于定位检索丢失边界。

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

## 企业私有银标

P1.4 从 Git 忽略的 `rag_datas/` 读取真实 Office/PDF 文档，所有解析、问题生成和后续基线都在本机执行。包含正文、文件名、证据或问题的产物只写入 `docs/evals/private/p1.4-enterprise/`，不得提交、上传到公共 CI 或发送给外部 LLM/Judge。

```bash
docker build -t ai-etl-parser-p14-test services/doc-parser-service

docker run --rm --user "$(id -u):$(id -g)" \
  --add-host host.docker.internal:host-gateway \
  -v "$PWD":/workspace -w /workspace \
  -e PYTHONPATH=/workspace/services/doc-parser-service \
  ai-etl-parser-p14-test \
  python scripts/build-enterprise-eval.py \
    --input-dir /workspace/rag_datas \
    --output-dir /workspace/docs/evals/private/p1.4-enterprise \
    --model qwen3:4b --questions-per-document 4

python3 scripts/validate_eval_dataset.py \
  --golden-set docs/evals/private/p1.4-enterprise/silver-eval.json \
  --min-cases 100 --allow-sensitive-patterns
```

首轮 40 文档、114 case 的本地真实模型结果见
[`p1.4-enterprise-baseline.md`](p1.4-enterprise-baseline.md)。该报告只包含脱敏
聚合数据；逐条报告继续保留在被忽略的
`docs/evals/reports/p1.4-enterprise-no-rerank/`。当前 Recall@5 为 83.49%，但答案
断言通过率仅 26.32%，因此该结果是改进起点，不是企业上线门禁。

`document-review.csv` 用于确认权限、业务所有者和文档效力；`conflict-review.csv` 用于指定疑似重复版本中的有效文档；`case-review.csv` 仅要求复核低置信、权限和冲突样本。自动通过证据、PII 和关键短语检查的内容是 silver，不是业务验收 gold；数据所有者确认后才能提升为金标门禁。

可先执行本地自动审核，将人工工作压缩到业务权威才能判断的项目：

```bash
python3 scripts/review-enterprise-eval.py \
  --model qwen2.5:1.5b --model-timeout 180

python3 scripts/validate_eval_dataset.py \
  --golden-set docs/evals/private/p1.4-enterprise/gold-draft.json \
  --min-cases 20 --allow-sensitive-patterns
```

审核器保留原始 CSV，另行生成 `automated-document-review.csv`、
`automated-case-review.csv`、`human-confirmation.csv` 和 `gold-draft.json`。
`gold-draft.json` 的 `business_approval_complete=false`，不得直接作为上线门禁。

### 业务 Gold 最终批准

技术确认、自动审核或“取最新日期”规则都不能替代业务负责人批准。最终批准使用
`business-gold-approval.template.json` 建立私有审批记录，并绑定候选文件与独立保存
的签署材料 SHA-256。审批记录和签署材料必须保存在 `docs/evals/private/` 或其他受控
私有位置，不得提交。

审批人必须确认完整的文档 ID 和案例 ID 范围，并逐项确认文档权威性、权限、有效
版本以及问题与标准答案。任何缺失、`pending`、摘要不匹配或 false 声明都会停止，
且不会创建输出：

```bash
python3 scripts/finalize-enterprise-gold.py \
  --candidate docs/evals/private/p1.4-enterprise/gold-candidate-v2.json \
  --approval docs/evals/private/p1.4-enterprise/business-gold-approval.json \
  --signed-approval docs/evals/private/p1.4-enterprise/signed-business-approval.pdf \
  --output docs/evals/private/p1.4-enterprise/gold-approved.json
```

成功输出使用 `enterprise_private_gold` 类型，并在 provenance 中记录候选、审批记录和
签署材料摘要。`business_approval_complete=true` 只表示该精确数据集已经通过所附业务
批准，不代表后续新增或修改内容自动获批。
首轮聚合结果和 cohort 解释见
[`p1.5-automated-review.md`](p1.5-automated-review.md)。

业务方接受“最新日期”作为本轮候选规则后，可解析正文、文件名及内嵌元数据，
回填确认表并生成研发用 Gold 候选：

```bash
python3 scripts/apply-enterprise-confirmations.py

python3 scripts/validate_eval_dataset.py \
  --golden-set docs/evals/private/p1.4-enterprise/gold-candidate.json \
  --min-cases 31 --allow-sensitive-patterns
```

日期并列或无法提取时脚本保持 `pending` 并停止生成，禁止使用文件系统上传时间
猜测业务版本。`gold-candidate.json` 会记录 `confirmation_rule`，仍不代表制度台账
或正式业务负责人已经签字。

P1.6 使用本地生成模型和独立审核模型扩充自然语义与跨文档 cohort：

```bash
python3 scripts/expand-enterprise-eval.py \
  --generation-model qwen3:4b \
  --review-model qwen3:1.7b

python3 scripts/validate_eval_dataset.py \
  --golden-set docs/evals/private/p1.4-enterprise/gold-candidate-v2.json \
  --min-cases 70 --allow-sensitive-patterns
```

脚本拒绝非回环模型地址，逐条复核证据、关键事实、自然度和来源完整性，并生成
30 条 semantic、15 条 lexical、10 条 cross-document 和 15 条 safety-negative
案例。`required_doc_ids` 表示检索与答案引用必须同时覆盖的全部来源。完整聚合结果
见 [`p1.6-semantic-cross-document.md`](p1.6-semantic-cross-document.md)。该候选集仍
保留 `business_approval_complete=false`，不得称为正式业务验收 Gold。

P1.7 使用同一 tenant、上传映射和模型完成 no-rerank 与 `auto` rerank 各三次
匹配运行。六次运行均通过完整性门禁，但 `auto` 在 semantic、lexical、
cross-document 和 safety 门禁上失败，因此默认策略保持 no-rerank。仅含聚合结果、
方差和限制的报告见
[`p1.7-enterprise-reranker-ab.md`](p1.7-enterprise-reranker-ab.md)；逐案例报告继续位于
Git 忽略目录。

P1.8 的受控诊断请求可在隔离 Compose 项目中设置
`RETRIEVAL_DIAGNOSTICS_ENABLED=true`，并在 `/v1/query` 请求中附带
`diagnostic_required_doc_ids`。该字段只用于本地评测；响应和报告只保留必要来源的
总数、命中数、all-required 布尔值，以及全部来源齐备时最深的首次出现排名；阶段
不完整时该排名为 0。诊断绝不回显来源 ID、文件名、问题、答案或证据。

诊断会分别记录每个后端候选、应用层融合 Top-50 和最终 Top-K 的覆盖率，从而区分
后端召回、融合截断与最终多样性选择造成的丢失。普通开发栈默认关闭该开关。

隔离评测不能跨 Compose 项目复用上传映射中的 Qdrant/Elasticsearch 数据卷；映射只
绑定 tenant、数据集摘要和文档 ID。新的隔离项目必须在本项目内上传并等待 ETL 完成。
若宿主磁盘水位或本机 Ollama circuit breaker 导致任务不完整，报告必须按
`run_valid=false` 排除，不得作为检索质量证据。

摄取完整性也是评测有效性的前置条件。文档状态为 `completed` 但 chunk 数为 0 时，
它在 registry 中可见却无法被 Qdrant/Elasticsearch 召回；这类状态必须视为摄取失败，
不能归类为检索漏召。P1.8 诊断据此将零 chunk 设为 worker 错误，并把“目录/目次”
噪声过滤限制为短片段，避免含相关词的长正文整块消失。

P1.8-B 曾在默认关闭的实验实现中测试确定性语义分句。matched feature-off/on
retrieval-only 运行均有效且请求完整，但 candidate 没有安全展开任何查询，聚合阶段
和最终 Top-5 指标均未变化，因此实现和配置已撤回。该结果只否定标点/连词驱动的
分句路径，不否定能识别隐含语义 facet 的其他多路检索设计。

### P1.8-C 本地 Query Planner 公共门槛

`query-planner-public.json` 是不含企业内容的 18 条合成门槛集，包含 8 条隐含跨文档
问题、6 条单语义对照和 4 条 lexical/exact 对照。评测器只允许回环模型端点，并且
只输出聚合指标：

```bash
python3 scripts/benchmark-query-planner.py \
  --dataset docs/evals/query-planner-public.json \
  --endpoint http://127.0.0.1:11434/v1/chat/completions \
  --model MODEL_NAME \
  --repetitions 3 \
  --timeout 45 \
  --report docs/evals/private/query-planner-report.json
```

正式门槛要求三轮合计达到：结构响应 100% 有效、跨文档激活率至少 80%、预期 facet
覆盖率至少 80%、单语义误激活率不超过 10%，以及 lexical/exact 激活率为 0%。模型
应先通过单轮筛选；任一指标失败时不得运行私有企业评测。

本轮 `qwen2.5:1.5b` 在结构提示修正后虽达到 100% 有效响应，但跨文档、单语义和
lexical/exact 均 100% 激活，预期 facet 覆盖率仅 6.25%。`qwen3:4b` 的单轮筛选
结构有效率、跨文档激活率和 facet 覆盖率均为 0%。两者均未进入正式三轮门槛；运行时
候选已撤回，且未运行私有评测。逐条模型输出和本地报告继续保持 Git ignored。

## 公共检索基线

NanoSciFact 是英文科学论断检索集，只评估通用 retrieval，不评估中文办公制度、权限治理或答案正确性。批准目录固定 `CC-BY-4.0` 许可证、上游 commit、行数和 SHA-256；下载内容与生成的评测集均位于 Git 忽略的 `docs/evals/private/public/`。

下载、转换并严格校验完整快照：

```bash
python3 scripts/download-public-eval.py \
  --dataset nanoscifact \
  --output-dir docs/evals/private/public/nanoscifact-beir

python3 scripts/import-public-eval.py \
  --beir-dir docs/evals/private/public/nanoscifact-beir \
  --output docs/evals/private/public/nanoscifact.json \
  --name NanoSciFact \
  --dataset-version 309f1d1ae3ae2e092444a8a0c25bed59b82318bc \
  --source-url https://huggingface.co/datasets/zeta-alpha-ai/NanoSciFact \
  --license CC-BY-4.0 \
  --split train

python3 scripts/validate_eval_dataset.py \
  --golden-set docs/evals/private/public/nanoscifact.json \
  --min-cases 50
```

快速链路检查可确定性抽取 10 个 query 和 300 个文档。该输出会记录 `benchmark_comparable=false`，不能与完整 NanoSciFact 分数比较：

```bash
python3 scripts/import-public-eval.py \
  --beir-dir docs/evals/private/public/nanoscifact-beir \
  --output docs/evals/private/public/nanoscifact-quick.json \
  --name NanoSciFact-quick \
  --dataset-version 309f1d1ae3ae2e092444a8a0c25bed59b82318bc \
  --source-url https://huggingface.co/datasets/zeta-alpha-ai/NanoSciFact \
  --license CC-BY-4.0 --split train \
  --max-queries 10 --max-documents 300 --seed 42

python3 scripts/run-evals.py \
  --golden-set docs/evals/private/public/nanoscifact-quick.json \
  --required-consecutive-hits 1 --max-wait 5 --poll-interval 0.5 \
  --min-hit-rate 0 --min-pass-rate 0
```

默认 mock 模式仅验证上传、ETL、索引与查询链路。完整质量基线必须使用真实 embedding/LLM，并把 `--embed-dim` 设为实际向量维度：

```bash
EMBED_ENDPOINT=... EMBED_MODEL=... \
LLM_ENDPOINT=... LLM_MODEL=... \
python3 scripts/run-evals.py \
  --real-models --embed-dim 768 \
  --golden-set docs/evals/private/public/nanoscifact.json
```

retrieval-only 数据集会自动关闭答案断言，也禁止启用 Judge，因为没有参考答案。报告会披露数据来源、许可证、模型模式和可比较性，并明确公共分数不是企业验收证据。

P1.3 的真实模型小样本结果、reranker 对照和完整运行前置条件见 [`p1.3-real-model-baseline.md`](p1.3-real-model-baseline.md)。完整快照运行前必须确认外部 LLM 预算；价格变量未配置时，报告不会估算费用。

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
  - `negative_pass_rate = 检索隔离与答案拒答均通过的负样本数 / 负样本总数`；默认独立要求 100%，不能被总体平均分掩盖。
  - `hit_rate = 正样本通过数 / 正样本总数`
  - `acceptable_hit_rate = 正样本可接受命中数 / 正样本总数`
  - `Recall@1/3/5 = 正样本可接受文档在前 K 条检索结果中出现的比例`
  - `avg_score = 命中样本的平均检索分数`

## 设计约束

- Mock embedding 与 mock chat 都是确定性的，保证 CI 可重复。
- CI 门禁可同时使用 `hit_rate` 与 `pass_rate`（建议 `hit_rate >= 0.90` 且 `pass_rate = 1.00`）。
- 安全负样本使用独立 `--min-negative-pass-rate` 门禁，默认值为 `1.00`。
- 该评测主要用于回归检测检索路径，不代表最终答案质量上限。
