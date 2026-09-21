# 项目优化设计方案

编制日期：2026-09-21
编制方式：代码 + 远端运行栈实测。**每条结论都标注了证据来源**，凡未实测的均明确标注为「未验证」。
范围：`ai-etl-platform`（Go ETL/Query + Python Parser/Reranker + Next.js 工作台）。

> 本文不是愿望清单。优先级按「不做的后果」排序，不按「工作量」排序。

---

## 0. 结论摘要

一句话：**功能主线（企业级 Agent 预审 + 发布中心）代码是完整的，当前真正的风险不在功能，在运维底座。**

| 类别 | 结论 |
| --- | --- |
| 功能完善度 | 主链路（上传→解析→向量化→检索→问答→发布审批）已闭环；缺口集中在**自助能力**、**备份恢复**、**合规审查深度**三处 |
| 系统设计 | 服务边界清晰、CI 门禁完整、租户/权限/证据链设计是扎实的；问题在**配置一致性**、**状态文档三源冲突**、**单文件职责过载** |
| 最紧急项 | **服务器磁盘 97%，Elasticsearch 已因磁盘水位拒绝分配分片，距只读阈值仅剩 3.3GB**，且**全栈无任何备份** |

---

## 1. P0 —— 必须先处理，否则会停服

### 1.1 磁盘濒满，Elasticsearch 已进入分配拒绝状态

**证据**

```
$ df -h /            → /dev/vda2  197G  182G  7.3G  97%
$ docker system df   → Images 108.7GB(可回收 79.55GB) / Build Cache 60.64GB(可回收 9.91GB)
                       Local Volumes 24.64GB(可回收 19.98GB)
$ ES _cluster/health → status: yellow, unassigned_shards: 1, active_shards_percent: 50%
$ ES allocation/explain → can_allocate: "no",
                          reason: "CLUSTER_RECOVERED", at: 2026-09-12T06:56:23Z
```

**机制**：`docker-compose.yml:147-149` 把 ES 水位设为**绝对值** —— `low=8gb` / `high=6gb` / `flood_stage=4gb`（远端 `.env` 未覆盖，走 compose 默认）。当前可用 7.3GB：

| 可用空间 | ES 行为 | 当前状态 |
| --- | --- | --- |
| < 8GB | 拒绝分配新分片 | **已触发**（`can_allocate: no`） |
| < 6GB | 尝试迁移分片（单节点无法迁移） | 还剩 1.3GB |
| < 4GB | **全部索引转只读，写入被拒** | 还剩 3.3GB |

**影响**：再消耗约 3.3GB，上传→入库→问答全链路停止。Kafka 日志、ES 段合并、容器日志都在持续吃这部分空间，不是「会不会」的问题，是「还有多久」。

**怎么做**

1. **立即回收空间**（需你确认后再执行，我不会擅自动）：
   - `docker builder prune` —— 回收构建缓存约 **9.9GB**，无风险。
   - 悬空镜像当前为 **0 个**，所以「清悬空镜像」无效，别指望它。
   - 108.7GB 镜像里**大部分是其他项目的**：`openclaw:custom-lobster-backup-20260721` 5.65GB、`ghcr.io/openclaw/openclaw:latest` 4.55GB、`openclaw-codex-worker:local` 4.15GB、`mcr.microsoft.com/playwright` 3.2GB 等。**不能整体 `docker image prune -a`**，那会打断你其他项目的可重启性。
   - `Local Volumes` 可回收 19.98GB，但**卷里可能有其他项目的数据，我强烈建议逐个人工确认，不做批量 prune**。
2. **把水位改成百分比**，别用绝对值 —— 单节点 ES 用 `8gb/6gb/4gb` 意味着机器越满越危险，且与宿主机总容量脱钩。
3. **加磁盘告警**：Prometheus 已有，加一条 `node_filesystem_avail_bytes` 规则，阈值 15% / 10% 两档，接现有 alertmanager。

**验收判据**：`df -h /` 可用空间 > 25GB；`ES _cluster/health` 的 `unassigned_shards` 归零、`status: green`；告警规则在 Prometheus 里 `up` 且能触发一次测试告警。

---

### 1.2 全栈没有任何备份

**证据**

```
$ ls scripts/ | grep -i "backup|restore|dump"   → 空
$ crontab -l                                    → 空
$ ls ~/backups                                  → 仅 openclaw-upgrade-20260328-165451（36MB，与本项目无关）
```

**影响**：PostgreSQL 是文档版本、generation、发布 release 和审批决策的权威（`LEARNINGS.codex.md` 明确定义）。它现在没有备份。磁盘满导致的写入失败、卷损坏、误删，任一发生都不可恢复。这与 1.1 是**叠加风险**，不是独立风险。

**怎么做**

1. 新增 `scripts/backup-stack.sh`：`pg_dump`（PostgreSQL）+ MinIO 对象清单 + Qdrant 快照，落到仓库外目录并做保留轮转。
2. 新增 `scripts/restore-stack.sh`，并且**必须真跑一次恢复演练**，不能只写脚本。
3. 挂 cron 每日执行，结果写日志；连续两次失败要能被发现。
4. 明确 RPO/RTO 并写进 `docs/`。backlog 的 P2.6 把「备份恢复」列为生产准入门，但在那之前，**现在这台机器上的数据就已经没有保护了**。

**验收判据**：恢复演练在隔离栈上跑通，恢复后的数据能通过一次真实问答（引用命中预期文档）；cron 条目存在且有成功日志。

---

## 2. P1 —— 工程可复现性（现在会绊住每一次改动）

### 2.1 远端 Go 工具链默认跑不通，`.gomod`/`.gocache` 是承重结构

**证据**

```
$ cd services/etl-worker && go test ./cmd/api ./cmd/worker
  internal/prometheus/metrics.go:16:2: mkdir /home/ubuntu/go/pkg/mod/cache/download/github.com/prometheus:
  permission denied
  FAIL  ai-etl-pipeline/cmd/worker [setup failed]

$ ls -ld ~/go/pkg/mod          → drwxr-xr-x root root   ← ubuntu 用户无写权限
$ go env GOMODCACHE           → /home/ubuntu/go/pkg/mod  ← 默认指向 root 拥有的目录

# 显式指定仓库内缓存后：
$ GOMODCACHE=$PWD/.gomod GOCACHE=$PWD/.gocache go test ./cmd/api ./cmd/worker
  ok  ai-etl-pipeline/cmd/api     27.418s
  ok  ai-etl-pipeline/cmd/worker   0.011s
```

**根因**：`/home/ubuntu/go/pkg/mod` 于 2026-09-08 由 root 创建，ubuntu 无写权限。仓库根目录的 `.gomod`(350MB) / `.gocache`(384MB) 是 2026-09-12 用 `GOMODCACHE`/`GOCACHE` 环境变量临时绕过的产物 —— **它们不是垃圾，是绕行方案**。

**影响**：任何新会话或新人在服务器上直接跑 `go test` 都会失败，且失败信息是权限错，容易误判成代码问题。这直接损害「改完能在服务器上验证」这个目标。

**怎么做**（三选一，推荐第 1 条）

1. `sudo chown -R ubuntu:ubuntu /home/ubuntu/go/pkg/mod` —— 一劳永逸，恢复默认行为。
2. `go env -w GOMODCACHE=/home/ubuntu/ai-projects/ai-etl-platform/.gomod GOCACHE=...` —— 固化到 go 配置，但把缓存绑在仓库路径上，仓库移动即失效。
3. 在 `services/etl-worker/Makefile` 和 `CONTINUATION.md` 里显式导出这两个变量 —— 只是把坑写清楚，没消除。

**注意**：无论选哪条，`.gomod`/`.gocache` 都不应提交进 Git（已在 `31a5b28` 加入 `.gitignore`），但**也绝不能删除**。

**验收判据**：在远端 `cd services/etl-worker && go test ./... -count=1` 不设任何额外环境变量即可通过。

### 2.2 `.env.example` 切块配置段有 3 个「死键」，改错静默无效

**证据**（`.env.example:408-412`）

```
MIN_CHUNK_SIZE=128
MAX_CHUNK_SIZE=600          ← 无任何编排引用
CHUNK_OVERLAP=50            ← 无任何编排引用
PARSER_MAX_CHUNK_SIZE=600   ← 真正生效
PARSER_CHUNK_OVERLAP=50     ← 真正生效
```

`docker-compose.yml:300-302` 实际是：

```
- MIN_CHUNK_SIZE=128                          ← 硬编码，不插值 .env
- MAX_CHUNK_SIZE=${PARSER_MAX_CHUNK_SIZE:-600} ← 从 PARSER_* 取值
- CHUNK_OVERLAP=${PARSER_CHUNK_OVERLAP:-50}    ← 从 PARSER_* 取值
```

**影响**：切块参数是入库质量的核心旋钮（`LEARNINGS.codex.md` 记录了从 1200 字符调到 600 的完整过程）。现在用户按直觉改 `MAX_CHUNK_SIZE=1000` 会**完全没反应**，且没有任何报错。这是最难排查的一类配置缺陷。

**怎么做**：删掉 3 个死键，只保留 `PARSER_*`；或在 `docker-compose.yml` 里改为真正的 `${MAX_CHUNK_SIZE:-600}` 插值。推荐前者 —— 一套键名，不留二义。

**验收判据**：`grep -rn "MAX_CHUNK_SIZE\|CHUNK_OVERLAP" .env.example` 只剩 `PARSER_` 前缀；改 `PARSER_MAX_CHUNK_SIZE` 后重建 parser 容器，容器内 `env | grep MAX_CHUNK` 的值随之变化。

### 2.3 19 个环境变量被代码引用但未写入 `.env.example`

**证据**

```
config.go 引用键 184 个，.env.example 定义 192 个，差集 19 个未文档化：
ALERT_WEBHOOK_TOKEN, EMBED_BACKOFF, EMBED_MAX_BACKOFF, EMBED_MAX_RETRIES,
HEALTH_PORT, LOGIN_RATE_LIMIT, LOGIN_RATE_LIMIT_STORE, PARSER_ENDPOINT,
PARSER_READ_BUFFER, PIPELINE_RETRY_BACKOFF, PIPELINE_TASK_BUFFER, PROMPT_DIR,
PROMPT_VERSION, RETRIEVAL_MIN_RELEVANCE, SCIM_BEARER_TOKENS, SPARSE_AVG_DL,
SPARSE_B, SPARSE_K1, UPLOAD_DIR, WORKER_HEALTH_URL
```

`AGENTS.md` 明文要求：「配置通过环境变量驱动，新增设置同步写入 `.env.example` 文件」。**这条约定正在被违反**，其中 `LOGIN_RATE_LIMIT`、`SCIM_BEARER_TOKENS`、`ALERT_WEBHOOK_TOKEN`、`RETRIEVAL_MIN_RELEVANCE` 都是运维/安全相关的键。

**怎么做**：补齐这 19 个键（带默认值和一行说明）。建议再加一个契约测试 —— 解析 `config.go` 的键集合与 `.env.example` 比对，缺一个就红，防止再次漂移。

**验收判据**：新增契约测试通过；差集为空。

---

## 3. P1 —— 状态文档三源冲突（这是真正的「设计问题」）

**证据**

| 文档 | 最后核验 | 它对状态的说法 | 实测事实 |
| --- | --- | --- | --- |
| `issues/findings-register.md` | 2026-09-11 | UAT-017/018/019/020 **开放** | 四项均已修复 |
| `docs/backlog.md` | 2026-09-12 | P-UAT-1「登记册开放项已清空」 | 与登记册直接矛盾 |
| `CONTINUATION.md` | 2026-09-09 | §4「UAT-001～009 仍待真实页面复验」 | 这九项 09-09 就已全部关闭 |

**实测证据**（在**运行中的容器**内检查部署产物，不是看源码）：

```
docker exec ai-etl-platform-web-1 grep -rl 生产库 /app/.next/static        → 命中（UAT-017 空间中文）
docker exec ai-etl-platform-web-1 grep -rl PPTX  /app/.next/static        → 命中（UAT-019 Office 类型）
docker exec ai-etl-platform-web-1 grep -rl XLSX  /app/.next/static        → 命中
docker exec ai-etl-platform-web-1 grep -rl 可检索、可问答、可发布的知识资产 /app/.next  → 命中（UAT-020 新文案）
docker exec ai-etl-platform-web-1 grep -rl 可量化的知识资产 /app/.next    → 空（旧文案已清除）
```

**这才是根问题**：三份文档**各自都声明自己是状态源** —— `backlog.md` 说「当前状态以本文件为准」，`findings-register.md` 说「新问题只在这里建单」，`CONTINUATION.md` 说「以本文件为准」。三者互相引用又互相矛盾。历史代价已经发生过：`LEARNINGS.codex.md` 里「续接文档口径对齐」「修复过时上下文材料」这类条目反复出现，都是在擦这个屁股。

**怎么做**

1. **确立单一事实源**：`docs/backlog.md` 作为唯一状态源；`findings-register.md` 只登记缺陷明细，不再自行声明总体状态；`CONTINUATION.md` 降级为「上次中断点 + 运行环境」，删掉所有状态断言。
2. **加一致性契约测试**：断言 `findings-register.md` 中「开放」条目数 == backlog 中对应未关闭条目数。不一致就红。这是把「文档纪律」变成 CI 门禁，而不是靠人记得更新。
3. **本轮先消除现存矛盾**：把 UAT-017～020 标记为「已修复（产物层验证）」，并在 `CONTINUATION.md §4` 删除已失效条目。

**验收判据**：三份文档对同一事实的说法一致；新增的一致性测试通过；`CONTINUATION.md` 里不再出现「UAT-xxx 待复验」这类会过期的状态断言。

**一个诚实的限制**：我对 UAT-017～020 的验证是**产物层**的（部署包里确有新代码、旧文案已清除），**不是真实页面复验**。项目自己的章程 `docs/product-experience-acceptance.md` 明确要求「不能用隔离栈绿报代替真实页面结论」。所以这四项应标为「已修复，待真实页面复验」，而不是「已关闭」。

---

## 4. P2 —— 功能完善度缺口

按「对用户可用性的影响」排序。

### 4.1 普通用户没有任何自助能力

**证据**：`grep -rn "register|forgot|reset-password" services/etl-worker/cmd/api/*.go` → 无匹配。`CONTINUATION.md §4` 亦记录：「普通用户仍不能自助注册、找回密码或管理知识空间；账号、空间和发布审批需要管理员预先配置」。

**影响**：这是「演示可用」与「能给别人用」之间的分界线。任何人要进来，都得先找到管理员。

**怎么做**：这是产品决策，不是纯技术活。最小切片：管理员邀请链接（带 TTL 的一次性 token）→ 用户自助设置密码。这比开放注册安全得多，且复用现有用户表。找回密码可以先用「管理员重置」兜住，不自建邮件通道。

**验收判据**：一条真实链路 —— 管理员发邀请 → 新用户点链接设密码 → 登录 → 问答。全程无需管理员再介入。

### 4.2 备份恢复 —— 见 1.2，此处不重复

### 4.3 上传者删除能力后端有、前端未开放

**证据**：`CONTINUATION.md §4` 记录「后端授权逻辑已有，但 UI 是否开放仍需产品决定」。

**影响**：低。但「后端有、前端不给」是一种**授权语义不完整**的状态 —— 接口能力已经存在，只是没人从 UI 能走到。要么开放，要么明确记录为「仅 API 可用」并加测试锁住，不要悬着。

### 4.4 合规审查深度 —— 项目自己已经反复划定边界

**证据**：`LEARNINGS.codex.md` 与 `backlog.md` 多处强调「不得把当前已实现的受限规则扫描描述为完整合规审查」。暂缓项：版本对比、政策库、跨文档冲突审查。

**我的判断**：这一项**不该现在做**。它是「大」项，且当前预审（R1/R6）已能覆盖发布资格预审的真实需求。先把它在文档里的表述**收紧到与实现一致**（现在是准确的，保持住即可），等有真实业务诉求再立项。

### 4.5 受外部决策阻塞的项（不建议现在动）

`backlog.md` Active work 里 5 项全部 blocked / pending decision，依赖项都在项目外部：P1.9（业务责任人签字）、P2.3（保留期决策）、P2.5-PROD/STAGE（IdP 选型与 staging）、P2.6（SLO/RPO/预算批准）。

**这些不是技术债，是决策债**。在决策到位前动手只会白做。

---

## 5. P2 —— 代码结构

### 5.1 `query/service.go` 单文件职责过载

**证据**：1749 行、51 个函数，混装了至少 6 类职责 ——

| 职责 | 代表函数 |
| --- | --- |
| HTTP 入口 | `HandleQuery`, `HandleQueryStreaming` |
| SSE 协议 | `writeSSEError`, `queryBodyLimit`, `isMaxBytesError` |
| LLM 传输 | `callLLM`, `streamChat`, `completeAnswer`, `enableChatStream` |
| 提示词组装 | `buildPrompt`, `promptContextExcerpt`, `loadSystemPrompt` |
| 检索后处理 | `gateByRelevance`, `stabilizeRanking`, `retrievalInfoFromResult` |
| 校验与成本 | `groundingCheck`, `parseGroundingVerdict`, `estimateLLMCost` |

对比：`cmd/api/main.go` 从 1746 行拆到 667 行（`LEARNINGS.codex.md` 2026-09-09 记录了这次拆分，方式是「同包机械拆分，不改 HTTP/权限语义」）。**同样的手法可以再来一次**，且项目已经记了「下一步如继续精简，再拆 `query/service.go`」。

**怎么做**：按上表拆成 `service.go`（编排）+ `llm_transport.go` + `prompt.go` + `sse.go` + `postprocess.go`，**保持同一 package、不改任何导出签名**。这是纯机械重构，风险可控，且能显著降低后续每次改问答链路的上下文成本。

**验收判据**：`go test ./internal/query -count=1` 通过；`go vet` 干净；拆分前后 `git diff --stat` 的**导出符号集合完全一致**（用 `go doc` 输出对比）。

**注意**：这是**收益/风险比最好的一条**，但**不是最紧急的**。它值得做，是因为每次改问答都要读 1749 行。

### 5.2 其他偏大文件（暂不动，仅记录）

`agentapi/service.go` 1195 行、`config/config.go` 1080 行、`pipeline/pipeline.go` 959 行、`cmd/api/upload_handlers.go` 941 行。前端 `qa/page.tsx` 512 行、`documents/page.tsx` 477 行。

这些没有 5.1 那么突出的职责混杂，先不动。

### 5.3 测试覆盖

**证据**：40/44 个 Go 包有测试文件。无测试的 4 个：`internal/checkpoint`、`internal/gateway`、`internal/metrics`、`internal/model` —— 都是数据结构或薄适配层，**风险可接受**。

**实测基线**（2026-09-21 在远端跑）：

| 套件 | 结果 |
| --- | --- |
| Go `internal/...` | 全部 ok |
| Go `cmd/api` / `cmd/worker` | 需 `GOMODCACHE` 覆盖后 ok（见 2.1） |
| Parser `pytest` | 38 passed |
| `scripts/tests` 契约测试 | 240 tests OK（17 skipped） |
| Web `eslint --max-warnings=0` | 通过 |
| Web `next build` | 通过 |

**CI 门禁**（`.github/workflows/ci.yml`）：go / eval / python / reranker / web / compose / observability / trivy 八项汇入 `Required Checks`，设计是完整的。

---

## 6. 需要进一步验证的项（诚实标注）

| 项 | 现状 | 验证方式 |
| --- | --- | --- |
| Generation build 长任务恢复语义 | `CONTINUATION.md §4` 记录「完整 manifest/digest 跨重试持久化语义需整本任务验证，不能仅依赖 Redis checkpoint」 | 需一次整本长文档的故障注入 |
| Jaeger `invalid UTF-8` 导出告警 | 已做 truncate 处理，但未确认运行日志中是否消失 | 查 jaeger 容器日志 |
| 评测脚本 ES 同步竞态 | 本次实测出现 `[eval] WARN: ES sync check did not reach 47 after 1s (last count 10); continuing` | 复现并确认是否影响评测结论 |
| UAT-017～020 真实页面复验 | 仅产物层验证 | 按 `docs/product-experience-acceptance.md` 用真实浏览器走一遍 |
| 企业身份生产 | 全部 blocked 在外部决策 | 不验证，等决策 |

---

## 7. 建议执行顺序

```
第 1 步（今天）   1.1 磁盘回收 + ES 水位改百分比 + 磁盘告警
第 2 步（本周）   1.2 备份脚本 + 恢复演练；2.1 Go 工具链权限修复
第 3 步（本周）   3.  状态文档三源归一 + 一致性契约测试
第 4 步           2.2 / 2.3 配置一致性修复 + 契约测试
第 5 步           4.1 邀请式自助开户（需你先确认产品口径）
第 6 步           5.1 query/service.go 机械拆分
```

**为什么是这个顺序**：1.1 和 1.2 是「不做会丢数据」；2.1 是「不做则每次改动都在踩坑」；第 3 步是「不做则后面所有状态判断都不可信」；第 4 步成本最低收益明确；第 5 步需要你的产品决策；第 6 步是纯收益优化，随时可做。

---

## 8. 我没有做的事（避免误解）

- **没有修改任何代码或配置**。本文只有调查结论。
- **没有删除任何东西**。远端 734MB 的 `.gomod`/`.gocache` 是承重结构，我反而在本次确认了它不能被删。
- **没有执行磁盘清理**。108.7GB 镜像里大部分是你其他项目的，且可回收卷里可能有数据 —— 这类操作我不会未经确认执行。
- **UAT-017～020 没有标记为「已关闭」**，因为只做了产物层验证，未做章程要求的真实页面复验。
