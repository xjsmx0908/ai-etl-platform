# AI ETL Pipeline

基于 Go 的**企业级文档知识流水线**，提供完整的文件上传→语义切块→向量化→混合检索→RAG 问答能力。

## 架构概览

```
                        ┌─────────────────────────────────────────────┐
                        │              AI ETL Pipeline                 │
                        │                                             │
HTTP POST /upload ─────►│  Gateway ──► Kafka ──► Worker Pool          │
                        │                         ├─ Parser (语义切块)  │
                        │                         ├─ Embedder (向量化)  │
                        │                         ├─ BM25 Sparse       │
                        │                         └─ Qdrant Upsert     │
                        │                                             │
HTTP POST /query ──────►│  RAG Query Service                          │
                        │    ├─ Retrieval Gateway                     │
                        │    │   ├─ Query Router                      │
                        │    │   ├─ Qdrant + Elasticsearch Recall     │
                        │    │   ├─ RRF Fusion + Reranker             │
                        │    │   └─ Redis Semantic Cache              │
                        │    └─ LLM Generation                        │
                        │                                             │
                        │  Redis (Checkpoint)  ·  Kafka DLQ (死信)     │
                        └─────────────────────────────────────────────┘
```

---

## 一、部署前准备工作

### 1.1 准备模型通道（OpenAI 兼容或本地 Ollama）

本项目需要两个模型通道：
- Embedding（文档入库向量化 + `/query` 问题向量化）
- Chat Completion（`/query` 最终答案生成）

| 通道 | 关键环境变量 | 用途 | 备注 |
|------|--------------|------|------|
| Embedding | `EMBED_ENDPOINT` / `EMBED_MODEL` / `EMBED_API_KEY` | 文本向量化 | 支持 OpenAI 兼容接口；也支持 Ollama 原生 `/api/embeddings` |
| LLM Chat | `LLM_ENDPOINT` / `LLM_MODEL` / `LLM_API_KEY` | RAG 问答生成 | 需要 `/chat/completions` 协议（可由服务自动补全路径） |

如果使用 OpenAI，可在 [platform.openai.com](https://platform.openai.com/api-keys) 获取 Key。

### 1.2 准备待处理的文档

文档可通过两种方式进入系统：

| 方式 | 说明 |
|------|------|
| **HTTP 上传**（推荐） | 通过 `POST /upload` 接口上传文件，系统自动存储并推入 Kafka |
| Kafka 消息 | 直接向 Kafka Topic 发送包含文件路径的 JSON 消息 |

支持的格式：`.pdf`（需 poppler-utils）、`.docx`（需 pandoc）、`.txt` / `.md`（直读）

### 1.3 安装 Docker 和 Docker Compose

```bash
# 安装 Docker（如果尚未安装）
curl -fsSL https://get.docker.com | sh
sudo systemctl enable --now docker

# 验证版本（需要 Docker Compose V2）
docker --version       # >= 20.10
docker compose version # >= 2.0
```

### 1.4 克隆项目代码

```bash
git clone <你的仓库地址> ai-etl-pipeline
cd ai-etl-pipeline
```

### 1.5 系统依赖（二进制解析工具）

Docker 镜像中已内置，本地开发需安装：

```bash
# Ubuntu/Debian
sudo apt install poppler-utils pandoc

# macOS
brew install poppler pandoc
```

---

## 二、配置环境变量

### 2.1 创建 `.env` 文件

```bash
cp .env.example .env
```

### 2.2 必须修改的配置项

编辑 `.env`，以下是 Docker Compose 全链路联调的推荐配置：

```bash
# 【关键】建议使用 staging（dev 会启用 mock source + 内存存储，不走完整链路）
ENVIRONMENT=staging

# 【关键】容器访问宿主机 Ollama（如使用本地 embedding）
EMBED_ENDPOINT=http://host.docker.internal:11434/api/embeddings
EMBED_MODEL=nomic-embed-text
EMBED_API_KEY=

# 【关键】LLM endpoint 支持 base URL，服务会自动补全 /chat/completions
LLM_ENDPOINT=https://your-llm-provider.example/v1
LLM_API_KEY=your-llm-api-key
LLM_MODEL=your-chat-model

# 如果使用 OpenAI Embedding，可改回：
# EMBED_ENDPOINT=https://api.openai.com/v1/embeddings
# EMBED_API_KEY=sk-your-real-api-key-here
```

### 2.3 可选调优配置

```bash
# Worker 并发数（根据 CPU 核心数调整，建议 CPU核数 * 2）
PIPELINE_MAX_WORKERS=10

# 批处理大小（影响 Embedding API 调用频率）
PIPELINE_BATCH_SIZE=10

# Embedding 限流（按你的服务吞吐能力调节）
EMBED_RATE_LIMIT=50

# Redis 密码（如果设置了 Redis 密码）
REDIS_PASSWORD=your-redis-password
```

### 2.4 完整配置说明

| 环境变量 | 默认值 | 说明 |
|----------|--------|------|
| **运行环境** | | |
| `ENVIRONMENT` | `dev` | `dev`/`staging`/`production` |
| **Pipeline 调度** | | |
| `PIPELINE_MAX_WORKERS` | `10` | Worker 并发数 |
| `PIPELINE_BATCH_SIZE` | `10` | 每批处理的 Chunk 数 |
| `PIPELINE_TIMEOUT` | `5m` | 单任务总超时 |
| `PIPELINE_STAGE_TIMEOUT` | `30s` | 单阶段超时 |
| `PIPELINE_MAX_RETRIES` | `3` | 失败重试次数 |
| `PIPELINE_RETRY_BACKOFF` | `500ms` | 重试退避基础间隔 |
| **Parser 切块** | | |
| `PARSER_MAX_CHUNK_SIZE` | `4096` | Chunk 最大字符数 |
| `PARSER_CHUNK_OVERLAP` | `200` | Chunk 重叠字符数 |
| `PARSER_INTERNAL_TOKEN` | _(空)_ | Parser Service 内部鉴权 token（非 dev 建议必配） |
| **Embedding API** | | |
| `EMBED_ENDPOINT` | `https://api.openai.com/v1/embeddings` | Embedding 接口地址（支持 Ollama `/api/embeddings`） |
| `EMBED_API_KEY` | _(空)_ | API Key（**生产必填**） |
| `EMBED_MODEL` | `text-embedding-ada-002` | 模型名称 |
| `EMBED_DIMENSION` | `1536` | 向量维度 |
| `EMBED_RATE_LIMIT` | `50` | 请求限流（req/s） |
| `EMBED_MAX_RETRIES` | `5` | 最大重试次数 |
| **LLM Chat（RAG 问答）** | | |
| `LLM_ENDPOINT` | `https://api.openai.com/v1/chat/completions` | LLM 接口（支持填写 base URL，如 `.../v1`） |
| `LLM_API_KEY` | _(空)_ | API Key（**生产必填**） |
| `LLM_MODEL` | `gpt-4o-mini` | 模型名称 |
| `LLM_MAX_TOKENS` | `1024` | 最大生成 Token |
| **Qdrant 向量库** | | |
| `STORE_ENDPOINT` | `http://localhost:6333` | Qdrant 地址 |
| `STORE_API_KEY` | _(空)_ | Qdrant Cloud 鉴权 |
| `STORE_COLLECTION` | `documents` | 集合名 |
| **Elasticsearch（最终一致性全文索引）** | | |
| `ES_ADDRESS` | `http://elasticsearch:9200` | Elasticsearch 地址 |
| `ES_API_KEY` | _(空)_ | Elasticsearch API Key（可选） |
| `ES_INDEX` | `documents_text` | 全文索引名称 |
| `ES_QUEUE_KEY` | `es:index:retry` | ES 重试队列 Redis Key |
| `ES_DEADLETTER_KEY` | `es:index:deadletter` | ES 死信队列 Redis Key |
| `ES_REPLAY_PERIOD` | `2s` | 重放轮询周期 |
| `ES_MAX_RETRIES` | `12` | 单条消息最大重试次数 |
| `ES_RETRY_BASE_BACKOFF` | `2s` | 重试基础退避时间 |
| `ES_RETRY_MAX_BACKOFF` | `5m` | 重试最大退避时间 |
| `ES_RETRY_JITTER` | `0.2` | 退避抖动比例（0~1） |
| **BM25 Sparse（混合检索）** | | |
| `SPARSE_K1` | `1.2` | BM25 k1 参数 |
| `SPARSE_B` | `0.75` | BM25 b 参数 |
| `SPARSE_AVG_DL` | `256` | 平均文档长度 |
| **Retrieval Gateway（多路召回）** | | |
| `RETRIEVAL_TIMEOUT` | `300ms` | Scatter-Gather 召回总超时 |
| `RETRIEVAL_CANDIDATE_K` | `50` | 每路粗召回候选数量 |
| `RETRIEVAL_FINAL_TOP_K` | `5` | 请求未传 `top_k` 时的默认上下文数量 |
| `RETRIEVAL_ENABLE_ES` | `true` | 是否启用 Elasticsearch BM25 召回 |
| `RETRIEVAL_ENABLE_RERANK` | `false` | 是否启用 HTTP Cross-Encoder Reranker |
| `RETRIEVAL_RERANK_POLICY` | `auto` | Rerank 策略：`auto` 跳过精确编号/关键词查询，并在候选包含 query 精确 token 时保护融合排序；`always` 对所有候选重排 |
| `RERANK_ENDPOINT` | _(空)_ | Reranker HTTP 端点 |
| `RERANK_API_KEY` | _(空)_ | Reranker API Key（可选） |
| `RERANK_MODEL` | `bge-reranker-base` | Reranker 模型名 |
| `RERANKER_MODEL` | `cross-encoder/ms-marco-MiniLM-L6-v2` | 可选本地 reranker-service 模型 |
| `RERANKER_DEVICE` | `cpu` | 可选本地 reranker-service 推理设备 |
| `SEMANTIC_CACHE_ENABLED` | `true` | 是否启用 Redis 语义缓存 |
| `SEMANTIC_CACHE_TTL` | `10m` | 检索缓存 TTL |
| `SEMANTIC_CACHE_THRESHOLD` | `0.92` | 语义缓存向量相似度阈值 |
| `SEMANTIC_CACHE_MAX_ENTRIES` | `128` | 每个租户权限范围保留的最近查询数量 |
| **Kafka** | | |
| `KAFKA_BROKERS` | `localhost:9092` | Broker 地址 |
| `KAFKA_TOPIC` | `doc-processing` | 输入 Topic |
| `KAFKA_GROUP_ID` | `etl-pipeline` | 消费者组 |
| `KAFKA_DLQ_TOPIC` | `doc-processing-dlq` | 死信 Topic |
| **Redis** | | |
| `REDIS_ADDR` | `localhost:6379` | Redis 地址 |
| `REDIS_PASSWORD` | _(空)_ | 密码 |
| `REDIS_DB` | `0` | 数据库编号 |
| **API 安全** | | |
| `CORS_ALLOWED_ORIGINS` | `*`(dev) / 空(非dev) | 逗号分隔 CORS allowlist（production 不允许 `*`） |
| `HTTP_READ_TIMEOUT` | `15s` | 请求体读取超时 |
| `HTTP_READ_HEADER_TIMEOUT` | `10s` | 请求头读取超时 |
| `HTTP_WRITE_TIMEOUT` | `60s` | 响应写入超时 |
| `HTTP_IDLE_TIMEOUT` | `120s` | Keep-Alive 空闲超时 |
| `HTTP_MAX_HEADER_BYTES` | `1048576` | 最大请求头大小 |
| `IDEMPOTENCY_TTL` | `24h` | 上传幂等记录保留时长 |
| **HTTP 服务 & 网关** | | |
| `HEALTH_PORT` | `8080` | 服务端口 |
| `UPLOAD_DIR` | `/data/uploads` | 文件上传存储目录 |
| `MAX_UPLOAD_SIZE_MB` | `512` | 最大上传文件大小(MB) |

敏感变量支持 `*_FILE` 读取（例如 `JWT_SECRET_FILE`、`EMBED_API_KEY_FILE`、`LLM_API_KEY_FILE`、`STORE_API_KEY_FILE`、`ES_API_KEY_FILE`、`REDIS_PASSWORD_FILE`、`S3_ACCESS_KEY_FILE`、`S3_SECRET_KEY_FILE`、`PARSER_INTERNAL_TOKEN_FILE`）。读取优先级：`KEY` > `KEY_FILE` > 默认值。

### 2.5 Docker Compose 关键说明

- `docker-compose.yml` 中应用容器已配置 `extra_hosts: host.docker.internal:host-gateway`，用于容器访问宿主机服务（如 Ollama）。
- `UPLOAD_DIR` 推荐保持 `/data/uploads`，与镜像内目录及 volume 挂载一致。
- `ENVIRONMENT=dev` 仅用于本地调试（会启用 mock source + 内存存储）；验证“上传 -> 消费 -> 入库 -> 检索 -> 生成”请使用 `staging` 或 `production`。
- `docker-compose.yml` 已配置 secrets 挂载，默认占位文件在 `secrets/examples/`；建议复制到 `secrets/dev/` 后，通过 `.env` 中 `*_FILE_PATH` 覆盖为本机私有文件。
- `production` 环境会拒绝 `CORS_ALLOWED_ORIGINS=*`，请配置明确域名白名单。

---

## 三、Docker Compose 部署

### 3.1 启动全套服务

```bash
# 后台启动（包含 Kafka + Redis + Qdrant + Elasticsearch + Pipeline + Kafka-UI）
docker compose up -d
```

首次启动会自动：
- 拉取 Kafka、Redis、Qdrant、Elasticsearch、Kafka-UI 镜像
- 编译 Go 源码为 Docker 镜像
- 按依赖顺序启动（基础设施就绪后才启动应用）

当前 `docker-compose.yml` 使用的关键镜像版本：
- Kafka: `bitnamilegacy/kafka:3.7.1`
- Redis: `redis:7-alpine`
- Qdrant: `qdrant/qdrant:v1.14.1`
- Elasticsearch: `docker.elastic.co/elasticsearch/elasticsearch:8.14.3`
- Kafka UI: `provectuslabs/kafka-ui:latest`

### 3.2 确认服务状态

```bash
# 查看所有容器状态
docker compose ps

# 期望输出：所有服务 Status 为 "Up" 且 healthy
```

| 服务 | 端口 | 健康检查 |
|------|------|----------|
| kafka | 9092 | Topic 列表可查询 |
| redis | 6379 | PING → PONG |
| qdrant | 6333 | TCP 6333 可连接 |
| etl-worker | - | 后台服务，无HTTP端点 |
| query-api | 8080 | /healthz → 200 |
| kafka-ui | 8090 | Web 界面可访问 |

### 3.3 验证服务健康

```bash
# Pipeline 健康检查
curl http://localhost:8080/healthz
# 期望: ok

# Pipeline 就绪检查
curl http://localhost:8080/readyz
# 期望: ready

# 查看版本信息
curl http://localhost:8080/version

# Qdrant 状态
curl http://localhost:6333/healthz

# Redis 状态
docker compose exec redis redis-cli ping
```

### 3.4 创建 Kafka Topic（可选）

Kafka 已配置自动创建 Topic，但如需手动指定分区数：

```bash
# 创建处理 Topic（6 分区）
docker compose exec kafka kafka-topics.sh \
    --bootstrap-server localhost:9092 \
    --create --if-not-exists \
    --topic doc-processing \
    --partitions 6 \
    --replication-factor 1

# 创建 DLQ Topic（3 分区）
docker compose exec kafka kafka-topics.sh \
    --bootstrap-server localhost:9092 \
    --create --if-not-exists \
    --topic doc-processing-dlq \
    --partitions 3 \
    --replication-factor 1
```

---

## 四、使用方式

### 4.1 方式一：HTTP 文件上传（推荐）

通过 `POST /v1/upload` 接口直接上传文件，系统自动完成存储→推 Kafka→异步处理：

```bash
curl -X POST http://localhost:8080/v1/upload \
  -H "Authorization: Bearer <jwt>" \
  -H "X-Idempotency-Key: upload-20260514-0001" \
  -F "file=@/path/to/report.pdf" \
  -F "permission=internal"
```

**请求参数**（multipart/form-data）：

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `file` | file | 是 | 上传的文档文件 |
| `permission` | string | 否 | 权限级别：`public`/`internal`/`confidential`（默认 `internal`） |

请求头说明：建议传 `X-Idempotency-Key`，同一租户重复请求会返回首次成功响应，避免重复入队。

租户隔离说明：上传接口不接受 `tenant_id` 表单字段，租户由 JWT `tenant_id` 强制注入。

**响应示例**（HTTP 202 Accepted）：

```json
{
  "task_id": "doc-20260512-143022-0001",
  "doc_id": "doc-20260512-143022-0001",
  "status": "processing",
  "file_hash": "a3f2b8c1d4e5...",
  "message": "file 'report.pdf' accepted, processing in background",
  "timestamp": "2026-05-12T14:30:22+08:00"
}
```

### 4.2 方式二：直接发送 Kafka 消息

向 Kafka Topic `doc-processing` 发送 JSON 消息：

```json
{
  "file_path": "tenant-a/report.pdf",
  "doc_id": "doc-001",
  "tenant_id": "tenant-a",
  "permission": "internal",
  "file_hash": "sha256-of-file",
  "created_at": "2026-05-12T14:30:22+08:00"
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `file_path` | string | 是 | 对象存储 Key（推荐）或容器内绝对路径（兼容） |
| `doc_id` | string | 是 | 文档唯一 ID（用于去重和 Checkpoint） |
| `tenant_id` | string | 是 | 租户 ID（多租户隔离） |
| `permission` | string | 否 | 权限级别 |
| `file_hash` | string | 否 | 文件 SHA-256 哈希（数据血缘追踪） |
| `created_at` | string | 否 | 任务创建时间（RFC3339） |

### 4.3 RAG 查询（问答接口）

文档处理完成后，可通过 `/v1/query` 接口进行知识检索 + LLM 回答：

```bash
curl -X POST http://localhost:8080/v1/query \
  -H "Authorization: Bearer <jwt>" \
  -H "Content-Type: application/json" \
  -d '{"question": "什么是向量数据库？", "top_k": 5}'
```

租户隔离说明：`tenant_id` 由 JWT 解析并强制注入检索过滤，不接受请求体指定租户。
权限隔离说明：检索会按 JWT `permission` 角色自动过滤文档权限，映射为 `admin -> public/internal/confidential`、`user -> public/internal`、`readonly/未知 -> public`。

如果你在上线该策略前已经有历史向量数据（payload 缺少 `permission`），请先做回填：

```bash
# 仅统计缺失数量（dry-run）
scripts/backfill-qdrant-permission.sh \
  --endpoint http://localhost:6333 \
  --collection documents

# 执行回填（默认按最小权限写入 confidential）
scripts/backfill-qdrant-permission.sh \
  --apply \
  --endpoint http://localhost:6333 \
  --collection documents \
  --permission confidential
```

说明：回填是保守兜底，想要精确权限标签建议重新入库。

`/v1/query` 采用严格失败语义，不做答案兜底：
- Embedding 失败：返回 `500 embedding failed`
- 检索失败：返回 `500 search failed`
- LLM 生成失败：返回 `500 generation failed`

### 4.4 挂载文档目录

如果文档存放在宿主机 `/home/data/documents`，需要在 `docker-compose.yml` 中为 etl-worker 服务添加 volume：

```yaml
etl-worker:
  volumes:
    - /home/data/documents:/data/documents:ro
```

### 4.5 发送测试消息（Kafka 方式）

```bash
# 方法一：通过 kafka-console-producer
echo '{"file_path":"/data/documents/test.pdf","doc_id":"test-001","tenant_id":"tenant-a"}' | \
    docker compose exec -T kafka kafka-console-producer.sh \
    --bootstrap-server localhost:9092 \
    --topic doc-processing

# 方法二：通过 Kafka-UI 界面
# 打开 http://localhost:8090 → Topics → doc-processing → Produce Message
```

### 4.6 查看处理日志

```bash
# 实时查看 ETL Worker 日志
docker compose logs -f etl-worker

# 查看 Query API 日志
docker compose logs -f query-api

# 查看最近 100 行
docker compose logs --tail 100 etl-worker
```

---

## 五、运维操作

### 5.1 停止服务

```bash
# 停止所有服务（保留数据卷）
docker compose down

# 停止并删除所有数据（慎用！）
docker compose down -v
```

### 5.2 重启单个服务

```bash
# 重启 ETL Worker（不影响 API 服务）
docker compose restart etl-worker

# 重启 Query API（不影响 Worker）
docker compose restart query-api

# 重新构建并启动 Worker（代码更新后）
docker compose up -d --build etl-worker

# 重新构建并启动 API（代码更新后）
docker compose up -d --build query-api
```

### 5.3 扩缩 Worker 实例

```bash
# 横向扩展为 3 个 ETL Worker 实例
docker compose up -d --scale etl-worker=3
```

> 注意：多实例共用同一个 Kafka ConsumerGroup，Kafka 会自动分配 Partition。

### 5.4 查看 Metrics

```bash
# JSON 格式指标
curl http://localhost:8080/metrics | python3 -m json.tool
```

### 5.5 查看 DLQ（处理失败的消息）

```bash
# 通过 Kafka-UI 查看
# 打开 http://localhost:8090 → Topics → doc-processing-dlq → Messages

# 或命令行消费
docker compose exec kafka kafka-console-consumer.sh \
    --bootstrap-server localhost:9092 \
    --topic doc-processing-dlq \
    --from-beginning
```

---

## 六、故障排查

### 6.1 容器启动失败

```bash
# 查看 Worker 启动日志
docker compose logs etl-worker

# 查看 API 启动日志
docker compose logs query-api

# 常见原因：
# - "config error: EMBED_API_KEY is required in production" → 仅 production 需要，检查 ENVIRONMENT 与 Key 配置
# - "redis connect failed" → Redis 未就绪，等待几秒后重试
# - "qdrant connection failed" → Qdrant 未启动
```

### 6.2 消息处理失败

```bash
# 查看 DLQ 中的失败消息
docker compose exec kafka kafka-console-consumer.sh \
    --bootstrap-server localhost:9092 \
    --topic doc-processing-dlq \
    --from-beginning

# 常见原因：
# - "file not found" → 文件路径不正确或未挂载
# - "embed: 429 Too Many Requests" → API 限流，降低 EMBED_RATE_LIMIT
# - "parse: exit status 1" → pdftotext/pandoc 解析失败，检查文件格式
```

### 6.3 `/query` 返回 `embedding failed`

```bash
# 1) 在宿主机确认 Ollama embedding 可用
curl http://localhost:11434/api/embeddings \
  -H "Content-Type: application/json" \
  -d '{"model":"nomic-embed-text","prompt":"连通性测试"}'

# 2) 在容器内确认可访问宿主机 Ollama
docker compose exec query-api curl -s http://host.docker.internal:11434/api/embeddings \
  -H "Content-Type: application/json" \
  -d '{"model":"nomic-embed-text","prompt":"container test"}'
```

常见原因：
- `EMBED_ENDPOINT` 配成了 `http://localhost:11434/...`（在容器内会指向容器自己）
- Ollama 仅监听 `127.0.0.1`，未对容器可达地址开放
- `EMBED_MODEL` 名称与本地已拉取模型不一致

### 6.4 `/query` 返回 `generation failed`

常见原因：
- `LLM_ENDPOINT` 只填了 base URL，但上游路径规则非 OpenAI 风格
- `LLM_API_KEY` 无效或权限不足
- 模型名无权限或不存在

说明：
- 服务会将 `LLM_ENDPOINT` 规范化到 `/chat/completions`。
- 当前请求体只发送 `model/messages/max_tokens`，不携带 `temperature`，用于兼容更多上游实现。
- 该接口不做兜底回答，上游失败会直接返回 500。

### 6.5 资源不足

```bash
# 查看容器资源使用
docker stats

# 如果内存不足，调整 docker-compose.yml:
# deploy:
#   resources:
#     limits:
#       memory: 2G
```

---

## 七、磁盘空间预估

| 组件 | 存储内容 | 预估大小 |
|------|----------|----------|
| Kafka | 消息日志（7天保留） | 每万条消息约 10MB |
| Redis | Checkpoint 进度 | 每文档约 200B，24h TTL 自动过期 |
| Qdrant | 向量数据 | 每 1000 chunks 约 12MB（1536维） |
| Docker 镜像 | 应用 + 基础设施 | 约 3GB（首次拉取） |

---

## 八、项目文件结构

```
ai-etl-pipeline/
├── cmd/
│   ├── worker/
│   │   └── main.go              # ETL Worker：Kafka消费 → 解析 → 向量化 → 入库
│   └── api/
│       └── main.go              # Query API：文件上传 + RAG查询 + 鉴权 + 限流
├── internal/
│   ├── model/model.go           # 核心领域模型 + 接口定义
│   ├── config/config.go         # 环境变量配置加载与校验
│   ├── gateway/gateway.go       # HTTP 文件上传网关（POST /upload）
│   ├── parser/parser.go         # 语义切块（Markdown 标题感知 + 段落合并）
│   ├── embedder/embedder.go     # Embedding 客户端（熔断器 + 重试 + Ollama兼容）
│   ├── sparse/encoder.go        # BM25 稀疏向量编码（Hybrid Search）
│   ├── store/store.go           # Qdrant 向量存储（Upsert 幂等）
│   ├── pipeline/pipeline.go     # Worker Pool 编排器（panic 自愈 + drain）
│   ├── kafka/kafka.go           # Kafka Consumer + Producer + DLQ
│   ├── checkpoint/checkpoint.go # Redis Checkpoint（断点续传）
│   ├── metrics/collector.go     # 结构化日志 + 指标采集
│   ├── query/service.go         # RAG Query Service（调用 Retrieval + LLM + 熔断器）
│   ├── retrieval/               # Query Router + 多路召回 + 融合 + Rerank + 缓存
│   ├── es/                      # Elasticsearch 全文索引与重试队列
│   ├── auth/auth.go             # JWT 鉴权 + RBAC + 租户隔离
│   ├── circuit/circuit.go       # 熔断器（Sony gobreaker 封装）
│   ├── s3/client.go             # MinIO/S3 对象存储客户端
│   ├── tracing/tracing.go       # OpenTelemetry 分布式追踪
│   ├── prometheus/metrics.go    # Prometheus 指标集合
│   └── middleware/middleware.go # 租户限流 + API版本控制 + CORS + 超时
├── .dockerignore
├── .env.example                 # 环境变量模板
├── .gitignore
├── .golangci.yml                # 代码检查配置
├── Dockerfile                   # 多阶段构建 - ETL Worker
├── Dockerfile.api               # 多阶段构建 - Query API
├── Makefile                     # 构建自动化命令
├── docker-compose.yml           # 企业级服务编排（Worker + API + 基础设施 + 可观测性）
├── go.mod / go.sum              # Go 依赖管理
└── README.md
```

---

## 九、核心特性

| 特性 | 实现 |
|------|------|
| **语义切块** | Markdown 标题（H1-H6）自动分块 + 短段落智能合并 + Overlap |
| **Hybrid Retrieval** | Query Router + Qdrant Dense/Sparse + Elasticsearch BM25 并发召回，RRF 融合、Rerank 和缓存降级 |
| **数据血缘** | 每个 Chunk 携带 DocID、FileHash(SHA-256)、Permission、CreatedAt |
| **At-Least-Once** | Kafka 手动 Commit + Redis Checkpoint 保证不丢数据 |
| **限流 & 重试** | 令牌桶限流 + 指数退避重试 + DLQ 兜底 |
| **防 OOM** | 流式解析 + 固定 Buffer + 信号量控制并发 |
| **幂等入库** | Qdrant Upsert（相同 ChunkID 覆盖写入） |
| **多租户** | TenantID 贯穿全链路，支持权限级别过滤 |
| **优雅关闭** | SIGTERM → 停止消费 → Drain 在途任务 → 释放资源 |
