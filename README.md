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
├── infrastructure/              # 基础设施配置
│   └── prometheus.yml
│
├── scripts/                     # 部署脚本
│   ├── start.sh
│   └── stop.sh
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
  -F "doc_id=test-001" \
  -F "tenant_id=tenant-a" \
  -F "file=@/path/to/document.pdf" \
  -F "permission=read"
```

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
- ✅ Circuit Breaker 熔断器
- ✅ Redis Checkpoint 断点续传
- ✅ DLQ 死信队列
- ✅ OpenTelemetry 追踪
- ✅ Graceful Drain 优雅关闭

### Parser Service (Python)
- ✅ PDF 解析（PyMuPDF）
- ✅ DOCX 解析（python-docx）
- ✅ 自动编码检测（chardet）
- ✅ 语义切块（标题感知）
- ✅ 段落合并 + 重叠切分
- ✅ FastAPI 异步处理

### Query API (Go)
- ✅ JWT 鉴权 + RBAC
- ✅ 租户级限流
- ✅ Hybrid Search（Dense + Sparse）
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

# JWT
JWT_SECRET=your-jwt-secret-change-in-production
```

## 📈 监控与可观测性

- **Prometheus**: http://localhost:9090
- **Jaeger UI**: http://localhost:16686
- **Kafka UI**: http://localhost:8090
- **MinIO Console**: http://localhost:9001

## 🔄 CI/CD

（待添加 GitHub Actions 配置）

## 📝 许可证

MIT

## 🤝 贡献

欢迎提交 Issue 和 Pull Request！
