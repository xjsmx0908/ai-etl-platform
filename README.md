# AI-ETL Platform

企业级 AI 知识流水线：文档解析、向量化、混合检索、RAG 查询，以及受控的
Knowledge Release Center 与自主 Agent 预审。

当前主链路、发布中心和 Agent 预审已落地。未完成事项与外部决策门见
[`docs/backlog.md`](docs/backlog.md)。

## 架构

```text
Web / API Client
      │
      ▼
Query API (Go)
  ├─ Upload / Catalog / Auth / RAG Query
  ├─ Knowledge Release Center
  │    └─ Review Agent: Planner → Read-only Tool → Observation → Report
  └─ Approval / exact-candidate publication
      │
      ├──────────────► PostgreSQL（权威元数据、发布、审批、审计）
      ├──────────────► Redis Cache / Redis State（缓存、Run/Step、锁）
      ├──────────────► Qdrant + Elasticsearch（向量与全文索引）
      └──────────────► MinIO（原始文档）

Kafka → ETL Worker (Go) → Parser Service (Python) → Embedding → 双索引
```

详细设计见 [`docs/architecture-design-document.md`](docs/architecture-design-document.md)
和 [`docs/technical-design-document.md`](docs/technical-design-document.md)。

## 项目结构

```
ai-etl-platform/
├── services/etl-worker/         # Go ETL Worker + Query API
├── services/doc-parser-service/ # Python Parser
├── services/reranker-service/   # 可选 Cross-Encoder
├── services/alert-webhook-service/
├── web/                         # Next.js 工作台
├── infrastructure/              # PostgreSQL / 监控 / 告警
├── deploy/                      # 预发覆盖与 Nginx
├── docs/                        # 需求、架构、验收
├── issues/                      # 产品体验问题登记
├── scripts/                     # 启停、评测、E2E 与治理验收
├── docker-compose.yml
└── .env.example
```

## 快速开始

```bash
cp .env.example .env
docker compose up -d
docker compose ps
curl http://localhost:8080/healthz
bash scripts/e2e-smoke.sh
```

前端默认 `http://localhost:3100`。密钥模板在 `secrets/examples/`，本地真实密钥放
`secrets/dev/`，不要提交。

## 常用验证

| 目的 | 命令 / 文档 |
| --- | --- |
| 发布中心业务矩阵 | `bash scripts/release-center-functional-acceptance.sh` |
| 治理验收 | `bash scripts/governance-acceptance.sh` |
| 确定性 RAG 回归 | `python3 scripts/run-evals.py` |
| 真实模型评测 | `python3 scripts/run-evals.py --real-models`，说明见 [`docs/evals/README.md`](docs/evals/README.md) |
| 查询性能门禁 | `python3 scripts/load-test.py --profile cold-retrieval --requests 40 --concurrency 5`，说明见 [`docs/load-test.md`](docs/load-test.md) |
| 个人身份演示 | `bash scripts/identity-demo-acceptance.sh` |
| 产品体验巡检 | [`docs/product-experience-acceptance.md`](docs/product-experience-acceptance.md) |

发布中心、Agent 预审或 Web 工作台变更后，必须跑发布中心矩阵和产品体验验收，
不能只用单元测试或隔离栈绿报结案。

## 开发

```bash
cd services/etl-worker && make test
cd services/doc-parser-service && pytest -q
cd services/reranker-service && RERANKER_BACKEND=lexical RERANKER_LOAD_ON_STARTUP=false pytest -q
cd web && npm install && npm run dev
```

## 端口

| 服务 | 端口 |
| --- | --- |
| Web | 3100 |
| Query API | 8080 |
| Parser | 8000 |
| Reranker（`rerank` profile） | 8091 |
| Kafka / Kafka UI | 9092 / 8090 |
| Redis Cache / State | 6379 / 6380 |
| PostgreSQL | 5432 |
| Qdrant | 6333 |
| MinIO | 9000 / 9001 |
| Prometheus / Grafana / Alertmanager / Jaeger | 9090 / 3001 / 9093 / 16686 |

## 配置

环境变量以 [`.env.example`](.env.example) 为准。`redis-cache` 可淘汰，`redis-state`
必须 `noeviction`，二者不能指向同一实例。生产环境拒绝 `CORS_ALLOWED_ORIGINS=*`。

## 文档

- 需求：[`docs/software-requirements-specification.md`](docs/software-requirements-specification.md)、[`docs/product-requirements-document.md`](docs/product-requirements-document.md)
- 发布中心验收：[`docs/release-center-functional-acceptance.md`](docs/release-center-functional-acceptance.md)
- 企业身份（未生产启用）：[`docs/enterprise-identity-decision-register.md`](docs/enterprise-identity-decision-register.md)
- CI：[`.github/workflows/ci.yml`](.github/workflows/ci.yml)

## 许可证

MIT
