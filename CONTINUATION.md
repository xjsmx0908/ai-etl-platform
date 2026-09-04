# 项目任务续接记录

更新时间：2026-09-03（Asia/Shanghai）

## 1. 已完成的工作

### 平台主链路与治理

- 已实现并合并文档上传、Kafka 异步 ETL、解析、OCR、Embedding、Qdrant/Elasticsearch 入库、混合检索、RAG 问答、引用与权限过滤。
- 已完成持久化接入、事务 outbox、重试/DLQ、任务状态、检查点、幂等、孤儿对象清理和崩溃恢复。
- 已完成 generation manifest、双索引一致性、原子激活、回滚、对账、版本绑定发布、四眼审批和可恢复删除。
- 已完成个人演示身份运行时：Keycloak、真实密码 + TOTP、SCIM provisioning、OIDC、HTTPS、会话管理和退出验收。
- PR #42 已合并；当前最新提交为 `54eccbd`，并已推送到 `origin/master`。

### 产品与前端体验

- 删除问答工作台中无实际作用的“演示知识库/用户上传”区分，上传入口统一按知识空间和权限处理。
- 问答工作台增加 SSE 阶段化反馈：准备范围、检索文档、权限/发布状态筛选、回答生成、回答校验、结果整理，以及拒答/失败终态；前端显示动态等待状态。
- 审计日志页面增加关键词检索、服务端分页、页码跳转和更清晰的布局。
- 文档管理页面完成筛选工具栏、正文检索、服务端分页、首页/末页/上一页/下一页、页码和跳转输入等交互优化。
- 可观测页面补齐核心服务显示和运行状态说明；质量评测、Agent Run ID、治理字段补充用途说明。

### 文档解析与扫描 PDF 链路

- DOCX 解析已支持按原始顺序遍历段落和表格，表格渲染为可检索的制表符分隔文本；“每个服务必须补齐的信息”表格可进入索引。
- PDF 切块已修复跨段落/跨页重复 overlap，增加 PDF 页边界处理、噪声块过滤和内容去重。
- 扫描 PDF 已改为分页 OCR：默认 25 页/批，页级检查点、稳定 chunk offset、OCR 专用 Kafka topic/DLQ、取消检查和 `cancelled` 终态。
- Parser 已移除 300 页硬限制，支持通过 `MAX_OCR_PAGES=0` 表示不限制页数；上传和 Parser 后端默认单文件上限为 100MB。

### 爱因斯坦传 PDF 超时修复

- 根因已确认：本机 CPU Ollama 的 `bge-m3` 对 1200 字符中文 chunk 处理过慢，批量并发下部分请求达到 `context deadline exceeded`；15 分钟总超时也不足以覆盖长文档。
- OCR embedding 已改为单 chunk 隔离，避免一个慢请求拖垮同批已成功 chunk。
- 默认切块调整为 600 字符、50 字符 overlap。
- Worker 默认流水线总超时调整为 2 小时，ingestion lease 调整为 12 小时；同时修复 lease 小于最坏重试窗口导致 worker 启动失败的问题。
- 用此前失败任务同一份 PDF 的第 26–50 页做真实验证：解析得到 78 个 chunk，最大长度 600；真实 Ollama `bge-m3` 逐个生成向量 78/78 成功，最长 6.76 秒，总耗时 209 秒。

### 验证与部署

- `docker compose config -q` 通过。
- Parser Service 测试：35 passed。
- Go 测试：`go test ./internal/... ./cmd/api ./cmd/worker` 全部通过。
- Web ESLint 和 Next.js production build 通过。
- 当前 Compose 全栈容器均已运行；Kafka、PostgreSQL、Redis、Qdrant、Elasticsearch、MinIO、Parser、Worker、Query API、Web 等状态正常。

## 2. 当前正在处理的工作

- 当前没有正在运行的代码实现任务。
- 最近一项修复已提交到远程，但《爱因斯坦传》此前失败的整本任务已经进入 DLQ；尚未自动重放或重新上传整本 583 页文件，因此“整本文件最终 completed”的端到端验收仍待执行。
- 下一次继续时应优先做一次全本重新上传/显式重放，并观察任务从 OCR 到 embedding、入库、发布完成的完整状态，而不是只做局部页批验证。

## 3. 已修改的文件及修改内容

### 配置、运行和文档

- `.env.example`：补充 OCR topic、页批、Embedding 并发和上传限制示例；默认 chunk 600/50、流水线超时 2h、lease 12h。
- `docker-compose.yml`：创建 OCR Kafka topic/DLQ；向 Parser、Worker、Query API 注入 OCR、切块、上传限制、Embedding 并发和长任务超时配置。
- `docs/backlog.md`：记录扫描 PDF、问答反馈、文档管理和治理相关工作项。
- `LEARNINGS.codex.md`：追加 DOCX 表格、重复切块、分页 UI、问答阶段反馈、扫描 PDF 和 CPU Embedding 超时修复记录。

### Parser Service

- `services/doc-parser-service/app/config.py`：支持文件大小/OCR 页数配置。
- `app/models.py`：扩展分页 OCR 解析响应字段。
- `app/routers/parse.py`：流式落盘、大小校验、分页参数、页统计、稳定 chunk offset；兼容直接调用时的 `Form` 默认值。
- `app/services/parser.py`、`app/services/parsers/pdf.py`：分页解析、OCR 调度和页范围处理。
- `app/services/parsers/docx.py`：按文档顺序提取段落和表格。
- `app/services/chunker.py`：页边界、去重、噪声过滤、600 字符切块配置。
- `tests/test_chunker.py`、`tests/test_parse.py`：增加表格、分页、流式上传和大小限制回归测试。

### ETL Worker/API

- `services/etl-worker/internal/pipeline/pipeline.go`：分页 OCR、页级检查点、取消、单 chunk OCR embedding、任务进度和 generation 流程。
- `internal/embedder/embedder.go`、`embedder_test.go`：Embedding 并发/超时重试以及 deadline 可重试判定。
- `internal/parser/client.go`：分页 Parser HTTP 客户端和较长 OCR 请求超时。
- `internal/config/config.go`：OCR、Embedding、任务租约和长流程配置。
- `internal/kafka/kafka.go`、`cmd/worker/main.go`：OCR topic、DLQ 和 consumer group 路由。
- `internal/model/model.go`、`internal/docstore/docstore.go`、`internal/store/store.go`：任务页进度、状态和索引相关字段/逻辑。
- `internal/ingestion/admission.go`、`internal/ingestion/postgres.go`、`internal/migrations/0001_init.up.sql`、`0008_ingestion_outbox.up.sql`、`0009_ingestion_job_lifecycle.up.sql`、`0023_ingestion_cancellation.*`：接入状态、取消终态和迁移约束。
- `internal/query/service.go`：问答检索与阶段事件支持。
- `cmd/api/main.go`、`cmd/api/audit_handlers.go`、`cmd/api/audit_fake_test.go`、`internal/audit/store.go`：上传、任务取消、审计检索和分页接口。
- `internal/pipeline/pipeline_test.go`、`internal/store/store_chunks_test.go`：流程、检查点、部分失败和索引测试。

### Web

- `web/app/(app)/qa/page.tsx`：问答 SSE 阶段化等待反馈和动态状态展示。
- `web/app/(app)/documents/page.tsx`：紧凑筛选、正文检索和完整服务端分页。
- `web/app/(app)/audit/page.tsx`：审计筛选、分页和视觉布局。
- `web/app/(app)/data/page.tsx`：上传入口和知识空间说明调整。
- `web/app/(app)/observe/page.tsx`、`agent/page.tsx`、`quality/page.tsx`：服务、治理和质量页面说明补充。
- `web/lib/apiClient.ts`、`web/lib/types.ts`：任务、上传、分页和 SSE 类型/客户端支持。

## 4. 已发现但尚未解决的问题

- 《爱因斯坦传》此前失败的任务 `doc-1788353215921792389` 已进入 DLQ；局部页批和真实 embedding 已通过，但整本 583 页重新上传后的最终 `completed` 尚未验证。
- Generation build 的长任务恢复仍需进一步审查：失败重试会重新创建 build session，虽然索引 upsert 具备幂等性，但完整 manifest/digest 的跨重试持久化语义需要整本任务验证，不能仅依赖普通 Redis checkpoint 宣称完全恢复。
- CPU `bge-m3` 仍然较慢；600 字符单请求已稳定，但整本扫描书预计耗时较长。若需要生产级吞吐，应使用 GPU 或独立 Embedding 服务，而不是继续盲目提高并发。
- Jaeger 曾出现 `string field contains invalid UTF-8` 导出告警；文本 truncate 已做有效 UTF-8 处理，但需继续确认运行日志中是否完全消失。
- 企业身份决策登记 E-01～I-10 仍为 Pending，D0～D7 为 Blocked；个人演示身份运行时不能视为 staging/production 企业身份准入。
- 普通用户仍不能自助注册、找回密码或管理知识空间；账号、空间和发布审批需要管理员预先配置。
- 普通用户 Web 界面尚未完整暴露上传者删除能力；后端授权逻辑已有，但 UI 是否开放仍需产品决定。
- 受管知识空间文档发布仍需管理员/独立审批；普通用户不能自行完成治理审批。
- 当前使用本机/演示配置，模型、密钥、备份恢复、SLO、容量和数据驻留尚未达到生产准入标准。
- `issues/bugs.md` 是用户问题记录，当前未纳入最近一次代码提交；其中部分产品解释已落实，仍需逐项确认是否要把剩余说明写入正式产品文档。

## 5. 下一步应该做什么

1. 让用户重新上传《爱因斯坦传》，或由管理员显式重放 DLQ 任务；记录新的 doc ID、job ID 和 generation ID。
2. 观察 `docker compose logs -f etl-worker parser-service`，确认至少跨越 OCR、embedding、入库和 completed/published 四个阶段；不要只依据页面静态状态判断。
3. 若整本仍失败，先按新错误定位是 OCR 页批、Embedding、Qdrant/ES、generation manifest 还是总超时，再针对单一边界修改。
4. 对整本任务完成后检查 Qdrant/ES 双索引数量、generation manifest、发布状态和问答命中“用户手册/爱因斯坦”等关键词。
5. 补充 generation build 跨重试的持久化 manifest 回归测试；确认失败后不会重复发布不完整 generation。
6. 清理或隔离 Jaeger invalid UTF-8 告警，并确认新任务日志不再出现该错误。
7. 若进入企业化阶段，先由业务、身份、安全和 SRE 负责人完成 E～I 决策，再解除 D0 并按 D1～D7 验收。

## 6. 继续当前任务必须知道的上下文

- 工作目录：`/home/ubuntu/ai-projects/ai-etl-platform`；分支：`master`；远程：`origin`。
- 当前 HEAD 与远程同步：`54eccbd fix: harden document ingestion and admin workflows`。
- 本地入口：Web `http://localhost:3100`，Query API `http://localhost:8080`，Parser `http://127.0.0.1:8000`。
- 当前关键运行配置：`PARSER_MAX_CHUNK_SIZE=600`、`PARSER_CHUNK_OVERLAP=50`、`OCR_PAGE_BATCH_SIZE=25`、`EMBED_CONCURRENCY=1`、`EMBED_TIMEOUT=180s`、`PIPELINE_STAGE_TIMEOUT=180s`、`PIPELINE_TIMEOUT=4h`、`PIPELINE_MAX_RETRIES=1`、`INGESTION_JOB_LEASE=12h`。
- 当前模型端点是本机 Ollama：`http://host.docker.internal:11434/api/embeddings`，模型 `bge-m3`，维度 1024；CPU 推理是整本任务耗时的主要因素。
- 当前 Compose 容器均运行；Kafka topic 已创建：`doc-processing`、`doc-processing-dlq`、`doc-processing-ocr`、`doc-processing-ocr-dlq`。
- 失败任务不应被隐式篡改；重新上传会产生新 doc ID。若要重放，必须通过明确的 DLQ/任务重放流程并记录结果。
- 不得修改、提交或删除用户本地文件：`:memory:.ses`、`CONTINUATION.md`（本文件除非用户明确要求更新）、`new_thread_prompt.md`；`issues/` 也应视为用户问题记录，除非明确要求不要覆盖。
- Go 宿主机可能没有 `go/gofmt`，使用 Docker 执行测试和格式化：

  ```bash
  docker run --rm -v "$PWD/services/etl-worker:/app" \
    -v go-mod-cache:/go/pkg/mod \
    -v go-build-cache:/root/.cache/go-build \
    -w /app golang:1.25 \
    go test ./internal/... ./cmd/api ./cmd/worker
  ```

- Parser 测试使用运行中的容器：

  ```bash
  docker compose exec -T parser-service python -m pytest -q
  ```

- Web 校验：

  ```bash
  cd web && npm run lint && npm run build
  ```

- 任何后续代码改动都必须保留现有用户未提交修改，使用 `apply_patch` 编辑，并在提交前运行相关测试、`git diff --check` 和 `docker compose config -q`。
