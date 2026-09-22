# 项目优化设计方案

编制日期：2026-09-21
编制方式：代码 + 远端运行栈实测。**每条结论都标注了证据来源**，凡未实测的均明确标注为「未验证」。
范围：`ai-etl-platform`（Go ETL/Query + Python Parser/Reranker + Next.js 工作台）。

> 本文不是愿望清单。优先级按「不做的后果」排序，不按「工作量」排序。

---

## 0. 结论摘要

一句话：**主链路能跑通，但「会自己恢复」这件事没做到 —— 已定位的十一处缺陷里，五处是同一个形状：一次瞬时故障被写成持久状态，之后没人再纠正它；第六处更隐蔽，失败路径把本该暴露问题的证据自己回滚掉了；第七处最安静，目录声明了一份从不存在的源对象，而平台里没有任何东西会去核对；第八处是前七处的反面 —— 不是没人修，是根本没有能修的地方；第九处是第七处在检索侧的重演 —— 加权最高的那个信号从被声明那天起就从未生效；第十处把前九处的教训合起来用了一遍 —— 缓存漏了一个输入，而那个输入恰好是裁决自己声称的出处；第十一处又回到了最开始的形状，只是这次没人回收的不是重试，而是记录本身 —— 一份被取代的裁决走不到「过期」这个状态，于是保留期对它永不生效；第十二处把这个形状搬到了备份脚本上 —— 完整性判定算出来了、印出来了，却传不到唯一能触发告警的那个值上。**

> **修订说明（2026-09-22）**：初稿结论是「风险不在功能，在运维底座」。随后在部署环境上做了八轮
> 缺陷排查，找到并修复了 12 个**功能/可靠性**缺陷（见 §1.3）：5 个属于「失败被固化、重试变成复读」，
> 第 6 个属于「不一致的数据被当真源，而失败路径把证据回滚掉」，
> 第 7 个属于「目录声明了从不存在的源对象，且平台没有任何核对机制」，
> 第 8 个属于「失败状态没有被任何自动流程认领，也没有被任何诊断指标统计」，
> 第 9 个属于「被声明、被加权、被测试过的检索信号，从未被写入索引」（§4.6），
> 第 10 个属于「缓存键漏了一个输入，而那个输入正是裁决自己声称的出处」，
> 第 11 个属于「生命周期只沿当前被引用的那一行走，被取代的记录再也无人回收」，
> 第 12 个属于「判定存在，但传不到唯一能触发告警的那个值上」。
> 原结论因此**不成立**，已按下表修订。
> 同时 P0 的 §1.2（全栈无备份）已从「待做」变成「已做并在隔离栈上实测通过」。

| 类别 | 结论 |
| --- | --- |
| 功能完善度 | 主链路（上传→解析→向量化→检索→问答→发布审批）已闭环；缺口集中在**自助能力**、**合规审查深度**两处（**备份恢复**已由 §1.2 补齐） |
| 系统设计 | 服务边界清晰、CI 门禁完整、租户/权限/证据链设计是扎实的；问题在**配置一致性**、**状态文档三源冲突**、**单文件职责过载** |
| 可靠性 | **真正的短板在故障恢复路径**：瞬时失败被持久化后没有自愈机制，重试路径要么不存在、要么复读旧结果。见 §1.3 |
| 最紧急项 | **没有 P0 挂着**。三项 P0 都已处理：ES 永久 yellow（§1.1.1）、全栈无备份（§1.2）、磁盘濒满（§1.1.2，可用空间 9.2GB → 63GB）。下一步按 §7 的执行顺序走 |
| 已修复项 | 12 个功能缺陷 + ES 永久 yellow（真因是**单节点配了 1 副本**，不是磁盘水位）+ P0 备份与恢复演练（§1.2）+ ES 水位百分比化、宿主机磁盘告警、磁盘回收（§1.1.2）。见 §1.1、§1.2、§1.3 |

---

## 1. P0 —— 必须先处理，否则会停服

### 1.1 磁盘濒满 + Elasticsearch 永久 yellow（两个独立问题，都已处理）

> **勘误（2026-09-21 深查后修正）**：本节初稿把 ES 的 unassigned shard 归因于磁盘水位，
> **这个归因是错的**。真因是单节点集群配了 1 个副本。磁盘濒满是**另一个**独立风险，
> 两者恰好同时存在，但互为因果的只有前者。

#### 1.1.1 ES 永久 yellow —— 真因是单节点配了副本（已修复）

**证据（推翻初稿的关键一步：看 decider，而不是看 `reason`）**

```
$ ES allocation/explain → can_allocate: "no", reason: "CLUSTER_RECOVERED"
                          node_allocation_decisions[0].deciders →
                            same_shard → "a copy of this shard is already allocated to this node"
$ ES _cat/shards/documents_text_v2 → documents_text_v2 0 p STARTED / 0 r UNASSIGNED
$ 清理出 19GB 可用空间后重新 explain → 仍然 can_allocate: no（排除磁盘水位）
$ PUT /documents_text_v2/_settings {"index":{"number_of_replicas":0}} → green / 100% / 0 unassigned
```

`can_allocate` 的 `reason` 是节点级摘要，会盖住真正的 decider；必须下钻到
`node_allocation_decisions[].deciders` 才能看到拒绝理由。

**机制**：`internal/es/indexer.go` 的 `ensureIndex` 建索引时只发 `mappings`，ES 套用默认
`number_of_replicas: 1`。单节点集群上这个副本永远分配不出去 —— 分配器不允许把分片副本放到
已经持有主分片的节点上（`same_shard`），于是集群永久 yellow，`active_shards_percent` 卡在 50%。

**修复**：commit `579d8b8` + `5516d00` 新增 `ES_INDEX_REPLICAS`（默认 0，即单节点语义），
经 `es.WithReplicas()` 透传到建索引请求；`ES_INDEX_REPLICAS=1+` 供多节点部署使用。
线上已对既有索引执行 `number_of_replicas: 0`，4681 篇文档未受影响。

**验收判据**：`ES _cluster/health` → `status: green`、`unassigned_shards: 0`、`active_shards_percent: 100%`。**已达成。**

#### 1.1.2 磁盘濒满 —— 已清理、水位已改、告警已落地（已修复）

**证据**

```
$ df -h /            → /dev/vda2  197G  182G  7.3G  97%     （第一次清理前）
                     → 可用 19G                              （第一次清理后）
                     → 可用 9.2G   96%                       （2026-09-21 傍晚，又掉回去了）
                     → 可用 63G    67%                       （清 build cache 后，终态）
$ docker system df   → Images 106.3GB(可回收 81.71GB) / Build Cache 58.18GB(默认口径只报 1.45GB 可回收)
                       Local Volumes 24.67GB(可回收 19.98GB)
```

**机制**：`docker-compose.yml` 原先把 ES 水位设为**绝对值** —— `low=8gb` / `high=6gb` /
`flood_stage=4gb`。可用空间 7.3GB 时确实低于 `low`，但实测证明这**没有**导致 unassigned shard
（见 §1.1.1）。真正的后果是另一条：

| 可用空间 | ES 行为 | 清理前 | 清理后 |
| --- | --- | --- | --- |
| < 8GB | 拒绝分配**新**分片 | 已触发 | 已解除 |
| < 6GB | 尝试迁移分片（单节点无法迁移） | 还剩 1.3GB | 已解除 |
| < 4GB | **全部索引转只读，写入被拒** | 还剩 3.3GB | 还剩 15GB |

**绝对值为什么是错的**：ES 实际看到的容量是 **211.25 GB**，`flood_stage=4gb` 只占 **2%**。
磁盘越满，这个「保护」相对越小 —— 等于磁盘快满了 ES 还在继续分配分片。百分比表达的是固定预留。

**已完成（水位）**：`docker-compose.yml` 改为 `low=95%` / `high=96%` / `flood_stage=97%`
（仍可用 `ES_DISK_WATERMARK_*` 覆盖），`.env.example` 同步。

**一个必须避开的坑（差点把演示栈打停）**：ES 自己的默认百分比是 `85/90/95%`。照抄会把索引
**立刻打成只读**，因为实测已用 **95.36%** > 95%。所以取值不能凭默认值，必须先读 ES 看到的真实
数字再定。改完的升序是这样（free 百分比）：

```
告警 warning  15%  free  → 31.7 GB      早期预警
告警 critical 10%  free  → 21.1 GB      该动手了
ES low        95% used → 5% free        ES 不再接收新分片
ES high       96% used → 4% free        尝试迁走（单节点迁不动）
ES flood      97% used → 3% free        全部索引转只读
```

改完时的 free 是 **4.64%**，落在 `low` 与 `high` 之间：ES 拒绝新分片，但**仍然可写** —— 演示不断。
这一条是实测的，不是推断（见下方验收判据的写入探针）。
腾出空间后 free 升到 31.84%，三项水位全部回到 `ok`。

**已完成（告警）—— 计划里漏掉的前提**：原计划写「Prometheus 已有，加一条规则即可」。
**实际 Prometheus 根本没有 `node_filesystem_avail_bytes`**：`infrastructure/prometheus.yml` 只抓
prometheus / query-api / etl-worker / qdrant，没有 node-exporter。所以规则写出来也永远不会响。
补的东西：

1. `docker-compose.yml` 新增 `node-exporter`（只读挂 `/proc`、`/sys`、`/`，`--path.rootfs`），
   端口 `NODE_EXPORTER_HOST_PORT`（默认 9100）。
2. `infrastructure/prometheus.yml` 新增 `node-exporter` 抓取任务。
3. `infrastructure/rules/host-alerts.yml`：两条规则，`HostRootDiskSpaceLow`（<15%，warning，for 10m）
   与 `HostRootDiskSpaceCritical`（<10%，critical，for 5m）。表达式用 `min by (mountpoint)` 收敛标签 ——
   直接写两个指标的比值会把 `device`/`fstype`/`device_error` 带进告警标签，而 `device_error` 在健康挂载上是空值，
   导致健康主机与故障主机的标签集不同，去重和静默规则都会错。
4. `infrastructure/tests/alert-rules.test.yml` 新增 3 个用例（15% 只 warning 不升级、10% 升级为
   critical、腾出空间后消解）。顺带记一个坑：**在一个 `values` 字符串里混写两个 `value xN`
   会静默产生另一条序列**，恢复用例因此一开始根本没在测消解 —— 改成显式样本列表才对。
5. `docker-compose.eval.yml` 给 `node-exporter` 也加 `ports: !reset []`，否则隔离栈
   （smoke / restore）会和演示栈抢 9100。两个脚本的 `COMPOSE_FILE` 都含 eval 覆盖，所以一处即可。
6. `infrastructure/rules/README.md` 与规则测试说明同步。

**已完成（腾空间）—— 判据达成，且没有碰你其他项目的任何东西**

`docker system df` 报的 build cache「可回收 1.45GB」是**默认 prune 的口径**，不是全部。
`docker buildx du` 显示整块 build cache 是 **58.18GB**，全部可回收 —— 它只影响下次构建速度，
不动镜像、不动容器、不动卷。清掉之后：

```
$ df -h /            → 197G  180G  9.2G  96%    （清理前）
                     → 197G  126G   63G  67%    （清理后）
$ docker system df   → Images 106.3GB → 49.13GB   Build Cache 58.18GB → 0B
```

**回收 53.8GB，可用空间 9.2GB → 63GB（free 4.64% → 31.84%）**，判据 > 25GB 达成。
`Images` 从 106.3GB 掉到 49.13GB，是因为其中约 57GB 本来是被共享的 build cache 层重复计入的 ——
**不是删了镜像**：`docker system df` 的在用计数从 `Images 31 / Containers 30` 保持不变，
30 个在跑的容器与它们引用的镜像一个没动。

清理后复验：本项目 19 个容器全部在跑，其他项目的 11 个容器也没受影响；
真实问答命中 `demo-doc-handbook`；ES 写入 `201`、计数回到 4684；ES 三项水位全部 `ok`；
**两条磁盘告警在 Prometheus 与 Alertmanager 里都已消解** —— 告警的 fire 与 resolve
两个方向都实测过，这比只看到它响一次更有说服力。

**还剩下的（不需要动）**：`Images` 仍有 24.59GB 可回收、`Local Volumes` 19.98GB 可回收，
大部分属于 `openclaw`/`umami`/`p_blog_2`；本项目自己无引用的缓存卷
（`ai-etl-go-build-cache` 939MB、`ai-etl-go-mod-cache` 348MB）与旧命名空间的
`ai-etl-pipeline_*`（合计约 14MB）也能清，但空间已不紧张，而 go 缓存卷是构建提速的承重结构，
**留着比清掉划算**。

**验收判据（水位与告警，全部满足）**

- ES 水位为百分比、升序正确、且**改完仍可写**：6 项断言全 PASS（`watermarks_are_percentages` /
  `watermarks_are_ordered` / `cluster_green` / `index_not_read_only` / `write_accepted` / `count_moved`），
  写入探针走真实别名 `POST /documents_text/_doc` → `201 created`，计数 4684 → 4685 → 删除后回到 4684。
- 反向验证：把绝对值以 transient 覆盖写回（等价于修复前的行为）→ `watermarks_are_percentages` **必 FAIL**；
  清除覆盖 → 全 PASS。
- `promtool test rules` **SUCCESS**；反向验证：删掉规则文件 → FAILED，把阈值改成 50%/5% → FAILED，
  恢复 → SUCCESS（sha256 一致）。
- 抓取目标 `node-exporter: health=up`；`/api/v1/rules` 有 `ai-etl-platform-host` 组、两条规则 `health=ok`。
- 告警**完整走了一遍生命周期**：两条都从 `pending` 转 `firing`（critical 在 pending+5m、
  warning 在 pending+10m），Alertmanager `/api/v2/alerts` 都收到；腾出空间后又都**消解**
  （Prometheus 与 Alertmanager 两侧均为空）。fire 与 resolve 两个方向都实测过 ——
  只看到它响一次，并不能证明故障解除后它会安静下来。

**验收判据（磁盘，全部满足）**：`df -h /` 可用空间 **63GB > 25GB**（清理前 9.2GB）。
告警随之消解，且消解本身也是实测的。

**顺带看到的一条线索（已查清并修复，见 §1.3 缺陷 8）**：Alertmanager 里另有一条
`IndexGenerationFailed` 在 active（`state="failed"`），与 `documents` 表里 7 行 `status='failed'`
相呼应。当时只把它记入 PROGRESS 待查；后续排查确认这是**第八处独立缺陷** —— 这 7 份生成卡在
`failed` 之后没有任何自动流程会认领它们，`repair_attempts` 恒为 0，而唯一统计"修复已耗尽"的指标
只数 `active`，于是死生成对整套信号完全不可见。

---

### 1.2 全栈没有任何备份（已修复）

**当时证据**

```
$ ls scripts/ | grep -i "backup|restore|dump"   → 空
$ crontab -l                                    → 空
$ ls ~/backups                                  → 仅 openclaw-upgrade-20260328-165451（36MB，与本项目无关）
```

**影响**：PostgreSQL 是文档版本、generation、发布 release 和审批决策的权威（`LEARNINGS.codex.md` 明确定义）。它当时没有备份。磁盘满导致的写入失败、卷损坏、误删，任一发生都不可恢复。这与 1.1 是**叠加风险**，不是独立风险。

**已做**

1. `scripts/backup-stack.sh`：`pg_dump -Fc` + MinIO 数据卷 tar（只读挂载）+ Qdrant 快照 + ES 逻辑归档，
   落到 `~/backups/ai-etl-platform/<UTC 时间戳>/`。单次 75 MB / 2.5 秒，保留 7 份（≈525 MB，`/` 可用 11 GB）。
2. `scripts/restore-stack.sh`：在隔离项目 `ai-etl-restore` 上恢复并断言，**真跑过**（2026-09-21）。
3. cron 每日 03:17（Asia/Shanghai）执行，日志 `~/backups/ai-etl-platform/backup.log`；
   `state.json` 记录连续失败数，达到 2 就写出 `ALERT.txt`。**失败的运行不会挤掉成功的备份**：
   轮转只统计含 `manifest.json` 的目录，中途死掉的目录改名 `.partial` 留在盘上供排查 ——
   否则连续失败一周就会把最后一份好备份转掉。
4. RPO/RTO、恢复流程、演练记录写进 `docs/backup-and-recovery.md`。

**投影为什么也必须快照，而不是「反正能重建」**

ADR-0010 说 Qdrant/ES 是可重建投影，但要求恢复方案证明重建能满足 RTO，否则快照是必需的。
本部署的重建**连发生都发生不了**：`EMBED_ENDPOINT=http://host.docker.internal:11434/api/embeddings` 是宿主机上的
ollama（bge-m3），不在栈内、不被任何备份覆盖。宿主机丢失——这正是备份要防的场景——投影就无法重建。
所以快照不是可选优化。

**顺带发现的东西（比备份本身更值钱）**

备份必须知道「要备份的东西还在不在」，于是加了完整性门禁，第一次运行就报出：
**119 份文档的 `object_key` 指向源对象，而对象存储里只有 1 个对象**（那 1 个是探针自己传的）。
平台里没有任何东西会发现这件事：文档照常 `completed`、检索照常作答、index manifest 照常健康、
`GET /v1/documents/{id}` 照常返回一个 `file_size`。第一个会失败的地方是修复重放。
其中 **3 份可以确定性重建**（演示种子自己的文档，内容由代码生成），已作为**缺陷 7** 修掉并线上断言；
剩下 **116 份不可恢复**，改为由完整性门禁持续报告（`integrity.status = degraded`），不再隐形。
见 §4.7 与 §1.3 的缺陷 7。

**验收判据（全部满足）**

- 恢复演练在隔离栈跑通，8 项断言全 PASS，恢复后的数据通过真实问答且引用命中 `demo-doc-handbook`（RTO 实测 79.7 秒）；
- cron 条目存在且有成功日志：`grep "backup finished" ~/backups/ai-etl-platform/backup.log`。

---

### 1.3 已修复的 12 个缺陷（前五处同一形状：失败被固化，重试变成复读）

排查方式统一为：**先在部署环境复现，再定位到具体代码行，再加回归测试，再反向验证（还原修复后测试必须失败），最后部署并线上断言**。下表每条都有线上证据。

| # | 缺陷 | 线上证据 | 真因 | commit |
| --- | --- | --- | --- | --- |
| 1 | ES 集群永久 yellow | `_cat/shards` → `r UNASSIGNED`；`allocation/explain` 的 decider 是 `same_shard` | `ensureIndex` 建索引只发 `mappings`，ES 默认 `number_of_replicas: 1`，单节点永远分不出去 | `579d8b8` `5516d00` |
| 2 | 发布中心预审队列被一行数据永久堵死 | 每 5 秒一条 `SQLSTATE 23505`（10 分钟 84 次） | 演示种子写死 `request_id`，而 `stableID` 把 count/digest/revision 算进哈希 → 派生 ID 不等 → 插入撞上第二个唯一约束；`RunPendingReviews` 首个失败即 `return err`，一行坏数据停摆整条队列 | `e6d1923` |
| 3 | 文档清单的块数恒显示「—」 | `documents` 表 119 行里大 `.txt`/`.md`/PDF 全为 `n/0` | 文本路径的 `totalChunks` 只在走 parser 服务分支时才从 channel 读到；PDF 路径先写对又被「第 x / y 页」的进度写回覆盖成 0 | `4cf5963` |
| 4 | 演示文档的 `index_manifests` 撒谎 → Agent 预审永远失败 | ES 实测 handbook=3 / onboarding=0 / payroll=0，但清单声称各 3；run 的 `error="exact candidate content is unavailable"` | `ensureDemoShowcaseIndex` 只索引 handbook，清单却给三份文档都写了 3 条；预审按 `document_version_id`+`generation_id` 精确取块得 0 条 | `624d936` |
| 5 | 预审的瞬时失败被永久缓存，重试变成复读 | 计划器 30s 超时后，Redis 里的 run 永久 `failed`；只能手工删 key 才恢复 | 预审 run id 只由候选派生，而 `failed` 是终态且 `ExecuteNext` 对终态直接短路 → 重试复读旧错误，从不重新调用计划器。自动恢复路径（预审过期 → `needs_info` → 队列重新拾取）每轮只消耗一个 `RELEASE_REVIEW_TTL`，Agent 一次都没重跑 | `91a47fc` |
| 6 | 演示清单的期望摘要是占位串，索引对账**周期性**永久报错 | `etl-worker` 每 30 分钟一条 `index manifest reconciliation failed … load repair ingestion job: no rows in result set`（08:30:03 / 09:00:03 / 09:30:03，间隔恰为 `INDEX_RECONCILE_LEASE`） | 种子把**文件哈希**（`sha256:demo-handbook` 这类占位串）同时写进 `expected_chunk_digest` 与两个投影摘要；按 `IdentityDigest` 复算真实值是 `sha256:e4adc7b7…` / `sha256:c9241b97…` / `sha256:4f2b1bb0…`。种子又只插 `ingestion_jobs`、不插 `ingestion_outbox`，而 `FinishReconciliation` 靠两者 JOIN 找重放目标 → 差异永远无法修复。失败路径 `tx.Rollback()` 把 `last_reconcile_error` 与 `last_reconciled_at` 一起回滚，于是清单看起来健康、可发布（`CurrentCandidate` 读的就是这个谓词），却永不收敛；而认领顺序是「最久未对账优先」，它们因此每轮都排在队首 | `3f1deb5` |
| 7 | 演示种子声明了它**从未写入**的源对象 | 修复前：`ListObjectsV2` 权威返回对象存储 **0 个对象**，而 `documents` 表 **119 行全部** `object_key <> ''`、`file_size` 从 35 到 45,799,879 字节；用真实 `/v1/upload` 传探针 → 对象正常落盘（111 B），证明坏的不是链路而是历史对象 | `demo_showcase.go:146,148`（修复前）把 `object_key` 写成 `"demo/"+doc.docID+".md"`、`file_size` 写死 `2048`，**从不调用对象存储的 Upload**；且 `ensureDemoShowcase` 当时跑在 `s3.New` 之前，物理上拿不到客户端。目录因此只是一句声明 | `7cea45f` |
| 8 | `state='failed'` 的生成没有任何自愈路径，且对诊断指标不可见 | `ai_etl_generation_manifests{state="failed"}=6`、`oldest_age_seconds{state="failed"}=1655531s`（≈19.2 天）；`IndexGenerationFailed` 自 2026-09-12T06:56:48Z firing 九天；这 6 份 `repair_attempts=0`、`last_reconciled_at` 恒为 `NULL`，而同一时间对账日志每 5 分钟稳定报 `checked:20 healthy:20 diverged:0 repair_exhausted:0` | 三处，都在 `indexmanifest/postgres.go`：① `ClaimReconciliation` 的 `WHERE state='active'` 让 failed 永不被认领 —— 这条本身是刻意的（只有 active 才有活投影可跨后端比对），但它**没有配套的接管路径**；② `OperationsSnapshot` 的 `repair_exhausted` 只数 `state='active'`，死生成对信号完全不可见；③ `Retry` 不清 `expected_chunk_count/expected_chunk_digest`，而 `SealExpected` 以 `IS NULL` 为守卫，封印过的失败再重试必 `ErrConflict` | `8b5e2f8` `7307e54` |
| 9 | ES 词法检索里权重最高的 `file_name` 加权是死代码 | `documents_text_v2` 的 mapping 无 `file_name`，`chunks with file_name = 0 / 4684`；`match_phrase(file_name,"员工手册")` 命中 0，而同一查询在正文侧命中 5；`backend_candidate_counts.elasticsearch` 对只靠标题才该命中的问句恒为 0 | 四处：`model.Task` / `model.Chunk` 没有 `FileName` 字段；`esChunkDoc` 没有该字段；ES mapping 没有该 property；入库链路（流式与 OCR/PDF 两条产出路径）从不填充。`titleAwareShouldClauses` 却发出 `match_phrase(file_name, boost 6.0)` 与 `match(file_name, boost 3.0)` —— ES 对未映射字段不报错、只是永不匹配，于是**静默降级**为「只有正文匹配」；`elastic_test.go` 只断言了子句存在，没有任何测试断言该字段被写入索引 | `5e172bb` |
| 10 | 预审裁决的缓存身份漏了模型，且 `report.model` 是每次读取时从当前配置**合成**的 | 改 `LLM_MODEL` 并重建 query-api 后连续三次调用返回**逐字相同**的 `review-run-5d0091b5ac8e12bf1c0a8bd3fd823c0a`，而 `review.model` 跟着配置走（`deepseek-v4-flash` → `deepseek-v4-flash-probe` → `deepseek-v4-flash`）；B 那次带着不存在的模型名却返回 `completed` 且 summary 逐字相同 —— 计划器根本没被调用（run 在 Redis 里跨容器重建持久） | 两处：`agentapi/review.go` 的 `reviewRunID` 哈希载荷只有 tenant / candidate / prompt 版本 / 尝试序号，**没有模型**；`agentapi/service.go:285` 每次读取都把 `report.Model` 盖成 `s.reviewModel`，而确定性报告（`review.go:787`）装配时根本没有 `Model` 字段 —— 于是每轮调用都重写已存储裁决的模型名，一份由 A 产出的裁决被读回时标成了 B | `78e3fb5` |
| 11 | `release_center_reviews` 的保留期对「被取代的评审行」永不生效，表只增不减 | demo 租户 2 条 `failed` 预审行（`review-69e42208…`、`review-a26fb4d1…`）在 2026-09-21 08:17:56 写入、10 分钟后即被成功的预审取代；两条都不被任何 request 引用，`expire_due_reviews` 的可达性判定为 **false**。全表 27 行里有 4 行处于「无人引用」状态，而 `purgeable_now = 0` | 两处，都在 `releasecenter/store.go`：① `ExpireDueReviews` 通过 `JOIN release_center_requests q ON q.review_id=rv.review_id` 遍历评审行 —— 只认「请求当前指向的那一行」，被取代的行从此不可达；② `PurgeExpiredReviews` 只删 `status='expired'`。两者相乘：**在 TTL 到期前被取代的行永远走不到 `expired`，于是永远删不掉**，`RELEASE_REVIEW_RETENTION`（90 天）对它完全失效 | `5afc6b1` |
| 12 | 备份脚本把完整性判定吞掉了：文档承诺 `REQUIRE_INTEGRITY=1 → exit 3`，实际恒为 0，于是备份自己的升级机制对「源已损坏」这个唯一要报的条件失明 | 部署环境实测 `REQUIRE_INTEGRITY=1 ./scripts/backup-stack.sh` **exit=0**，同时日志里印着 `ERROR integrity is degraded`；`state.json` 连续 6 次运行都是 `last_integrity=degraded` 配 `consecutive_failures=0`，`ALERT.txt` 从未产生 | 两处，都在 `scripts/backup-stack.sh`：① 清单的退出码被 `manifest_code=$?` 接住后**只用于一行日志**（原 `if manifest_code -eq 3; then log ERROR`）；② 最终退出阶梯只由 `FAILURES` 决定（`if FAILURES > 0: exit 2; exit 0`），而那条路径从不递增它。全脚本**没有任何一处 `exit 3`** —— 文档承诺的码不可达。而 `record_state` 只按退出码计数、`consecutive_failures>=2` 才写 `ALERT.txt`，所以判定永远传不到告警 | `4668d92` |

**缺陷 6 的报错周期，实测与直觉相反**：对账每 5 分钟跑一轮，但**报错每 30 分钟才出现一次**。
原因在租约：`ClaimReconciliation` 把 `reconcile_lease_until` 推到 `now()+INDEX_RECONCILE_LEASE`（30m）
并**提交**，而失败分支的 `tx.Rollback()` 只回滚了自己那一份写入、清不掉这个租约。于是这三份清单
被认领一次、失败一次，然后被自己的租约挡住 29 分钟，期间每轮对账都报 `healthy: 20/20`。
这解释了为什么它看起来"时好时坏"：**故障在 29/30 的时间里是自我隐藏的**，唯一症状是那条周期性的
ERROR 日志。排查时不能只看"最近一次对账是否成功"，必须看 `last_reconciled_at` 是否在推进。

**缺陷 6 修复后的线上状态（2026-09-21 09:35 UTC 实测）**：三份清单的
`expected_chunk_digest = qdrant_digest = elasticsearch_digest`，值分别为
`sha256:e4adc7b7…` / `sha256:c9241b97…` / `sha256:4f2b1bb0…`；`last_reconciled_at` 开始推进，
`last_reconcile_error` 为空，`repair_attempts=0`。全表 `state='active'` 的 105 份清单
`diverged=0` 且 `never_reconciled=0`；`ingestion_jobs` 中找不到 outbox 的行数为 **0**
（修复前是 3）。对账日志从 ERROR 变为 `checked:20 healthy:20 diverged:0`，
`query-api` 无 ERROR，演示租户仍是 3 份文档、2 条 `approval_pending`（需 1 / 2 名审批人），
问答命中 `demo-doc-handbook`。

**缺陷 6 的一个细节，值得记住**：三张表之间存在**每 5 秒生效一次**的强耦合 ——
`index_manifests.expected_chunk_digest`、`release_center_requests.expected_chunk_digest`、
以及真实投影的观测摘要必须完全一致。`ReconcileStaleRequests` 每轮比对它们，不一致就把请求
从 `approval_pending` 降级为 `needs_info`；`ApprovalService.Decide` 在审批时再比对一次，不一致
直接返回 `ErrStaleReview`。所以**只改清单不改请求**，会让演示里的审批队列在 5 秒内塌掉。
这类"看起来只动了一处"的改动，必须先查清所有读取方再动手。

**缺陷 5 的一个细节，值得记住**：`AGENT_RUN_TTL=24h` 让这个 bug 看起来「过一阵会自己好」——
run 记录 24 小时后过期，下一次重试才是真的重跑。也就是说恢复靠的是**缓存过期这个副作用**，
而不是任何重试逻辑。对演示场景，这意味着一个 30 秒的网关抖动能让文档卡住 24 小时，
除非人工写「人工例外理由」放行。修复后 run id 含 prompt 版本与尝试序号，
`AGENT_REVIEW_MAX_ATTEMPTS`（默认 3）给出有界重试，失败的 run 保留在盘上作为证据。

**缺陷 7 的形状与前面六个都不同，也是本轮最安静的一个**：前六个都是「东西坏了但没人修」，
这一个连「坏」都看不出来 —— 平台里**没有任何代码**会把 `documents.object_key` 与对象存储对一遍。
文档 `status='completed'`、`file_size` 是一个具体字节数、检索照常作答、
`GET /v1/documents/{id}` 照常返回元数据、`index_manifests` 照常 `healthy`（对账比的是**投影之间**的
一致性，投影不需要源对象）。第一个会失败的地方是**修复重放**：`FinishReconciliation` 靠重放
ingestion 事件去重新物化对象，而没有对象可重放时它只能报错 —— 这个缺口把自己的修复路径也堵死了。
所以它不是被排查发现的，是**被备份的完整性门禁逼出来的**：备份必须先知道「要备份的东西还在不在」。
修复后线上断言：`demo/demo-doc-handbook.md` 513 B / `demo-doc-onboarding.md` 401 B /
`demo-doc-payroll.md` 395 B，`documents.file_size` 与观测字节数**逐一相等**；
缺失计数从 119 降到 116（那 3 份是唯一能确定性重建的）。剩余 116 份见 §4.7。

**缺陷 8 是前七处的反面，也是十一处里唯一一处「不是没人修，是根本没有能修的地方」**：前七处都有代码
在错误的时刻做了错误的事，这一处是**代码根本不存在**。`ClaimReconciliation` 只认领
`state='active'`（这条本身是刻意的：只有 active 才有活投影可跨后端比对），而承载那份生成唯一
一次投递的 `ingestion_outbox` 行早已 `published_at` 非空，relay 也不会再取它。于是这批生成
落在两条路径中间的缝里：**没有任何代码会把它们捞回来**。它能藏九天，是因为诊断指标站在了
错误的一边 —— `repair_exhausted` 只数 active，死生成从未被计入任何一条信号；`IndexGenerationFailed`
虽然一直 firing，但同一时间对账日志报的是 `healthy:20 diverged:0`，绿灯让人以为那只是历史噪音。

**修复的形状**：给 failed 生成一条**与 active 平行、但有界**的认领路径，并让「耗尽」成为可观测终态。

- `ClaimFailedRepairs` / `ScheduleFailedRepair`：独立认领 `state='failed'` 且
  `repair_attempts < N` 的清单，在**行锁内**读 job 状态后三分支 —— 无 job 可重放 → 扣预算并报
  `ErrNoReplayPath`；job 已在 `queued/published/processing` → 释放租约且**不扣预算**（重放已在飞，
  报 `ErrRepairInFlight`）；其余 → 清 `ingestion_outbox.published_at` 并重开 `ingestion_jobs`。
- **重驱动不再改 `state`**。这是本次自查出的第二个真缺陷：第一版先把 state 改成 `building` 再重放，
  而重建不保证产出同一个 `generation_id`（id 由构建定义派生）→ 清单既不会被重建、也不会再被报
  `failed`，只会被报成 stalled。第一版上线后线上确实出现 3 行 `building` + 空 `last_error`，
  已按部署时间边界恢复为 `failed`，并补 `state != 'failed'` 断言 + 反向验证 R6。
- `Retry` 与重驱动共用同一条语句并清空封印（`expected_chunk_count/expected_chunk_digest` 等），
  否则 `SealExpected` 的 `IS NULL` 守卫会让封印过的失败重试必然 `ErrConflict`。
- `OperationsSnapshot` 的 `repair_exhausted` 扩到 `state='failed'`，并新增
  `ai_etl_generation_reconciliations_total{outcome="repair_replayed"|"repair_unavailable"}`。

**线上断言（2026-09-21 13:42–14:07 UTC，第二次部署后）**：对账五轮依次认领
`replayed=6 / 6 / 4 / 1 / 1`（累计 **18**），此后每轮 `replayed=0` —— 预算走满即停，不再空转。
7 份清单的 `repair_attempts` 全部到 3，`sum(repair_attempts)=21=7×3`，与
`repair_replayed(18) + repair_pending(3)` 精确吻合，`last_reconcile_error` 全部清空。
四个独立来源给出同一个耗尽信号：DB `repair_attempts=3`、worker `/metrics`
`ai_etl_generation_diagnostics{condition="repair_exhausted"}=7`、Prometheus 同值、
Alertmanager `IndexGenerationRepairExhausted` 自 13:52:48Z 起 `active`。

**这 7 份重放之后仍然失败，而这是正确行为**：根因是源对象已不存在
（`materialize object: copy object to temp file: The specified key does not exist.`，即 §4.7 的
116 份缺失），重放不可能成功。修复保证的是**有界重试 + 耗尽可见**，不是「一定能自愈」——
把不可恢复的失败从「静默」变成「响亮且不再重试」，才是这条路径该做的事。

**缺陷 9 是缺陷 7 在检索侧的重演：声明与实现之间隔着一整个测试的盲区。** 缺陷 7 里，
种子声明了源对象却从不写入；这里，`titleAwareShouldClauses` 声明了 `file_name` 的加权却从不写入，
而唯一的测试（`elastic_test.go`）只断言「查询里有这两条子句」—— 声明方被测试覆盖，
写入方连字段都不存在。两处的共同点是：**断言停在了发出请求的那一侧**。

它的静默程度比缺陷 7 更高：缺陷 7 至少能被备份的完整性门禁逼出来（要备份就得先知道东西在不在），
这一处没有任何门禁会碰它 —— 词法分支的 `minimum_should_match: 1` 由 `content` 子句兜底，
所以查询照常返回、答案照常生成，只是召回质量比设计意图低一档。用户按文档名提问时，
权重最高（boost 6.0）的那条信号从未参与过打分。

**修复的形状**：把「上传时的原始文件名」从任务一路带到索引，并让存量数据不必重传即可补上。

- `model.Task` / `model.Chunk` 增加 `FileName`，在 `cmd/api/upload_handlers.go` 与
  `internal/gateway/gateway.go` 两个 task 构造点填入 `header.Filename`。
- **两条 chunk 产出路径都要打标**：流式路径（parser 服务，`chunkCh` 消费循环）与 OCR/PDF 批路径
  （`processPDFBatches` 的 `result.Chunks`）是**互相独立**的，只改前者会让 PDF 文档仍然没有该字段 ——
  这是回归测试当场抓出来的（`chunk doc-name_0000 lost the upload file name, got ""`）。
- `esChunkDoc` 加字段、`mapChunkToESDoc` 填值、建索引 mapping 加 `file_name`
  （`type=text, analyzer=cjk, search_analyzer=cjk`，与 `content` 同 analyzer，否则查询侧与索引侧切词不一致）。
  **注意存量索引拿不到 `search_analyzer`**：对已是 text 的字段追加它会被 ES 拒绝，所以回填只写了
  `type` 与 `analyzer`；缺 `search_analyzer` 时查询与索引两侧都用 `cjk`，功能上等价（见 §4.6 实测结果）。
- 存量数据用 `scripts/backfill-es-file-name.sh` 回填，**不重建索引**：`documents` 表里已有权威文件名，
  逐文档 `_update_by_query` 幂等写入即可，比重建 4684 条索引风险低得多。
  `scripts/migrate-es-cjk-index.sh` 的 mapping 也同步加上该字段，供未来重建时使用。

**线上断言（2026-09-22 01:2x UTC，部署 `5e172bb` 之后）**：回填 `documents=119 chunks_updated=4684
chunks_with_file_name=4684/4684`；`match_phrase(file_name,"员工手册")` 命中 **0 → 5**。

端到端反向验证用一个**只可能由 `file_name` 子句命中**的问句构造（「手册里有哪些规定」：
不含连续的「员工手册」故不触发 title 匹配，正文两条子句实测命中 0，唯一命中来自
`match(file_name, boost 3.0)`）：

| 步骤 | `chunks_with_file_name` | `backend_candidate_counts.elasticsearch` |
| --- | --- | --- |
| A 基线（`file_name` 在位） | 9 | **3** |
| B 移除 demo 全部 `file_name`（= 还原修复前的索引状态） | 0 | **0** |
| C 重跑回填脚本 | 9 | **3** |

这是**在部署环境上**做的反向验证：不是还原代码，而是还原线上索引，观测到的差异只能来自该字段。
它同时证明了回填脚本幂等（A→C 结果一致）。代码级反向验证另做三项，还原后守卫测试全部失败：
`esChunkDoc` 去字段 → `TestMapChunkToESDoc_CarriesTheUploadFileName` FAIL；mapping 去属性 →
`TestNewHTTPIndexerCreatesVersionedCJKIndexAndWriteAlias` FAIL；`processPDFBatches` 去打标 →
`TestProcessTask_CarriesTheUploadFileNameIntoEveryChunk` FAIL。

**排查中的一个假警报，值得记下来**：`入职指南什么时候生效` / `薪酬核算的周期是多久` 这两个问句
在 ES 直查能命中 3 条，而线上 API 报 `elasticsearch: 0`，看起来像又一处缺陷。
查清后是**正确行为**：那 3 条来自 `demo-doc-onboarding`（`documents.publication_status='draft'`），
而 `publicationrelease.ResolveVisibility` 要求候选精确匹配 `document_releases` 里已发布的
version/generation —— 草稿文档被正确挡在证据之外。另外 `backend_candidate_counts` 是在可见性过滤
**之后**统计的（`retrieval/engine.go:297-301`），所以 0 表示「候选被丢弃」，不表示「ES 没返回」。

**缺陷 10 是缺陷 5 的同一条，只是漏掉了一个输入，而且它还多了一层谎报。**

缺陷 5 把 prompt 版本与尝试序号放进了预审 run 的身份，理由写得很清楚：run 是「这个候选、由这个
prompt 审过」的缓存，漏掉一个输入就等于把一份裁决永久缓存下去。**但模型也是这个裁决的输入，
当时没放进去** —— 于是 `AGENT_RUN_TTL`（24h）之内把 `LLM_MODEL` 指向另一个模型，命中的还是
旧 run，新模型一次都不会被问到。

第二层更糟：`report.Model` **根本不是溯源信息**。确定性预审装配的报告
（`review.go:787`）里没有 `Model` 字段，而 `service.go:285` 在**每次读取时**把当前配置盖上去 ——
于是每一轮调用都会把已存储裁决的 `model` 重写一遍。一份由模型 A 产出的裁决，被读回时标成了模型 B。

**线上复现（2026-09-22 01:46–01:53 UTC）**：改 `.env` 的 `LLM_MODEL` 并重建 query-api，
连续三次调用返回**逐字相同**的 `review-run-5d0091b5ac8e12bf1c0a8bd3fd823c0a`，而
`review.model` 跟着配置走（`deepseek-v4-flash` → `deepseek-v4-flash-probe` → `deepseek-v4-flash`）。
run 存在 Redis（`agent:run:<id>`，跨容器重建持久），所以 B 那次**确实复用了旧裁决**：
它带着一个不存在的模型名却返回 `status=completed`、与原裁决逐字相同的 summary —— 计划器根本没被调用。

**修复的形状**：把模型放进 run 的身份，并把模型记在 run 自己身上。

- `reviewRunID` 的哈希载荷加 `review_model`，与 prompt 版本、尝试序号并列。
- `StartManagedReview` 把 `review_model` 写进 run 的 memory（`review_candidate` 已经在用这个位置）。
- `resumePublicationReview` 用 `reviewModelFromRun(run)` 读回，**不再盖当前配置**。
  在本次修复之前创建的 run 没有这个值，它们**如实报告没有模型**，而不是填一个当前配置 ——
  伪造溯源正是这个缺陷本身。

**线上断言（部署 `78e3fb5` 之后，同一套 A/B/C）**：

| 步骤 | `LLM_MODEL` | `agent_run_id` | `review.model` | `status` |
| --- | --- | --- | --- | --- |
| A | `deepseek-v4-flash` | `review-run-d8286cc4e461840644306e1720c1eaab` | `deepseek-v4-flash` | completed |
| B | `deepseek-v4-flash-probe` | `review-run-e6cf95927db6ba0d265cb3846d80e0fc`（**变了**） | —— | **failed** |
| C | `deepseek-v4-flash` | `review-run-d8286cc4…`（**回到 A 的值**） | `deepseek-v4-flash` | completed |

Redis 里三条 run 记录把两半都钉死了：

```
review-run-5d0091b5…（修复前创建）memory_keys=["review_candidate"]            ← 没有 review_model
review-run-d8286cc4…（修复后 A）   memory_keys=["review_candidate","review_model"]
                                   review_model="deepseek-v4-flash"
review-run-e6cf9592…（修复后 B）   review_model="deepseek-v4-flash-probe"
                                   error="agent planner returned status 503:
                                          model_not_found … 模型 deepseek-v4-flash-probe 无可用渠道"
```

B 的错误原文是**新模型真的被调用了**的直接证据 —— 它失败在一个真实存在的模型名不存在上，
而修复前同一个位置返回的是 completed。C 的 id 回到 A 的值说明身份仍是确定性的，
原裁决被正确复用且**保留它自己的模型名**。

**这条缺陷的一半在网上不可观测，值得记下来**：一旦身份包含模型，能从当前配置到达的 run
就必然带着同一个模型，所以「读回时被贴错模型名」这一半在**新** run 上无法用线上 API 观测到 ——
它由单元测试 + 反向验证 R2 覆盖（还原后报 `verdict produced by model-a was relabelled as "model-b"`）。
线上能观测的是另一半（身份分叉、新模型被真的调用），以及历史 run 如实报告「无模型」。

### 缺陷 11：被取代的预审行不受保留期约束

**起点是一个看起来只是「脏数据」的现象**：demo 租户的 `release_center_reviews` 里躺着 2 条 `failed`
预审行（`review-69e42208b72c0e0b`、`review-a26fb4d1d170c359`），`recommendation=manual_review`、
`risk_level=high`，而它们对应的文档早已有更新的、`completed` 的预审。两条行看起来只是历史残留。

**先查清它们从哪来**。两条的 id 都是 16 位十六进制，而 `AgentReview` 派生的是 32 位 ——
`nextReviewID(previousID) = sha256(previousID + "\x00rereview")[:16]`，正是「上一份裁决已过期、
开一次重审」这条路径。实测两条 id 恰好等于 `nextReviewID("demo-review-onboarding")` 与
`nextReviewID("demo-review-payroll")`。时间线也吻合：种子行 2026-09-12 写入、TTL 7 天，
2026-09-21 08:17:56 触发重审 —— 而那一刻计划器失败（这正是缺陷 5 的形状），于是写入 `failed`；
10 分钟后重审成功，请求的 `review_id` 改指向新的 `completed` 行，两条 `failed` 就此**失去引用**。

**到这里，问题从「残留数据」变成了「没人回收」**。查保留期的两端：

- `ExpireDueReviews`（`store.go:529`）从 `release_center_requests` 出发 JOIN 评审行：
  `JOIN release_center_reviews rv ON rv.tenant_id=q.tenant_id AND rv.review_id=q.review_id`。
  它只认**请求当前指向的那一行**。被取代的行从此不可达。
- `PurgeExpiredReviews`（`store.go:590`）只删 `status='expired'`。

两者相乘就是结论：**一份裁决如果在 TTL 到期之前被取代，它就永远走不到 `expired`，
于是永远删不掉**。`RELEASE_REVIEW_RETENTION`（默认 90 天）—— 一个写进配置、写进迁移注释
（`0027` 的 "Indexes for review-report expiry, automatic rereview pickup, and retention cleanup"）
的存储上界 —— 对这类行**完全失效**。

**线上复现**（全表 27 行）：

| 状态 | 无人引用 | 行数 |
| --- | --- | --- |
| completed | 否 | 12 |
| completed | **是** | **2** |
| expired | 否 | 4 |
| expired | 是 | 7 |
| failed | **是** | **2** |

那 7 行 `expired` 无人引用的行会在 90 天后被正常清掉（它们已经走到了 `expired`）；
另外 **4 行（2 `failed` + 2 `completed`）永远进不了那条路径**。两条 `failed` 行的
`expire_due_reviews` 可达性实测为 **false**。

**修复的形状**：让清理用「保留期本来想表达的那个不变量」做判据，而不是用一个中间状态做判据。

- `PurgeExpiredReviews` 去掉 `status='expired'` 门禁，改成
  **`expires_at <= now()-retention` 且没有任何 request 引用它**。
  对被引用的行，新旧谓词完全等价 —— `NOT EXISTS(referenced)` 那道守卫本来就已经拦住它们了，
  所以这不是放宽，只是把「谁算过期」这件事交给真正决定它的事实。
- `0030` 迁移把 `release_center_reviews_purge_idx` 从 `WHERE status='expired'`
  改成 `WHERE expires_at IS NOT NULL`，否则新谓词没有可用索引。

**线上 A/B（部署前 / 部署后，同一批探针行）**：向线上库插入 4 行 `zz-probe-*`
（`expires_at = now()-100d`，其中 `zz-probe-expired` 已是 `expired` 态），等采集器跑（每 5 秒一轮）：

| 探针行 | 部署前（`83194a7`） | 部署后（`5afc6b1`） |
| --- | --- | --- |
| `zz-probe-expired` | 被回收 | 被回收 |
| `zz-probe-superseded-failed` | **存活** | **被回收** |
| `zz-probe-superseded-completed` | **存活** | **被回收** |
| `zz-probe-fresh`（`expires_at = now()-1h`，在保留期内） | 存活 | 存活 |

同一份谓词在同一张线上表上的对照（事务回滚，不改数据）：旧谓词命中 **1**，新谓词命中 **3**，
差值正是那两行被取代的行。

**那两条 `failed` 行今天不会被删，这是对的**：它们的 `expires_at = 2026-09-28`，窗口还没到。
修复改变的不是「今天删不删」，而是「窗口过后删不删」。线上实测（回滚事务，把判据日期取到 2027-01-01）：
部署后的谓词会回收 demo 租户 6 行 —— 两条 `failed`、两条被取代的 `completed`、两条种子 `expired`；
旧谓词只回收 2 行（那两条种子）。**「永不回收」变成了「窗口过后回收」。**

**反向验证**：把 `status='expired'` 门禁放回去 ——
`TestPurgeExpiredReviewsReapsSupersededReviews`（真 Postgres）报 `deleted=1, want 3`，
`TestPurgeExpiredReviewsDeletesUnreferenced`（pgxmock）报
`could not match actual sql … with expected regexp "WHERE rv\.expires_at IS NOT NULL"`；
还原后文件哈希与快照逐字节一致。

### 缺陷 12：备份脚本把完整性判定吞掉了

这条来自**核对 §4.7 的验收判据**。§4.7 的判据是「116 稳定、`REQUIRE_INTEGRITY=1` 退出码为 3」，
前半条实测成立（2026-09-22 的 cron 备份 `integrity.missing_objects = 116`，
`--require-integrity` 直接调 `backup-manifest.py` 确实返回 3）。**但判据说的是脚本**，
而脚本从不被单独验过 —— 于是问题出在包装层：

```
$ REQUIRE_INTEGRITY=1 ./scripts/backup-stack.sh ; echo "exit=$?"
...
ERROR integrity is degraded; see manifest.json integrity block
backup finished: integrity=degraded dir=.../20260922T033113Z keep=7
exit=0                     ← 文档写的是 3
```

**真因两处，都在 `scripts/backup-stack.sh`**：

1. 清单的退出码被 `manifest_code=$?` 接住后**只喂给一行日志**（原
   `if [[ ${manifest_code} -eq 3 ]]; then log "ERROR ..."`）。
2. 最终退出阶梯只由 `FAILURES` 决定（`if [[ ${FAILURES} -gt 0 ]]; then exit 2; fi; exit 0`），
   而那条路径从不递增 `FAILURES`。

全脚本**没有任何一处 `exit 3`** —— 头注释第 28 行承诺的那个码在代码里不可达。

**为什么这不只是一行文档错误**：退出码是脚本**已有的那套升级机制的唯一输入**。
`record_state` 按退出码累计 `consecutive_failures`，`>= 2` 时写 `ALERT.txt`。判定既然到不了
退出码，那么**一个已经损坏的备份会持续报告成功**：`state.json` 里连续 6 次运行都是
`last_integrity=degraded` 配 `consecutive_failures=0`，`ALERT.txt` 从未出现。
这与前面 11 个缺陷是同一个形状 —— **信号存在，但不可能响**。

**修复**：把判定带到退出阶梯。`manifest_code == 3` 时置 `INTEGRITY_DEGRADED=1`，
阶梯在 `FAILURES > 0 → exit 2` 之后返回 3（缺件比「忠实备份了一个已损坏的源」更紧急）。

**升级链路线上实测**（`BACKUP_ROOT` 指向临时目录，部署环境的备份根未被触碰）：

```
run 1 exit=3
run 2 exit=3
state.json: {consecutive_failures: 2, last_exit_code: 3, last_integrity: degraded}
ALERT.txt : backup has failed 2 times in a row; last run 2026-09-22T03:33:04Z
```

**修复后线上断言**：`REQUIRE_INTEGRITY=1` → **exit 3**（修复前 0）；不设该变量 → exit 0；
契约测试 18 项全绿（含 1 项 opt-in 的线上行为测试）。

**反向验证**：把 `backup-stack.sh` 还原到 HEAD 版本（用 `git show HEAD:…` 取原文件，
不用 `git checkout --`，因为修复尚未提交），两项守卫测试都失败，还原后文件哈希与快照逐字节一致。

**刻意没有改的事**：**没有让 cron 打开 `REQUIRE_INTEGRITY`**。§4.7 的 116 份缺失是**已接受的残留**，
把它设成致命会让 `ALERT.txt` 永久点亮、把信号变成噪声。这次改变的是「这个开关对想要严格策略的
运维者真的按它说的生效」，不是改变日常策略。

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

### 4.2 备份恢复 —— 已完成，见 §1.2 与 `docs/backup-and-recovery.md`

### 4.3 上传者删除能力后端有、前端未开放

**证据**：`CONTINUATION.md §4` 记录「后端授权逻辑已有，但 UI 是否开放仍需产品决定」。

**影响**：低。但「后端有、前端不给」是一种**授权语义不完整**的状态 —— 接口能力已经存在，只是没人从 UI 能走到。要么开放，要么明确记录为「仅 API 可用」并加测试锁住，不要悬着。

### 4.4 合规审查深度 —— 项目自己已经反复划定边界

**证据**：`LEARNINGS.codex.md` 与 `backlog.md` 多处强调「不得把当前已实现的受限规则扫描描述为完整合规审查」。暂缓项：版本对比、政策库、跨文档冲突审查。

**我的判断**：这一项**不该现在做**。它是「大」项，且当前预审（R1/R6）已能覆盖发布资格预审的真实需求。先把它在文档里的表述**收紧到与实现一致**（现在是准确的，保持住即可），等有真实业务诉求再立项。

### 4.5 受外部决策阻塞的项（不建议现在动）

`backlog.md` Active work 里 5 项全部 blocked / pending decision，依赖项都在项目外部：P1.9（业务责任人签字）、P2.3（保留期决策）、P2.5-PROD/STAGE（IdP 选型与 staging）、P2.6（SLO/RPO/预算批准）。

**这些不是技术债，是决策债**。在决策到位前动手只会白做。

### 4.6 ES 词法检索的标题加权是死代码（已修复，见 §1.3 缺陷 9）

> 本节保留修复前的证据与机制分析；修复形状、线上断言与端到端反向验证在 §1.3 缺陷 9。

**证据（修复前实测）**

```
$ curl :9200/documents_text_v2/_mapping
  properties → chunk_id chunk_index content content_hash created_at doc_id
               document_version_id file_hash generation_id metadata permission tenant_id
                                     ↑ 没有 file_name

$ internal/es/indexer.go:522  esChunkDoc{ChunkID,DocID,TenantID,Content,Permission,
                                ChunkIndex,FileHash,DocumentVersionID,GenerationID,
                                ContentHash,Metadata,CreatedAt}
                                     ↑ 也没有 file_name

$ internal/retrieval/elastic.go:183-198  titleAwareShouldClauses() 发出两条
  {"match_phrase":{"file_name":{...,"boost":6.0}}} 与 {"match":{"file_name":{...,"boost":3.0}}}
                                     ↑ 查询一个永远不会被索引的字段

$ internal/retrieval/elastic_test.go:109  断言这两条子句「存在」
                                     ↑ 只断言了子句形状，没有任何测试断言该字段被写入索引
```

**机制**：`ElasticRetriever` 的词法分支里权重最高的两个信号（boost 6.0 / 3.0）都打在
`file_name` 上，而 `esChunkDoc` 从不写入该字段、ES mapping 里也没有它。ES 对未映射字段的
`match`/`match_phrase` 不报错、只是永不匹配，所以这条链路**静默降级**为「只有正文匹配」。

**影响**：用户按文档名提问（「员工手册里怎么说」）时，词法分支拿不到任何标题信号。
不影响功能正确性（`minimum_should_match: 1` 仍由 `content` 子句满足），
所以不会报错、不会失败，只会让召回质量比设计意图低一档。

**修复**：三件事都做了，但第三件换了做法 —— 没有重建索引，而是回填。

① `model.Task` / `model.Chunk` 加 `FileName`，在两个 task 构造点（`cmd/api/upload_handlers.go`、
`internal/gateway/gateway.go`）填入 `header.Filename`，并在**两条独立的 chunk 产出路径**
（流式 parser 路径与 `processPDFBatches`）上打标；
② `esChunkDoc` 加字段、`mapChunkToESDoc` 填值、建索引 mapping 加 `file_name`；
③ 存量 4684 条**不回传、不重建** —— `documents` 表里已有权威文件名，
`scripts/backfill-es-file-name.sh` 逐文档 `_update_by_query` 幂等写入即可。
`scripts/migrate-es-cjk-index.sh` 的 mapping 也同步加上该字段，供未来重建时使用。

**实测结果**

| 判据 | 结果 |
| --- | --- |
| `GET documents_text_v2/_mapping` 含 `file_name` | 含。**线上索引实际是 `{"type":"text","analyzer":"cjk"}`，没有 `search_analyzer`** —— 对已是 text 的字段追加 `search_analyzer` 会被 ES 拒绝（`analyzer on a text field cannot be changed`），所以回填脚本只发前两项。缺 `search_analyzer` 时查询侧与索引侧都用 `analyzer` 指定的 `cjk`，两边一致，功能上无缺口；`search_analyzer` 只出现在代码的建索引 mapping 与 `scripts/migrate-es-cjk-index.sh` 里，供未来重建时使用 |
| `match_phrase(file_name,"员工手册")` 命中数 | **0 → 5** |
| 回填 | `chunks_with_file_name = 0/4684 → 4684/4684`（119 份文档） |
| 端到端反向验证（移除 `file_name` → 回填） | 线上 API 的 `backend_candidate_counts.elasticsearch` **3 → 0 → 3** |
| 「索引里有 file_name」的断言 | 新增 `TestMapChunkToESDoc_CarriesTheUploadFileName` + 建索引测试的 mapping 断言 |

**这条判据本身是错的，已纠正**：初稿写的是「用文档标题作为查询时
`backend_candidate_counts.elasticsearch > 0`」。实测发现该指标**不能**证明本缺陷已修 ——
`query/title.go` 的 `titleMatchedDocIDs` 会在问句包含文件名时追加一条 `terms: {doc_id: [...]}` 子句，
命中完全绕开 `file_name` 字段。用「员工手册讲了哪些内容」做验证时，即使把索引里全部 9 条
`file_name` 都删掉，`elasticsearch` 仍是 3。有效的问句必须**同时**满足「不含连续文件名（不触发 title 匹配）」
与「正文子句命中 0」，「手册里有哪些规定」满足这两条。

> 索引名说明：应用用的是别名 `documents_text`，它指向物理索引 `documents_text_v2`
> （`internal/es/indexer.go` 的 `physicalIndex := i.index + "_" + cjkIndexVersion`，
> 建完再 `_aliases` 挂上）。两处写法指同一个索引，本节用物理名是为了 `_mapping` 可直查。

---

### 4.7 116 / 119 份文档的源对象已经不存在（已确认，不可恢复）

**证据**（2026-09-21 实测）

```
$ python3 scripts/object-store-inventory.py --endpoint http://127.0.0.1:9000 --bucket documents
  {"total": 3, "bytes": 1309, "objects": {"demo/demo-doc-handbook.md": 513,
                                          "demo/demo-doc-onboarding.md": 401,
                                          "demo/demo-doc-payroll.md": 395}}

$ psql -tAc "SELECT count(*) FILTER (WHERE object_key <> ''),
                    count(*) FILTER (WHERE object_key =  ''),
                    count(*) FROM documents"                        → 119|0|119
```

**怎么确认不是「上传链路坏了」**：走真实接口 `POST /v1/upload` 传一份探针文档 → `202` →
对象 `demo/scratch-object-probe/versions/07046a88-….txt`（111 B）**立刻出现**在对象存储里，
且 `file_size` 与观测字节数相等。链路是好的；丢的是**历史对象**。
（探针已按正常删除流程清掉：`DELETE /v1/documents/scratch-object-probe` → 202，
一轮收集器后目录回到 119 行、对象回到 3 个。）

**为什么长期没人发现**：检索读的是 Qdrant/ES 投影，不读源对象。于是下面五件事同时成立，
没有一件会报警 ——

| 表面证据 | 实际情况 |
| --- | --- |
| `documents.status = 'completed'` | 与源对象是否存在无关，只是入库流程走完了 |
| `GET /v1/documents/{id}` 返回具体 `file_size` | 那是**目录里记的数字**，不是对象存储里的 |
| 检索与问答正常作答 | 投影还在；投影不需要源对象 |
| `index_manifests` 全部 `healthy` | 对账比的是**投影之间**是否一致 |
| 完整性门禁第一次运行就报 119 | 这是**唯一**会说话的地方，而它此前不存在 |

**影响面**：这 116 份的**正文仍在投影里**，所以检索与问答不受影响。
受影响的是三件事：**重放修复、重嵌入、下载原文**。
其中最要紧的是第一件 —— 对账器只能靠重放来修复差异，而源对象不存在时
`FinishReconciliation` 找不到重放目标（缺陷 6 的报错就是这么来的）。

**这条缺失与缺陷 8 的关系（2026-09-21 补测）**：缺陷 8 修复后，`state='failed'` 的生成会被
有界重放三次；而 §4.7 的缺失让这些重放**注定失败**。线上实测：7 份 failed 生成累计被重放 18 次，
每一次都在 `materialize object` 阶段因 `The specified key does not exist` 落入 DLQ，走满预算后
`IndexGenerationRepairExhausted` 转 `active`。也就是说，**这条缺失的终态现在是显式的** ——
它不再表现为「一批文档悄悄停在 failed」，而是「修复预算耗尽 + 一条会响的告警」。
当前 `documents.status='failed'` 的 7 行是这 116 份里的一个子集；其余缺失文档的正文已在投影里、
`status='completed'`，检索照常，只有重放会失败。**要恢复必须先有源对象，所以这 7 行不可自动修复。**

**已做与不可做**

- 演示种子的 3 份**可以确定性重建**（内容由代码生成），已作为缺陷 7 修复并线上断言：
  `file_size` 与对象字节数 513 / 401 / 395 逐一相等，缺失计数从 119 降到 **116**。
- 其余 **116 份不可恢复**：字节已经没了，没有副本、没有上游，也不在任何备份里（备份晚于丢失）。
  脚本**不做假造** —— 造一份「看起来有」的对象比缺失更危险，那会让完整性门禁变成谎言。
- 这份缺失现在由门禁**持续报告**（`integrity.status = degraded`），不再隐形。

**验收判据**：`scripts/backup-stack.sh` 产出的 `manifest.json` 里
`integrity.missing_objects` 稳定为 116（不是 0，也不是 119）；
`REQUIRE_INTEGRITY=1` 时退出码为 3；
`documents` 表里**凡是对象存在的行**，`file_size` 与实际对象字节数一致（当前 3/3）。

**判据复核（2026-09-22，在部署环境上实跑）**：① 2026-09-22 03:17 的 cron 备份
`integrity = {status: degraded, missing_objects: 116, orphan_objects: 0}` —— 不是 0，也不是 119；
② 用该备份的产物单独调 `backup-manifest.py`：不加 `--require-integrity` 返回 0，
加 `--require-integrity` 返回 **3**（真实 `manifest.json` 未被改动，mtime 不变）；
③ 3/3 —— `demo-doc-payroll` 395、`demo-doc-handbook` 513、`demo-doc-onboarding` 401，
目录里的 `file_size` 与对象字节数逐一相等。三条判据全部成立。

> **但这次复核暴露了另一个缺陷**：判据说的是「`REQUIRE_INTEGRITY=1` 退出码为 3」，
> 而**脚本**从不返回 3 —— 判据被验在 `backup-manifest.py` 上，而 cron 跑的是包装脚本。
> 见 §1.3 缺陷 12。

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
| `scripts/tests` 契约测试 | 256 tests OK（17 skipped，含本轮新增的 16 个备份/恢复契约测试） |
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
第 1 步（已完成） 1.1.1 ES 单节点副本 + 1.1.2 磁盘回收 + §1.3 缺陷 1–5
第 2 步（已完成） 1.2 备份脚本 + 恢复演练（隔离栈 8 项断言全 PASS，RTO 79.7 秒实测）
第 3 步（已完成） 1.1.2 剩余：ES 水位改百分比 + 宿主机磁盘告警（含补 node-exporter）
第 4 步（已完成） 1.1.2 终态：磁盘 9.2GB → 63GB（清 build cache 58.18GB，未碰其他项目）
第 5 步（已完成） §1.3 缺陷 6 / 7：期望摘要是占位串（对账周期性永久报错）、种子声明了不存在的源对象
第 6 步（已完成） §1.3 缺陷 8：failed 生成的自愈路径与耗尽信号（含两项自查出的二次缺陷）
第 7 步（已完成） §1.3 缺陷 9 / §4.6：ES 词法检索的 file_name 加权（回填 4684 条，未重建索引）
第 8 步（已完成） §1.3 缺陷 10：预审裁决的缓存身份与模型溯源
第 9 步（已完成） §1.3 缺陷 11：被取代的预审行不受保留期约束
第 10 步（已完成） §1.3 缺陷 12：备份脚本把完整性判定吞掉（`REQUIRE_INTEGRITY` 不生效）
第 11 步          2.1 Go 工具链权限修复
第 12 步          3.  状态文档三源归一 + 一致性契约测试
第 13 步          2.2 / 2.3 配置一致性修复 + 契约测试
第 14 步          4.1 邀请式自助开户（需你先确认产品口径）
第 15 步          5.1 query/service.go 机械拆分
```

**为什么是这个顺序**：第 1–4 步是「不做会丢数据或停服」，全部完成 —— 磁盘那一项从
「没人看得见的下滑」变成了「两条会响也会消的告警 + 63GB 余量」。第 5–10 步是
「不做则故障永远静默」：这几处缺陷的共同点是自己不报错、还让看板变绿（见 §1.3）。
第 11 步是「不做则每次改动都在踩坑」；第 12 步是「不做则后面所有状态判断都不可信」；
第 13 步成本最低收益明确；第 14 步需要你的产品决策；第 15 步是纯收益优化，随时可做。

---

## 8. 边界与未做的事（避免误解）

- **代码改动限于两处**：§1.3 列出的 12 个缺陷，以及 §1.1.2 的 ES 水位百分比化 + 宿主机磁盘告警
  （含补上的 `node-exporter`）。其余章节仍是调查结论，未据此改代码。
- **§1.2 不只是文档**：`scripts/backup-stack.sh`、`scripts/restore-stack.sh`、3 个单职责助手脚本、
  `docker-compose.restore.yml`、16 个契约测试都已提交，并在隔离项目 `ai-etl-restore` 上真跑过
  （`down -v` 只作用于该隔离项目，碰不到演示栈的卷）。
- **新增了一个服务**：`node-exporter`。它不是可选的装饰 —— 没有它就没有任何磁盘信号，
  告警规则写出来也是死的。端口 9100 只在 `127.0.0.1`，隔离栈通过 eval 覆盖把它收起来。
- **没有删除任何东西**。远端 734MB 的 `.gomod`/`.gocache` 是承重结构，本次确认了它不能被删。
- **没有清理你其他项目的镜像/卷**。清理后 `Images` 仍有 24.59GB 可回收、`Local Volumes`
  19.98GB 可回收，大部分不属于本项目 —— 这类操作我不会未经确认执行。
  本轮的回收只做了 `docker builder prune -af`（58.18GB 构建缓存，只影响下次构建速度，
  不动镜像、不动容器、不动卷），其他项目的 11 个容器在清理后逐一确认仍在运行。
- **没有删除任何本项目的数据卷**。`ai-etl-platform_*` 全部保留；无引用的
  `ai-etl-go-build-cache` / `ai-etl-go-mod-cache` 与旧命名空间的 `ai-etl-pipeline_*` 也留着 ——
  空间已不紧张，而 go 缓存卷是构建提速的承重结构。
- **UAT-017～020 没有标记为「已关闭」**，因为只做了产物层验证，未做章程要求的真实页面复验。
- **§4.5 没有动手**，理由写在该小节里（被外部决策阻塞）。**§4.6 已动手并完成**（见 §1.3 缺陷 9），
  但走的是**回填**而不是原计划的重建索引 —— 4684 条存量 chunk 的 `file_name` 由
  `scripts/backfill-es-file-name.sh` 就地补齐，没有 drop/rebuild 索引，也没有重新上传任何文档。
- **§4.7 的 116 份源对象没有试图补救**。字节已不存在，脚本不做假造；它被降级为「由完整性门禁
  持续报告的已知缺失」，而不是「已修复」。
- **缺陷 9 的修复不改变「已发布 / 草稿」的可见性口径**。`file_name` 回到索引后，
  `demo-doc-onboarding` 与 `demo-doc-payroll` 的词法候选会重新出现，但它们
  `publication_status='draft'`，仍被 `publicationrelease.ResolveVisibility` 正确挡在证据之外
  （线上实测：这两个问句的 `backend_candidate_counts.elasticsearch` 为 0，是正确行为，不是缺陷）。
- **缺陷 10 的修复让修复之前创建的预审 run 变成不可达**，这是刻意的。它们的身份里没有模型，
  因此现在算不出它们的 id；而它们也从未记录过自己的模型。线上那批遗留 run（如
  `review-run-5d0091b5…`，`memory_keys=["review_candidate"]`）会自然过期（`AGENT_RUN_TTL=24h`），
  同一候选的下一次预审会用新身份重新跑一遍。**没有做数据迁移**：把旧 run 迁移到新身份需要
  假设它们的模型，而那正是本次要消灭的假设。
- **缺陷 10 只有一半能在线上观测，另一半靠测试**。身份包含模型之后，从当前配置能到达的 run
  必然带着同一个模型，所以「读回时被贴错模型名」在**新** run 上无法用线上 API 观测；
  它由单元测试与反向验证 R2 覆盖（还原后报 `verdict produced by model-a was relabelled as "model-b"`）。
  线上能观测到的是身份分叉（换模型 → 换 run）、新模型确实被调用（错误原文 `model_not_found`），
  以及遗留 run 如实报告「无模型」。
- **缺陷 8 的修复不承诺「自愈成功」**。它承诺的是：`state='failed'` 的生成会被有界重放
  （`INDEX_RECONCILE_MAX_REPAIRS`，默认 3）、重放结束后停止、耗尽状态进入
  `ai_etl_generation_diagnostics{condition="repair_exhausted"}` 并触发
  `IndexGenerationRepairExhausted`。线上那 7 份走满预算后仍然 `failed`，根因是 §4.7 的源对象
  缺失 —— 那不是这条路径能修的，它只负责让不可恢复的失败变得可见、且不再空转。
- **缺陷 11 不动「已发布的文档保留自己的审批证据」这件事**。清理只针对**无人引用**的行；
  仍有请求指向的那一行永远保留 —— 那正是 `NOT EXISTS(引用)` 守卫的作用，也是治理场景下
  审批证据该有的行为。本次改掉的只是「被取代的行也永远留着」这一半，没有放宽任何被引用的行。
- **缺陷 11 的两条线上残留行今天不会被删**，这是判据本身决定的，不是修复没生效：
  它们的 `expires_at = 2026-09-28`，窗口还没到。修复改变的是「窗口过后删不删」。
  线上以 2027-01-01 作判据日期、在回滚事务里验过：这两行会被回收，而修复前无论等多久都不会。
- **本轮为验证保留期插入的 4 行 `zz-probe-*` 探针行已全部删除**（部署前存活的那 2 行由
  部署后的采集器回收，`zz-probe-fresh` 由手工删除）。表回到 27 行，`状态 × 是否被引用`
  矩阵与探针插入前逐项一致。
- **缺陷 12 没有改变日常备份策略**。`REQUIRE_INTEGRITY` 仍然**没有**写进 cron —— §4.7 的 116 份
  缺失是已接受的残留，把它设成致命会让 `ALERT.txt` 永久点亮、把信号变成噪声。改的只是
  「这个开关对想要严格策略的运维者真的按它说的生效」。
- **缺陷 12 的验证全部走临时 `BACKUP_ROOT`**，部署环境的 `~/backups/ai-etl-platform/` 未被污染：
  `state.json` 仍是 `consecutive_failures=0`，且没有 `ALERT.txt`。为复现而多跑的那一次备份
  落在同一个保留窗口内（7 份上限，未触发轮转）。
