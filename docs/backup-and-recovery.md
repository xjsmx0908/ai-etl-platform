# 备份与恢复

本文只描述**这台机器上实际部署的栈**（`ai-etl-platform`，`ENVIRONMENT=staging`，2026-09-21 实测）。
它记录的是工程能力与实测数字；**已批准的 RPO/RTO 目标不在本文**，原因见最后一节。

## 1. 哪些状态必须备份，哪些只是投影

`docs/adr/0010-production-slo-and-recovery-gates.md` 划的边界：

| 状态 | 性质 | 丢失后果 |
| --- | --- | --- |
| PostgreSQL（catalog、audit、版本、generation、发布与审批决策） | **权威** | 不可恢复 |
| 不可变源对象（MinIO `documents` 桶） | **权威** | 不可重放、不可重嵌入 |
| Qdrant `documents-v2` | 投影 | 原则上可重建 |
| Elasticsearch `documents_text` | 投影 | 原则上可重建 |

**本部署的投影实际上重建不了**，所以它们也必须快照：

```
EMBED_ENDPOINT=http://host.docker.internal:11434/api/embeddings
EMBED_MODEL=bge-m3
```

嵌入模型跑在**宿主机上的 ollama** 里。它不在 compose 栈内，不被任何备份覆盖，也不是容器镜像。
宿主机丢失（这正是备份要防的场景）时，投影无法重建。ADR-0010 的原文是：

> Qdrant and Elasticsearch remain rebuildable projections, but the recovery plan
> must prove that rebuilding them meets the approved RTO. If it does not,
> snapshots are also required.

重建连发生都发生不了，所以**快照是必需的，不是可选的**。

## 2. 备份包含什么

`scripts/backup-stack.sh`，单次实测 2.5 秒：

| 组件 | 方式 | 单次体积 | 说明 |
| --- | --- | --- | --- |
| PostgreSQL | `pg_dump -Fc -Z 6` | 249 KB | 权威状态 |
| MinIO | 数据卷 `tar`（`:/data:ro` 只读挂载） | 73 KB | 桶元数据与对象数据一起走，恢复时不需要 S3 客户端 |
| Qdrant | `POST /collections/{c}/snapshots` + `GET .../snapshots/{name}` | 64.3 MB | 快照里带集合配置（1024 维 Cosine、payload 索引），恢复即用 |
| Elasticsearch | `scripts/es-index-archive.py dump`（scroll，JSONL） | 10.4 MB | 首行是 `settings`+`mappings`，回灌时先建索引 |
| 合计 | | **≈ 75 MB** | `RETENTION=7` → ≈ 525 MB；`/` 可用 9.3 GB |

ES 索引名用 `documents_text` —— 这是**别名**，指向物理索引 `documents_text_v2`
（`internal/es/indexer.go`：`physicalIndex := i.index + "_" + cjkIndexVersion`，建完再挂 `_aliases`）。
归档走别名，所以物理索引换版本时备份脚本不需要跟着改。

ES 为什么不用 `_snapshot`：它需要集群级 `path.repo`，而 `elasticsearch:8.14.3` 镜像没设、
也不能在运行时设。逻辑归档不依赖目标集群配置，且能灌进任意一个空集群。

**刻意不做的事**：脚本里没有 `down -v`、没有 `docker volume rm`。备份脚本不能是能删掉它要保护的东西的脚本。

## 3. 完整性门禁（本项最容易被漏掉的部分）

`manifest.json` 里有一块 `integrity`：

```json
"integrity": {
  "status": "degraded",
  "missing_objects": 116,
  "missing_sample": ["default/ADM-2024-001/versions/...md", ...],
  "orphan_objects": 0
}
```

**为什么需要它**：平台里没有任何东西会把 `documents.object_key` 和对象存储对一遍。
源对象丢了以后，`documents` 行仍然是 `completed`，检索照常作答，index manifest 照常健康，
`GET /v1/documents/{id}` 照常返回一个 `file_size`。第一个会失败的地方是**一次修复重放**——
而对账器只能靠重放来修复。于是「备份成功」这四个字可以覆盖一个已经不可能恢复的环境。

`status=degraded` 就是那个缺失的信号。语义：

- `degraded` **不会**让备份失败（退出码仍为 0），因为产物本身是有价值的；
- 想要它变成硬失败，加 `REQUIRE_INTEGRITY=1`，退出码 3；
- 恢复演练用 `REQUIRE_INTACT=1` 时才会要求恢复出来的环境本身是完整的。

## 4. RPO / RTO

**工程现状（实测，不是目标）**

| 指标 | 当前能力 | 依据 |
| --- | --- | --- |
| RPO | **24 小时** | cron 每日 03:17（Asia/Shanghai）跑一次；随时可手动触发 |
| RTO | **79.7 秒** | 2026-09-21 演练实测（含全部 8 项断言），镜像已缓存 |

RTO 分解（同一次演练）：postgres 就绪 6 s → 恢复 1 s → 对象卷 1 s →
Qdrant/ES/MinIO 就绪 24 s → Qdrant 快照 1 s → ES 回灌 3 s → query-api 就绪 18 s → 断言 14 s。

**已批准的 RPO/RTO：未定。** ADR-0010 把数值目标列为业务方的 open decision，
并写明「Engineering must not invent them silently」。本文不替业务方认定目标，
只给出实测能力，供批准时对照。如果批准的目标严于上表，需要改的是：
RPO < 24 h → 提高 cron 频率并保留更多份；RTO < 80 s → 这已经是「整栈冷启 + 全量恢复」的下限，
进一步要压的是容器就绪时间。

## 5. 定时执行与失败发现

```
17 3 * * * cd /home/ubuntu/ai-projects/ai-etl-platform && ./scripts/backup-stack.sh >> /home/ubuntu/backups/ai-etl-platform/backup.log 2>&1
```

- 日志：`~/backups/ai-etl-platform/backup.log`
- 每次运行的状态：`~/backups/ai-etl-platform/state.json`（含最近 50 次的 `history`）
- **连续失败**：`consecutive_failures` 累加；达到 2 时写出 `ALERT.txt` 并在 stderr 打一行 `ALERT:`。
  成功一次即清零并删除 `ALERT.txt`。查一下就知道：
  `grep -c ALERT ~/backups/ai-etl-platform/backup.log`
- **失败的运行不会挤掉成功的备份**：轮转只统计含 `manifest.json` 的目录；
  中途死掉的目录会被改名成 `<时间戳>.partial`，留在盘上供排查，但不算一份备份。
  否则连续失败一周就会把最后一份好备份转掉——这正是备份系统最典型的自毁方式。

## 6. 恢复演练

```
scripts/restore-stack.sh                 # 恢复 latest 并断言
scripts/restore-stack.sh --backup-dir <dir> --keep
```

隔离做法（照抄 `scripts/e2e-smoke.sh`）：

```bash
COMPOSE_PROJECT_NAME=ai-etl-restore
COMPOSE_FILE="docker-compose.yml:docker-compose.eval.yml:docker-compose.restore.yml"
```

- 脚本**拒绝**任何名字里不含 `restore` 的项目，也显式拒绝 `ai-etl-platform` 和 `ai-etl-smoke`；
- 结束时是作用域内的 `docker compose down -v --remove-orphans`，碰不到演示栈的卷；
- `docker-compose.eval.yml` 把所有中间件端口收起来，`docker-compose.restore.yml` 只放回演练需要的：
  postgres 55432 / qdrant 6335 / es 9202 / minio 9002 / query-api 8082。

恢复顺序（顺序本身是断言的一部分）：**postgres → 源对象卷 → 投影 → 最后才起 query-api**。
应用启动时会跑迁移和演示收敛，所以目标库必须先于应用恢复完；而 `boot_convergence`
检查会比对应用启动前后的行数，确保「恢复」不是被种子重新写了一遍。

### 2026-09-21 演练记录

```
PASS  catalog_counts        所有表与备份一致
PASS  object_store_total    恢复 3 个对象，源 3 个
PASS  object_store_keys     键集合与大小完全一致
PASS  catalog_references    116/119 引用对象缺失，与源相同
PASS  manifest_integrity    manifest 记 116，源清单推出 116
PASS  qdrant_points         恢复 4716，记录 4716
PASS  es_documents          恢复 4684，记录 4684
PASS  retrieval_answer      引用命中 ['demo-doc-handbook']
PASS  boot_convergence      应用启动未改动行数
```

耗时 79.7 秒。恢复后的栈能回答真实问题并引用到预期文档
（问题「知识发布前需要经过哪些流程」，命中 `demo-doc-handbook`）。

注意 `catalog_references` 的语义是**保真度**而不是**环境健康**：
它断言「恢复后的缺失集合 == 备份时的缺失集合」。源环境本身缺 116 个对象，
忠实的恢复就该恢复出同样缺 116 个。要把它变成健康门禁，用 `REQUIRE_INTACT=1`。

## 7. 已知限制

- **116 / 119 份文档的源对象已经不存在**（见 `docs/optimization-plan.md` §4.7）。
  它们的正文仍在 Qdrant/ES 里，检索与问答正常；但**不可重放修复、不可重嵌入、不可下载原文**。
  字节已经没了，脚本不做假造。这份缺失现在由完整性门禁持续报告，不再隐形。
- 演练栈与演示栈共用 `.env`，因此共用嵌入模型与维度；这是刻意的，否则恢复出来的向量空间不一致。
- **恢复出来的 ES 索引拓扑与生产不完全相同**：空集群里 `PUT /documents_text` 建的是**物理索引**
  同名，而生产里它是别名。功能上等价（应用只按名字读写，且 `ensureIndex` 先 `HEAD` 到 200 就返回），
  但物理名不同。若将来引入「索引版本迁移靠别名切换」的流程，恢复脚本要跟着改。
- 备份是**文件级**的，不是崩溃一致的：Postgres 走 `pg_dump` 所以一致性有保证；
  MinIO 卷是直接 `tar`。本部署的对象写入后不可变，风险低，但不是零。
- 演练用宿主机 `python3`（3.12）与 `curl`，没有引入新依赖。
