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
│  Kafka │ Redis │ Qdrant │ MinIO │ Jaeger │ Prometheus      │
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

### 6. 评测闭环

```bash
python3 scripts/run-evals.py
```

该脚本会用 `docs/evals/golden-set.json` 做确定性 retrieval 回归评测，并输出 JSON/Markdown 报告。

### 7. 轻量压测

```bash
python3 scripts/load-test.py --requests 40 --concurrency 5
```

该脚本用于快速观察 `/v1/query` 的延迟、错误率和命中率。

## 🔧 开发指南

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
| Redis | 6379 | 缓存 + Checkpoint |
| Qdrant | 6333 | 向量数据库 |
| MinIO | 9000/9001 | 对象存储（API/Console） |
| Prometheus | 9090 | 指标监控 |
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
- ✅ 可插拔 HTTP Cross-Encoder Reranker
- ✅ Redis 语义缓存（tenant + permission scope 隔离）
- ✅ RAG 查询（向量检索 + LLM）
- ✅ MinIO 文件上传
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

# Embedding
EMBED_ENDPOINT=http://host.docker.internal:11434/api/embeddings
EMBED_MODEL=nomic-embed-text
EMBED_DIMENSION=768

# LLM
LLM_ENDPOINT=http://host.docker.internal:11434/v1
LLM_MODEL=qwen2.5:7b

# Retrieval Gateway
RETRIEVAL_TIMEOUT=300ms
RETRIEVAL_CANDIDATE_K=50
RETRIEVAL_FINAL_TOP_K=5
RETRIEVAL_ENABLE_ES=true
RETRIEVAL_ENABLE_RERANK=false
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
```

启用本地 CPU reranker：

```bash
RETRIEVAL_ENABLE_RERANK=true \
RERANK_ENDPOINT=http://reranker-service:8091/rerank \
docker compose --profile rerank up -d --build reranker-service query-api
```

敏感配置读取优先级：`KEY` > `KEY_FILE` > 默认值。`docker-compose.yml` 已为 `query-api`、`etl-worker`、`parser-service` 挂载 secrets，默认占位文件在 `secrets/examples/`，建议复制到 `secrets/dev/` 后替换为真实值。

## 📈 监控与可观测性

- **Prometheus**: http://localhost:9090
- **Jaeger UI**: http://localhost:16686
- **Kafka UI**: http://localhost:8090
- **MinIO Console**: http://localhost:9001

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
