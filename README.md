# AI-ETL Platform

企业级 AI 知识流水线平台，支持文档解析、向量化、语义检索和 RAG 查询。

## 🏗️ 架构概览

```
┌─────────────────────────────────────────────────────────────┐
│                    AI-ETL Platform                          │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐ │
│  │ ETL Worker   │───▶│ Parser       │    │  Query API   │ │
│  │ (Go)         │    │ Service      │    │  (Go)        │ │
│  │              │    │ (Python)     │    │              │ │
│  │ - Kafka消费   │    │              │    │ - JWT鉴权    │ │
│  │ - 任务编排    │    │ - PDF解析    │    │ - 限流       │ │
│  │ - 向量化调用  │    │ - DOCX解析   │    │ - RAG查询    │ │
│  │ - 入库       │    │ - 语义切块   │    │ - 文件上传   │ │
│  └──────┬───────┘    └──────────────┘    └──────┬───────┘ │
│         │                                        │         │
└─────────┼────────────────────────────────────────┼─────────┘
          │                                        │
          ▼                                        ▼
┌─────────────────────────────────────────────────────────────┐
│                    基础设施层                                 │
│                                                             │
│ Kafka │ Redis(Cache+State) │ Qdrant │ MinIO │ Jaeger │ Prometheus │ Grafana │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

## 📁 项目结构

```
ai-etl-platform/
├── services/
│   ├── etl-worker/              # Go ETL Worker + Query API
│   │   ├── cmd/
│   │   │   ├── worker/          # ETL Worker 入口
│   │   │   └── api/             # Query API 入口
│   │   ├── internal/            # 业务逻辑
│   │   ├── Dockerfile           # Worker 镜像
│   │   ├── Dockerfile.api       # API 镜像
│   │   └── README.md
│   │
│   └── doc-parser-service/      # Python Parser Service
│       ├── app/                 # FastAPI 应用
│       ├── Dockerfile           # Python 服务镜像
│       └── README.md
│
│   └── reranker-service/        # 可选 Cross-Encoder Reranker
│       ├── app/                 # FastAPI rerank API
│       ├── Dockerfile           # CPU 模型服务镜像
│       └── README.md
│
│   └── alert-webhook-service/   # Alertmanager 企业通知适配器
│
├── infrastructure/              # 基础设施配置
│   └── prometheus.yml
│
├── scripts/                     # 部署脚本
│   ├── start.sh
│   ├── stop.sh
│   └── backfill-qdrant-permission.sh  # 历史向量 permission 回填
│
├── docker-compose.yml           # 统一服务编排
├── .env.example                 # 环境变量模板
└── README.md                    # 本文档
```

## 🚀 快速开始

### 1. 环境准备

```bash
# 克隆项目
git clone git@github.com:xjsmx0908/ai-etl-platform.git
cd ai-etl-platform

# 复制环境变量配置
cp .env.example .env
```

### 2. 启动服务

```bash
# 启动所有服务
docker compose up -d

# 查看服务状态
docker compose ps

# 查看日志
docker compose logs -f
```

### 3. 验证服务

```bash
# 健康检查
curl http://localhost:8080/healthz          # Query API
curl http://localhost:8000/healthz          # Parser Service
curl http://localhost:9090                  # Prometheus
curl http://localhost:9093                  # Alertmanager
curl http://localhost:3001                  # Grafana
curl http://localhost:16686                 # Jaeger UI
curl http://localhost:8090                  # Kafka UI
```

### 4. 测试文档解析

```bash
# 上传文档进行解析
curl -X POST http://localhost:8000/api/v1/parse \
  -H "X-Internal-Token: ${PARSER_INTERNAL_TOKEN}" \
  -F "doc_id=test-001" \
  -F "tenant_id=tenant-a" \
  -F "file=@/path/to/document.pdf" \
  -F "permission=internal"
```

### 5. 全链路烟雾测试

```bash
bash scripts/e2e-smoke.sh
```

该脚本会启动本地 `docker compose` 全链路，并用 mock OpenAI 服务验证 `上传 -> Kafka -> 解析 -> 向量化 -> 入库 -> 查询`。

### 企业治理验收

```bash
bash scripts/governance-acceptance.sh
```

该隔离命令从认证后的公共 HTTP 接口验证受管空间上传、双管理员审批、
旧版本连续可见、批准后切换、可恢复删除和审计关联，并通过 Compose 注入
Kafka、进程及存储依赖中断。结果写入 `artifacts/governance-acceptance/`
中的无密钥 JSON 报告；完整场景与证据映射见
[`docs/governance-acceptance.md`](docs/governance-acceptance.md)。此命令是显式
发布验收，不会部署服务或启用 generation retention。

企业身份仍为默认关闭状态。生产 IdP 验收门槛见
[`docs/enterprise-identity-production-acceptance.md`](docs/enterprise-identity-production-acceptance.md)，
会话、MFA、重新认证、退出和双认证迁移的待审批设计见
[`docs/enterprise-session-security-design.md`](docs/enterprise-session-security-design.md)。
两份文档均不代表已选择或启用真实 IdP。

E～I 共 51 项企业决策、稳定审查 ID、责任角色和实现准入状态统一登记在
[`docs/enterprise-identity-decision-register.md`](docs/enterprise-identity-decision-register.md)。
当前全部为 `Pending`；表中的建议基线不是企业批准，合并文档 PR 也不会解锁实现。
个人演示采用独立的
[`docs/personal-demo-identity-profile.md`](docs/personal-demo-identity-profile.md)，其
`DemoApproved` 值只解锁默认关闭的本地实现与测试，不能解锁企业 staging/production。
P2.5-F1 已提供 provider-neutral 会话核心与 PostgreSQL 模型；P2.5-F2 在受保护 HTTP
认证 seam 接受版本化 `Bearer ps1_<opaque-token>`，并从当前用户记录解析 role/scopes。
`SESSION_CORE_ENABLED=false` 仍是默认值，现有 JWT 保持兼容；登录仍只签发原 JWT，
Cookie、OIDC、退出和 production 行为均未迁移到新会话。
P2.5-F3a 已为身份/用户/租户变更、文档删除与发布、Agent 批准和 generation rollback
登记高风险动作；`ps1_` 会话证据达到 10 分钟边界时返回结构化
`401 reauthentication_required`。P2.5-F3b 已在 `oidcauth` seam 实现 Keycloak demo
`acr`/`amr`/`auth_time` 到 `demo-mfa` 的严格转换，以及绑定当前 credential、tenant、
subject、action 的 state/nonce/PKCE 一次性事务；普通与重新认证事务不可混用，Redis
支持跨实例单次消费。该核心尚未连接 HTTP callback，也未轮换凭据或修改 Cookie；当前
运行登录行为保持不变。

紧急访问的待审批保管、短期租约、最小恢复权限和演练设计见
[`docs/enterprise-emergency-access-design.md`](docs/enterprise-emergency-access-design.md)。
该设计不创建紧急账号或密钥，也不提供数据库、shell 或基础设施后门。

租户企业组到知识空间 reader/contributor/manager 的待审批同步、映射、撤权和
迁移设计见
[`docs/enterprise-group-authorization-design.md`](docs/enterprise-group-authorization-design.md)。
raw IdP group claim 不直接授权，也不推导租户或平台角色。

非人类业务客户端、平台内部工作负载、异步委托和基础设施凭据的待审批身份、能力、
轮换与迁移设计见
[`docs/enterprise-workload-identity-design.md`](docs/enterprise-workload-identity-design.md)。
机器身份不能复用浏览器会话、伪装用户或继承平台 admin。

选定身份提供方、权威 Users 对账、隔离 staging 组合验收、证据签名与后续生产晋级
设计见
[`docs/enterprise-identity-provider-staging-design.md`](docs/enterprise-identity-provider-staging-design.md)。
当前 Keycloak 结果仅为 OIDC 协议基线，F～I 仍待实现；文档批准不等于生产启用。

### 6. 评测闭环

```bash
python3 scripts/run-evals.py
```

该脚本默认用 mock 模型跑确定性回归，输出 JSON/Markdown 报告。

**两种模型模式，指标含义不同：**

| 模式 | 命令 | 指标含义 |
| --- | --- | --- |
| mock（默认） | `python3 scripts/run-evals.py` | **仅验证链路接通**。embedding 由 hash 派生、无语义结构，Recall/hit rate 不能作为检索质量证据。适合做 CI 门禁：确定性、免费、快。 |
| real | `python3 scripts/run-evals.py --real-models` | **唯一能说明检索质量的模式**。使用真实 embedding 与 LLM 端点。 |

真实模式从 CLI 参数、环境变量、`*_FILE_PATH` secrets 依次解析配置（与 Go 服务的 `KEY` > `KEY_FILE` 优先级一致）：

```bash
export EMBED_ENDPOINT=http://host.docker.internal:11434/api/embeddings
export EMBED_MODEL=nomic-embed-text
export LLM_ENDPOINT=https://your-provider/v1
export LLM_API_KEY_FILE_PATH=./secrets/dev/llm_api_key
export LLM_MODEL=your-model

python3 scripts/run-evals.py --real-models --embed-dim 768
```

真实模式会自动派生独立的 Qdrant collection（如 `documents-real-nomic-embed-text-768`）。这是必须的：collection 的向量维度创建后不可修改，mock 用 8 维而真实模型用原生维度，共用会直接冲突。

报告头部会标注 `Model mode`，mock 报告显式声明「NOT a quality signal」，避免数字被误读为质量结论。

可选 LLM-as-a-Judge：

```bash
JUDGE_API_KEY=... JUDGE_MODEL=gpt-4o python3 scripts/run-evals.py --judge --judge-max-cases 10
```

Judge 评测输出 Faithfulness、Correctness、Relevance 和总体通过率；常规 CI 仍使用确定性评测，手动工作流位于 `.github/workflows/judge-eval.yml`。

### 7. 轻量压测

```bash
python3 scripts/load-test.py --requests 40 --concurrency 5
```

该脚本用于快速观察 `/v1/query` 的延迟、错误率和命中率。

## 🔧 开发指南

### 前端产品（web/）

本地开发：

```bash
cd web
cp .env.local.example .env.local   # 配置 NEXT_PUBLIC_API_BASE
npm install
npm run dev                        # http://localhost:3000
```

认证：登录页（`/login`）支持本地密码或显式启用的企业 OIDC。两种方式最终都只把
平台 session token 存入 HttpOnly cookie；`app/api/*` route handler 从 cookie 读
token 代理到 query-api（`/v1/*`），SSE 流式透传，无跨域。OIDC 使用 Authorization
Code + PKCE、state、nonce、严格 issuer/audience/RS256/JWKS 校验；IdP Token 不返回
浏览器 JavaScript。
首启 bootstrap admin 由后端 `BOOTSTRAP_ADMIN_*` 创建；admin 登录后在「用户管理」创建用户。

认证后的授权上下文由 provider-neutral `Principal` 提供。`production` 会拒绝
测试/旧格式 token、未知或停用的内部用户，并从内部用户记录重新计算租户、角色和
capabilities；非生产环境保留显式评测 token 兼容。OIDC 默认关闭，启用后仅已由管理
员绑定 `(issuer, subject)` 的活动内部用户可登录；production 启用 OIDC 时普通本地
密码登录自动关闭。默认关闭的 SCIM Users 子集现在可原子地配置和停用精确 OIDC
身份，但 JIT、组授权、服务身份、break-glass 和企业 MFA 策略仍不在本切片内；生产
SCIM/OIDC 还必须通过选定 IdP 的验收。

Keycloak 验收配置示例：创建 confidential OIDC client，将 Valid Redirect URI 精确
设为 `https://<RAG 域名>/api/auth/oidc/callback`，把 realm issuer、client ID 和 secret
分别配置为 `OIDC_ISSUER`、`OIDC_CLIENT_ID` 与
`OIDC_CLIENT_SECRET_FILE_PATH`；先用管理员 API 建立 external identity binding，再在
staging 设置 `OIDC_ENABLED=true`。不要从 realm role、group 或邮箱域名自动派生权限。

SCIM 配置固定 `SCIM_CONNECTOR_ID`、`SCIM_TENANT_ID`、`SCIM_ISSUER`、
`SCIM_SUBJECT_ATTRIBUTE=externalId` 和最小权限 `SCIM_DEFAULT_ROLE`。将一个或多个逗号
分隔的凭据写入 `SCIM_BEARER_TOKENS_FILE_PATH` 指向的文件；新旧凭据重叠可完成轮换。
在选定 IdP 的稳定 subject 映射以及 create/update/deactivate/reactivate-to-OIDC 全流程
通过 staging 验收前，保持 `SCIM_ENABLED=false`。
生产 IdP 比较、策略审批和证据要求见
[Enterprise Identity Production Acceptance](docs/enterprise-identity-production-acceptance.md)。

部署（compose 服务）：

```bash
# 1. 在 .env 设 BOOTSTRAP_ADMIN_USERNAME/PASSWORD/TENANT（首启自动建初始 admin）
# 2. 启动整套 + 前端
docker compose up -d --build
# 3. 灌入语义集语料（44 篇文档，经登录鉴权）
python3 scripts/load-corpus.py --api-base http://localhost:8080 --username admin --password <pw>
```

前端在 `WEB_HOST_PORT`（默认 3100，3000 被其他项目占用时用 3100）。浏览器只访问前端端口，
`/api/*` 由 Next route handler 代理到 query-api（`/v1/*`），SSE 流式透传，无跨域。
功能：问答（SSE 流式 + 引用展开）、文档管理（列表/搜索/删除）、用户管理（admin）、
数据接入、系统可观测、检索质量、Agent 文档发布治理（受管草稿的精确版本/索引代次
检查、四眼审批、事务化幂等发布与审计）。

受管文档的替换会先建立新的版本和索引代次，但不会立即影响线上查询；旧的已发布版本
会持续提供结果，直到新的精确候选通过独立审批并完成原子切换。问答和全文搜索都只读取
PostgreSQL 发布记录指向的健康代次。默认 `user-uploads` 空间在摄取完成后通过同一发布
记录自动发布，重放安全；替换上传不得改变文档权限或知识空间。

删除接口采用异步可恢复语义：`DELETE /v1/documents/{id}` 成功接纳后返回 HTTP `202`
和 `job_id`，并立即撤销该文档的查询发布权。worker 随后幂等清理 Qdrant、
Elasticsearch 和接纳时记录的精确 MinIO object keys；部分失败会保留进度并自动重试，
所有依赖均确认成功后才删除 PostgreSQL 权威记录。

公网 HTTPS 入口为 `https://rag.ipuau.com`。宿主机 Nginx 配置模板位于
`deploy/nginx/rag.ipuau.com.conf`，反向代理到 Web Compose 服务的宿主机端口
`3100`。HTTPS 部署必须在本地 `.env` 设置 `COOKIE_SECURE=true`；证书由宿主机
Certbot 管理并通过 systemd timer 自动续期。

### Go 服务开发

```bash
cd services/etl-worker

# 编译
make build

# 本地运行
make run-worker
make run-api

# 测试
make test
```

### Python 服务开发

```bash
cd services/doc-parser-service

# 创建虚拟环境
python -m venv venv
source venv/bin/activate  # Linux/Mac
venv\Scripts\activate     # Windows

# 安装依赖
pip install -r requirements.txt

# 运行服务
python -m app.main

# 访问 API 文档
# http://localhost:8000/docs
```

## 📊 服务端口

| 服务 | 端口 | 说明 |
|------|------|------|
| Query API | 8080 | HTTP API（查询 + 上传） |
| Parser Service | 8000 | 文档解析服务 |
| Reranker Service | 8091 | 可选 Cross-Encoder 重排服务（`rerank` profile） |
| Kafka | 9092 | 消息队列 |
| Redis Cache | 6379 | 语义检索缓存（`allkeys-lru`，可淘汰） |
| Redis State | 6380 | Agent run / fencing token / 幂等 / Checkpoint（`noeviction` + AOF）；Agent 审批记录存 PostgreSQL |
| Qdrant | 6333 | 向量数据库 |
| MinIO | 9000/9001 | 对象存储（API/Console） |
| Prometheus | 9090 | 指标监控 |
| Alertmanager | 9093 | 告警聚合与路由（仅本机监听） |
| Grafana | 3001 | 监控大屏（容器内 3000，仅本机监听） |
| Jaeger | 16686 | 分布式追踪 |
| Kafka UI | 8090 | Kafka 管理界面 |

## 🎯 核心特性

### ETL Worker (Go)
- ✅ Kafka 消费 + 手动 Commit
- ✅ Worker Pool 并发处理
- ✅ S3/MinIO 对象键自动落盘解析
- ✅ Circuit Breaker 熔断器
- ✅ Redis Checkpoint 断点续传
- ✅ DLQ 死信队列
- ✅ DLQ 成功后再 Commit（失败可重试）
- ✅ OpenTelemetry 追踪
- ✅ Graceful Drain 优雅关闭

### Parser Service (Python)
- ✅ PDF 解析（PyMuPDF）
- ✅ 旧版 DOC 解析（LibreOffice 隔离转换）
- ✅ DOCX 解析（python-docx）
- ✅ 自动编码检测（chardet）
- ✅ 语义切块（标题感知）
- ✅ 段落合并 + 重叠切分
- ✅ 上传文件大小限制 + 扩展名白名单
- ✅ FastAPI 异步处理

### Query API (Go)
- ✅ JWT 鉴权 + RBAC
- ✅ 租户级限流
- ✅ Query Router（精确锚点 / 语义 / 混合意图）
- ✅ Scatter-Gather 多路召回（Qdrant Dense+Sparse + Elasticsearch BM25）
- ✅ RRF 融合去重 + 单路故障降级
- ✅ 可插拔 HTTP Cross-Encoder Reranker + exact-match pin 保护
- ✅ 业务 metadata schema exact evidence（订单号、合同号、trace、客户参考号等）
- ✅ Redis 语义缓存（tenant + permission scope 隔离）
- ✅ RAG 查询（向量检索 + LLM）
- ✅ MinIO 文件上传
- ✅ 文档删除（`DELETE /v1/documents/{doc_id}`，级联清理 Qdrant 向量、ES 文档、MinIO 对象）
- ✅ API 版本控制
- ✅ CORS 支持

## 🔐 环境变量

复制 `.env.example` 为 `.env` 并配置：

```bash
# 环境
ENVIRONMENT=dev

# Kafka
KAFKA_BROKERS=kafka:9092
KAFKA_TOPIC=doc-processing
KAFKA_GROUP_ID=etl-pipeline

# Redis（缓存与状态分离，见下方说明）
REDIS_CACHE_ADDR=redis-cache:6379
REDIS_CACHE_DB=0
REDIS_STATE_ADDR=redis-state:6379
REDIS_STATE_DB=0

# Embedding
EMBED_ENDPOINT=http://host.docker.internal:11434/api/embeddings
EMBED_MODEL=nomic-embed-text
EMBED_DIMENSION=768

# LLM
LLM_ENDPOINT=http://host.docker.internal:11434/v1
LLM_MODEL=qwen2.5:7b

# 模型选型（能力/延迟/成本三角，按场景取不同的模型）：
#   - 在线回答（RAG 生成）：要求低延迟与稳定输出，默认 deepseek-v4-flash 这类轻量模型
#   - Agent planner：需要工具调用与多步推理，可用更高档模型（AGENT_PLANNER_MODEL）
#   - LLM-as-a-Judge：离线评测，对质量敏感，可选最强模型（JUDGE_MODEL）
# 不是「一个模型打天下」；每个角色独立配置，默认值仅作 fallback，生产用环境变量覆盖。

# Retrieval Gateway
RETRIEVAL_TIMEOUT=300ms
RETRIEVAL_CANDIDATE_K=50
RETRIEVAL_FINAL_TOP_K=5
RETRIEVAL_ENABLE_ES=true
RETRIEVAL_ENABLE_RERANK=false
RETRIEVAL_RERANK_POLICY=auto
RETRIEVAL_EXACT_SCHEMA_FIELDS=doc_id,chunk_id,order_id,order_no,contract_id,contract_no,ticket_id,invoice_no,trace_id,request_id,customer_ref,email,phone,sku,user_id
RERANK_ENDPOINT=
RERANK_MODEL=bge-reranker-base
RERANKER_MODEL=cross-encoder/ms-marco-MiniLM-L6-v2
RERANKER_DEVICE=cpu
SEMANTIC_CACHE_ENABLED=true
SEMANTIC_CACHE_THRESHOLD=0.92

# 直接环境变量（可选，优先级高于 *_FILE）
JWT_SECRET=
PARSER_INTERNAL_TOKEN=

# 上传限制（Query API）
MAX_UPLOAD_SIZE_MB=512
MULTIPART_MAX_MEMORY_MB=4

# Docker secrets 文件路径覆盖（可选）
# JWT_SECRET_FILE_PATH=./secrets/dev/jwt_secret
# PARSER_INTERNAL_TOKEN_FILE_PATH=./secrets/dev/parser_internal_token
# ALERT_WEBHOOK_TOKEN_FILE_PATH=./secrets/dev/alert_webhook_token
# WECOM_WEBHOOK_URL_FILE_PATH=./secrets/dev/wecom_webhook_url
# DINGTALK_WEBHOOK_URL_FILE_PATH=./secrets/dev/dingtalk_webhook_url
# DINGTALK_SECRET_FILE_PATH=./secrets/dev/dingtalk_secret
# GRAFANA_ADMIN_PASSWORD_FILE_PATH=./secrets/dev/grafana_admin_password
```

启用本地 CPU reranker：

```bash
RETRIEVAL_ENABLE_RERANK=true \
RETRIEVAL_RERANK_POLICY=auto \
RERANK_ENDPOINT=http://reranker-service:8091/rerank \
docker compose --profile rerank up -d --build reranker-service query-api
```

`RETRIEVAL_RERANK_POLICY=auto` 是推荐的企业级默认策略：明确的精确编号、订单、错误码、trace 等路由直接保留 BM25/向量融合排序；语义/混合查询会调用 Cross-Encoder，但如果候选的 `doc_id`、`chunk_id`、正文或配置的业务 `metadata` 字段命中 query 里的强 exact token，最终会 pin exact-match 候选，避免被非 exact 候选超过。需要做离线对比实验时可设为 `always` 复现裸 rerank 行为。

上传业务字段时使用 multipart 字段 `metadata`，值为 JSON object，例如 `{"contract_no":"CN-2026-0001","customer_ref":"x9k-77q-plum"}`；参与 exact evidence 的字段由 `RETRIEVAL_EXACT_SCHEMA_FIELDS` 控制。

敏感配置读取优先级：`KEY` > `KEY_FILE` > 默认值。`docker-compose.yml` 已为 `query-api`、`etl-worker`、`parser-service` 挂载 secrets，默认占位文件在 `secrets/examples/`，建议复制到 `secrets/dev/` 后替换为真实值。

### Redis 缓存与状态分离

平台使用两个 Redis 实例，职责不可混用：

| 实例 | 淘汰策略 | 存放内容 | 丢数据的后果 |
| --- | --- | --- | --- |
| `redis-cache` | `allkeys-lru` | 语义检索缓存 | 多做一次检索，无正确性影响 |
| `redis-state` | `noeviction` + AOF | Agent run 状态、fencing token、幂等键、Checkpoint、任务状态、ES 重试队列 | **破坏正确性** |

分离的原因是正确性而非容量：`allkeys-lru` 会淘汰任意 key。若 fencing token 被淘汰后重置，`agent.Orchestrator` 依赖的 `lease.FencingToken > run.FencingToken` 判断将无法再拒绝陈旧写入，durable run 的并发安全保证失效。审批记录已独立持久化到 PostgreSQL；Redis 状态实例仍必须 `noeviction`，宁可写入失败并显式报错，也不能静默丢运行状态。

配置优先级：`REDIS_CACHE_*` / `REDIS_STATE_*` > `REDIS_*`（共享回退，便于本地单实例调试）。生产环境启动校验会拒绝两者指向同一实例与 DB。

## 📈 监控与可观测性

- **Prometheus**: http://localhost:9090
- **Alertmanager**: http://localhost:9093
- **Grafana**: http://localhost:3001（预置 `AI ETL Platform Overview`）
- **Jaeger UI**: http://localhost:16686
- **Kafka UI**: http://localhost:8090
- **MinIO Console**: http://localhost:9001
- Query API 会继承 W3C `traceparent`，并通过响应头 `X-Trace-ID` 返回当前 TraceID。
- Jaeger Query 链路包含 HTTP、Query、Embedding、Cache、Route、Qdrant/Elasticsearch、Fusion、Rerank、Prompt 与 LLM 阶段 Span。
- Prometheus 暴露 LLM 请求结果、延迟和进程级连续失败次数；Query API 启动时会先暴露配置模型的连续失败值 `0`，连续 5 次失败、错误率和 p95 延迟由 Alertmanager 告警。
- PostgreSQL 持久化摄取状态会定期映射为低基数指标：待发布/已重试 outbox 数量、最老事件年龄、各 job 状态数量和过期 processing lease；持续积压、重试或过期 lease 会触发 Alertmanager。采样周期由 `INGESTION_METRICS_INTERVAL` 控制。
- Query API 定期清理超过 `ORPHAN_CLEANUP_GRACE_PERIOD` 的 versioned 对象，但只有 PostgreSQL 能证明其既不在当前目录、也不属于任何已接纳 job 时才删除；批次和周期分别由 `ORPHAN_CLEANUP_BATCH_SIZE`、`ORPHAN_CLEANUP_INTERVAL` 控制。
- 每次成功的 LLM 调用会记录 token 消耗（`ai_etl_llm_tokens_total{model,kind}`）。配置 `LLM_PRICE_PROMPT_PER_1K` / `LLM_PRICE_COMPLETION_PER_1K` 后，还会累计估算成本（`ai_etl_llm_cost_usd_total{model}`）；未配置价格时成本指标保持 0，避免误报。
- 企业微信/钉钉 webhook 放在 `secrets/dev/`，由内部 `alert-webhook-service` 转换消息并发送；不要把真实 webhook 提交到 Git。
- Query API 的 `/v1/query` 支持 SSE 流式输出（客户端发送 `Accept: text/event-stream` 即触发）。事件流：先发 `sources` 事件（引用可即时渲染），再逐段发 `delta` 事件，最后 `done` 事件携带 token 用量。流式调用会记录首 Token 时间（TTFT）到 trace span，为未来 TTFT 告警提供数据基础。非流式调用（默认）行为不变。

## 🔄 CI/CD

- ✅ 已配置 GitHub Actions 工作流：`.github/workflows/ci.yml`
- ✅ 已配置全链路烟雾测试工作流：`.github/workflows/e2e-smoke.yml`
- 自动触发：`push`（`main/master`）与 `pull_request`
- 覆盖检查：
  - Go：`gofmt`、`go vet`、`go test ./...`
  - Python：`pytest -q`
  - 部署配置：`docker compose config`
  - 安全门禁：Trivy（`CRITICAL` 漏洞/配置）
- ✅ 已配置镜像发布工作流：`.github/workflows/cd.yml`
  - 触发：`main/master` push、`v*` tag、手动触发
  - 推送镜像：`ghcr.io/<owner>/ai-etl-platform-etl-worker`
  - 推送镜像：`ghcr.io/<owner>/ai-etl-platform-query-api`
  - 推送镜像：`ghcr.io/<owner>/ai-etl-platform-parser-service`
- ✅ 已配置手动预发部署工作流：`.github/workflows/deploy-staging.yml`
  - 依赖 `deploy/docker-compose.staging-images.yml` 进行 GHCR 镜像覆盖
  - 需要预先配置 GitHub Environment `staging` secrets：
    - `STAGING_SSH_HOST`
    - `STAGING_SSH_PORT`（可选，默认 `22`）
    - `STAGING_SSH_USER`
    - `STAGING_SSH_KEY`
    - `STAGING_APP_DIR`
    - `STAGING_GHCR_USERNAME`
    - `STAGING_GHCR_TOKEN`
- 🔒 PR 门禁策略说明见：`.github/BRANCH_PROTECTION.md`（在仓库 Settings 中启用）

## 📝 许可证

MIT

## 🤝 贡献

欢迎提交 Issue 和 Pull Request！
