# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概览

企业级 AI 知识流水线平台（AI ETL/RAG）：上传文档 → Kafka 异步处理 → 解析切块 → 向量化 → Qdrant/ES 混合检索 → LLM 生成回答。三个核心工程关注点：**可靠性**（消息不丢）、**权限隔离**（检索源头过滤，机密不进候选）、**可量化检索质量**（双模式评测 + 对照实验）。仓库同时含一个面向面试 demo 的 Next.js 前端（`web/`）。

## 常用命令

### 全栈本地起服

```bash
cp .env.example .env            # 首次
docker compose up -d            # 起全部服务（含 web、prometheus/grafana/jaeger）
docker compose logs -f query-api
```

浏览器访问 http://localhost:3100（web 前端）或 http://localhost:8080/healthz（query-api）。本地 CPU rerank 需 `--profile rerank` 拉起 `reranker-service`。

### Go (services/etl-worker)

```bash
cd services/etl-worker
make build        # 编译 worker + api 两个二进制
make run-worker   # dev 模式跑 ETL worker
make run-api      # dev 模式跑 query-api（固定 JWT_SECRET=dev-secret）
make test         # go test -v -race + coverage，遍历 internal/...
make lint         # golangci-lint
make docker       # 构建 worker/api 镜像

go test ./internal/pipeline/ -run TestProcessTask -v   # 单个测试
```

### Python 解析服务 (services/doc-parser-service)

```bash
cd services/doc-parser-service
python -m venv venv && source venv/bin/activate
pip install -r requirements.txt
python -m app.main            # http://localhost:8000/docs
pytest -q
```

### 评测（scripts/run-evals.py）——双模式，指标含义不同

```bash
python3 scripts/run-evals.py                 # mock：确定性 CI 门禁，仅验证链路接通
python3 scripts/run-evals.py --real-models   # 真实 embedding+LLM，唯一能说明检索质量的方式
```

- mock 模式 embedding 由 hash 派生，**Recall/hit rate 不是质量证据**；真实模式才是。
- real 模式自动派生独立 Qdrant collection（`documents-real-<model>-<dim>`），维度创建后不可改。
- 配置解析优先级与 Go 服务一致：CLI 参数 > 环境变量 > `*_FILE_PATH` secrets。
- 可选 `--judge`（LLM-as-a-Judge）输出 Faithfulness/Correctness/Relevance。

### 全链路冒烟 / demo 数据

```bash
bash scripts/e2e-smoke.sh                          # 起本地 compose 全链路 mock 冒烟
python3 scripts/seed-demo-data.py --token <demo_jwt>   # 灌 demo 种子数据（语义集 44 篇）
```

### 前端 (web/)

```bash
cd web
cp .env.local.example .env.local   # 配 NEXT_PUBLIC_API_BASE 与 NEXT_PUBLIC_JWT
npm install && npm run dev         # http://localhost:3000（compose 内为 WEB_HOST_PORT=3100）
```

## 架构总览

### 端到端数据流

```
upload(/v1/upload → MinIO 落盘 + Kafka 投递)
  → etl-worker(consume → worker pool → parse → embed → store)
  → Qdrant(dense+sparse 向量) + Elasticsearch(BM25 全文，最终一致 sink)
  → query-api(/v1/query → Router → Scatter-Gather 召回 → RRF 融合 → 可选 rerank + exact pin → 语义缓存)
  → LLM → 流式回答（SSE）
```

### ETL worker（services/etl-worker，Go module `ai-etl-pipeline`）

两个入口：`cmd/worker`（Kafka 消费端）、`cmd/api`（query-api）。业务逻辑全在 `internal/`。

**解析双路径**（`internal/pipeline/pipeline.go`，改动最频繁的核心）：
- 纯文本（.txt/.md/.csv…）→ 本地确定性 scanner（`internal/parser`），与 golden-set 评测切块语义一致；
- 二进制（.pdf/.docx/.doc，含扫描件）→ 先从 MinIO 物化到本地临时文件，再经 parser-service（FastAPI）HTTP 解析，扫描件走 OCR（rapidocr）。
- 两条路径以流式 channel 方式喂给统一 batch embed + store 消费端（新切片仍在演进中）。

**可靠性不变量**（整个 worker 遵守）：
- 成功才 Ack；失败指数退避重试，重试耗尽**先写 DLQ、DLQ 成功后才 Ack**（写 DLQ 失败则 Nack 重投）。
- Redis checkpoint 断点续传（每 batch 后更新）；单 chunk 用 `storer.Exists` 跳过已完成项。
- 每 batch 并发 embed（sem=5）+ 顺序 store；store 失败率过半则整任务失败。
- ES 全文索引是最终一致 sink：Qdrant 成功为主路径，ES 错误不影响主链路。

### 检索（internal/retrieval + internal/query）

- `router.go` 判断意图：精确锚点（`RETRIEVAL_EXACT_SCHEMA_FIELDS` 里的 doc_id/order_no 等字段）直接精确路由；否则语义/混合。
- `engine.go` Scatter-Gather：Qdrant dense+sparse + ES BM25 多路召回，`fusion.go` RRF 融合去重，单路故障降级。
- 可选 Cross-Encoder rerank（`reranker-service`，compose `rerank` profile）。`RETRIEVAL_RERANK_POLICY=auto` 下 exact-match pin 保护：exact token 命中即保留候选，避免被 rerank 翻转（实验结论：英文 ms-marco reranker 对中文负优化，默认关闭，但 pin 机制独立生效）。
- Redis 语义缓存（`redis-cache`，tenant + permission scope 隔离，`SEMANTIC_CACHE_THRESHOLD`）。

### Redis 缓存/状态分离 —— 正确性关键（ADR 0004）

两个实例职责不可混用，别把状态写进 cache 实例：
- `redis-cache`（allkeys-lru）：语义检索缓存，丢了只是多检索一次，无正确性影响。
- `redis-state`（noeviction + AOF）：agent run 状态、审批审计、fencing token、幂等键、checkpoint、任务状态、ES 重试队列。**被 lru 淘汰会破坏并发安全与合规凭证**。

配置优先级 `REDIS_CACHE_*` / `REDIS_STATE_*` > `REDIS_*`（共享回退）；生产启动校验拒绝两者指向同一实例/DB。

### Agent orchestrator（internal/agent + internal/agentapi）

`/v1/agent/runs` API 背后的有状态编排核心：durable run/step 记录、fencing token + 乐观版本号防陈旧写入、高危险工具挂起审批 + 审计、失败工具补偿 handler、Redis 分布式锁、OpenAI 兼容 LLM planner + 确定性 Rule planner（开发/测试用）。设计见 `docs/agent-orchestrator-design.md`。

### 解析服务（services/doc-parser-service，FastAPI）

`app/services/parsers/{pdf,docx,text}.py` 按扩展名路由，`app/services/chunker.py` 语义切块（标题感知 + 段落合并 + 重叠）。安全：文件大小限制、扩展名白名单、非 dev 环境要求 `X-Internal-Token`。近期加入扫描件 OCR 与噪声切块过滤（签名页、目录、空表格）。

### 评测体系（scripts/ + docs/evals/）

- 数据集：`golden-set.json`（锚点/关键词匹配，别拿它当质量证据）、`semantic-golden-set.json`（44 篇语义集，检索质量主依据）、`real-baseline-findings.md`（真实基线报告）。
- 核心教训：锚点集 100% 是假象（关键词匹配未走语义检索）；换真实语义集 + bge-m3 后 Recall@1 19% → 71%、@5 95%（nomic-embed-text → bge-m3）。embedding 选型是检索质量的决定性因素。
- `docs/adr/` 记录关键决策。ADR 0006：相关性硬阈值因分数分布重叠（重叠宽度 0.2784）未启用，仅暴露 `MaxRelevance` 作可观测置信度，不做硬门控。

### 前端（web/，Next.js 14）

面试 demo 工作台：问答（SSE 流式渲染 + 引用展开）、数据接入（上传/同 doc_id 重传即替换）、检索质量、系统可观测、Agent 编排。`app/api/*` route handler 代理到 query-api `/v1/*`，SSE 透传无跨域。demo JWT 由 etl-worker 的 `internal/auth` 生成（scope `query,upload`）。演示脚本与注意事项见 `docs/demo-runbook.md`。

## 约定

- **配置解析**：`KEY` > `KEY_FILE`（`<KEY>_FILE` 指向 secret 文件）> 默认值。新增配置要镜像进 `.env.example`。
- **Secrets**：绝不提交真实凭据。模板在 `secrets/examples/`，本地真实值放 `secrets/dev/`（git-ignored）。webhook/密码等真实文件只在 `secrets/dev/`。
- **提交**：Conventional Commits（`feat:`/`fix:`/`chore:`），聚焦、描述用户可见或运维影响。分支保护要求 1 个 approval + CI 通过。
- **代码风格**：Go 用 gofmt、包名短小写、测试 `_test.go` 与源码同包；Python PEP8 + Pydantic typed models。
- **文档语言**：README/docs/demo 均为中文；接口拒答用固定句「未找到相关文档，无法回答该问题。」。改动涉及检索/agent 行为时，更新 `docs/demo-runbook.md` 与评测口径保持一致。
- **测试**：行为变更必须带测试。Go 用确定性 mock fixture（`make test` 已含 race）；Python 用 pytest；全链路仅用 `scripts/e2e-smoke.sh`。CI 门禁：gofmt、go vet、go test、pytest、`docker compose config`、Trivy CRITICAL。
