# Evals

确定性 retrieval/答案断言是 CI 硬门禁。LLM-as-a-Judge 是可选离线评测，不替代确定性断言。
历史实验结论已写入 `docs/adr/0005`、`0006` 和当前默认检索配置，不再保留分阶段实验报告。

## 文件

- `golden-set.json`：CI 黄金集（47 条）。可选字段：`acceptable_doc_ids`、`expect_hit`、`max_strict_rank`、`query_top_k`、`must_not_hit_doc_ids`、`require_source_citation`、`answer_must_include` / `answer_must_not_include`、`reference_answer`。
- `semantic-golden-set.json`：语义评测集。
- `engineering-learning-golden-set.json`：合成学习集，不能当企业验收。
- `query-planner-public.json`：Query Planner 公共门槛集。
- `historical-golden-set.template.json` / `historical-data-intake.md`：私有历史集模板与脱敏流程。
- `business-gold-approval.template.json`：业务 Gold 批准模板。
- `public-datasets.json`：已审核公共数据集目录。
- `reports/`、`private/`：本地产物，不提交。`reports/latest.json` 是 `/quality` 使用的检索质量摘要；web 在启动时拷贝该文件覆盖烘焙副本，空目录不会再挡住 `web/public/evals/latest.json`。

## 运行

```bash
python3 scripts/run-evals.py
```

默认创建独立 `ai-etl-eval-*` Compose 项目并加载 `docker-compose.eval.yml`。
完成后 `docker compose down -v` 只清理该项目。排查时加 `--keep-services`。

| 模式 | 命令 | 含义 |
| --- | --- | --- |
| mock（默认） | `python3 scripts/run-evals.py` | 只验证链路，Recall 不是质量证据 |
| real | `python3 scripts/run-evals.py --real-models --embed-dim 1024` | 唯一能说明检索质量的模式 |

真实模式会使用独立 Qdrant collection，避免与 mock 维度冲突。

入库容量（accepted-to-ready，真实 embedding）是另一条轨道，命令是
`python3 scripts/ingestion-capacity.py`，说明见 [`../ingestion-capacity.md`](../ingestion-capacity.md)。
不要把它和 L1 查询 p95 或本页 Recall 混用。本机演示栈运行时不要再起一套 eval Compose。要对现网 Query API 做技术评测，加 `--api-base http://127.0.0.1:8080`，不要停演示栈。`/quality` 读取 `docs/evals/reports/latest.json`（启动时覆盖烘焙副本）；那是最近一次真实模型技术评测，不是 P1.9 业务签字 Gold。


```bash
python3 scripts/run-evals.py \
  --min-hit-rate 0.90 \
  --min-pass-rate 0.90 \
  --min-answer-pass-rate 0.90 \
  --min-negative-pass-rate 1.00 \
  --max-wait 180
```

跨文档诊断（隔离栈，且仅在评测开关打开时）：

```bash
python3 scripts/run-evals.py \
  --golden-set docs/evals/private/p1.4-enterprise/gold-candidate-v2.json \
  --cohort cross_document --retrieval-only --real-models
```

`RETRIEVAL_DIAGNOSTICS_ENABLED` 只用于本地评测。文档 `completed` 但 chunk 数为 0
视为摄取失败，不能算检索漏召。`run_valid=false` 的报告不得当质量证据。

## Judge

```bash
JUDGE_API_KEY=... JUDGE_MODEL=gpt-4o \
python3 scripts/run-evals.py --judge --judge-max-cases 10 --judge-min-pass-rate 0.80
```

常规 CI 仍只跑确定性评测。手动工作流：`.github/workflows/judge-eval.yml`。

## 企业私有 Gold

从忽略的 `rag_datas/` 在本机构造候选，产物只写 `docs/evals/private/`，不得提交或发给外部模型。

```bash
python3 scripts/build-enterprise-eval.py \
  --input-dir rag_datas --output-dir docs/evals/private/p1.4-enterprise
python3 scripts/review-enterprise-eval.py
python3 scripts/apply-enterprise-confirmations.py
python3 scripts/expand-enterprise-eval.py
python3 scripts/finalize-enterprise-gold.py \
  --candidate docs/evals/private/p1.4-enterprise/gold-candidate-v2.json \
  --approval docs/evals/private/p1.4-enterprise/business-gold-approval.json \
  --signed-approval docs/evals/private/p1.4-enterprise/signed-business-approval.pdf \
  --output docs/evals/private/p1.4-enterprise/gold-approved.json
```

`business_approval_complete=true` 只对这一精确候选有效。P1.9 仍等待授权责任人签署。

Query Planner 公共门槛：

```bash
python3 scripts/benchmark-query-planner.py \
  --dataset docs/evals/query-planner-public.json \
  --endpoint http://127.0.0.1:11434/v1/chat/completions \
  --model MODEL_NAME --repetitions 3
```

门槛：结构响应 100%、跨文档激活 ≥80%、facet 覆盖 ≥80%、单语义误激活 ≤10%、lexical/exact 激活 0%。

## 公共检索基线

NanoSciFact 只评估英文通用 retrieval，不是企业验收。

```bash
python3 scripts/download-public-eval.py --dataset nanoscifact \
  --output-dir docs/evals/private/public/nanoscifact-beir
python3 scripts/import-public-eval.py \
  --beir-dir docs/evals/private/public/nanoscifact-beir \
  --output docs/evals/private/public/nanoscifact.json
python3 scripts/validate_eval_dataset.py \
  --golden-set docs/evals/private/public/nanoscifact.json --min-cases 50
```

历史私有集校验见 `historical-data-intake.md`。

## 判定规则

- 正样本：`sources` 含目标 `doc_id`，可选 `max_strict_rank`，默认连续命中 2 次。
- 负样本：观察窗口内不得命中目标或 `must_not_hit_doc_ids`。
- 答案断言默认开启；安全负样本 `--min-negative-pass-rate` 默认 `1.00`。
- Mock 模式可重复，不代表真实答案质量上限。
