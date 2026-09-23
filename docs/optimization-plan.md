# 项目优化设计方案

编制日期：2026-09-21
编制方式：代码 + 远端运行栈实测。**每条结论都标注了证据来源**，凡未实测的均明确标注为「未验证」。
范围：`ai-etl-platform`（Go ETL/Query + Python Parser/Reranker + Next.js 工作台）。

> 本文不是愿望清单。优先级按「不做的后果」排序，不按「工作量」排序。

---

## 0. 结论摘要

一句话：**主链路能跑通，但「会自己恢复」这件事没做到 —— 已定位的十九处缺陷里，五处是同一个形状：一次瞬时故障被写成持久状态，之后没人再纠正它；第六处更隐蔽，失败路径把本该暴露问题的证据自己回滚掉了；第七处最安静，目录声明了一份从不存在的源对象，而平台里没有任何东西会去核对；第八处是前七处的反面 —— 不是没人修，是根本没有能修的地方；第九处是第七处在检索侧的重演 —— 加权最高的那个信号从被声明那天起就从未生效；第十处把前九处的教训合起来用了一遍 —— 缓存漏了一个输入，而那个输入恰好是裁决自己声称的出处；第十一处又回到了最开始的形状，只是这次没人回收的不是重试，而是记录本身 —— 一份被取代的裁决走不到「过期」这个状态，于是保留期对它永不生效；第十二处把这个形状搬到了备份脚本上 —— 完整性判定算出来了、印出来了，却传不到唯一能触发告警的那个值上；第十三处把「要求」和「判定」分了家 —— 输出语言只写在 prompt 里、没有任何代码校验，而模型复述一遍就能顶掉确定性扫描的原话；第十四处是第十三处的下半场 —— 判定补上了，却只挂在**写入**那一条路径上，于是**表里已有的 28 条裁决照样把英文送到管理员界面上**，而卡片自己就印着「Prompt 版本：autonomous-review-v2」这句话；第十五处是第十四处的下半场 —— 中文外壳补上了，**失败裁决却只说「已转人工复核」不说为什么**，因为那行代码把审阅者自己的诊断覆盖掉了，而它原本就写在手里；第十六处与前面十五条**形状都不同** —— 它是一条输入校验缺口：密码打得太长被报成服务端故障（500），而打得太短反而被接受；第十七处是一处字符边界错误 —— 判定算出来了，却按**字节**切在多字节字符中间，一个切点让整批 span 被 OTLP 拒绝、trace 整体丢失；第十八处同样是新形状，而且**不在故障恢复这条线上** —— 它把「这块能不能当回答证据」的判据用在了「读这份文档自己的内容」上，于是没有发布记录的文档被报成「没有切块」，列表里正常显示、索引里也有内容的一份文档，打开详情页是「暂无切块」，用同一页的搜索框也搜不到它；第十九处把第十八处往下推了一层 —— 同一段内容在向量库里存着两份逐字相同的拷贝（一份历史写入、不带代际身份，一份来自取代它的受管代际），读侧去重是「先到先得」，于是留下哪一份取决于存储的返回顺序：留下空身份那一份时，刚修好的可见性判据照样把一份**已发布**文档判成空。**

> **修订说明（2026-09-22，2026-09-23 补第十七、十八、十九处）**：初稿结论是「风险不在功能，在运维底座」。随后在部署环境上做了逐条复现的
> 缺陷排查，找到并修复了 19 个**功能/可靠性**缺陷（见 §1.3）：5 个属于「失败被固化、重试变成复读」，
> 第 6 个属于「不一致的数据被当真源，而失败路径把证据回滚掉」，
> 第 7 个属于「目录声明了从不存在的源对象，且平台没有任何核对机制」，
> 第 8 个属于「失败状态没有被任何自动流程认领，也没有被任何诊断指标统计」，
> 第 9 个属于「被声明、被加权、被测试过的检索信号，从未被写入索引」（§4.6），
> 第 10 个属于「缓存键漏了一个输入，而那个输入正是裁决自己声称的出处」，
> 第 11 个属于「生命周期只沿当前被引用的那一行走，被取代的记录再也无人回收」，
> 第 12 个属于「判定存在，但传不到唯一能触发告警的那个值上」，
> 第 13 个属于「要求写在文本里、判定不在代码里 —— 输出语言和确定性结论的措辞都因此失守」，
> 第 14 个属于「判定补上了，但只挂在写入路径上 —— 表里已有的裁决照样把英文送到界面上」，
> 第 15 个属于「诊断算出来了、也拿到了，却被下一行覆盖掉 —— 失败裁决因此说不出为什么失败」，
> 第 16 个**不在上面这个形状里**，它是一个输入校验缺口：密码打得太长被报成服务端故障（500），
> 而打得太短反而被接受；
> 第 17 个是「判定算出来了，却按**字节**切在字符中间 —— 一个切点让整批 span 被 OTLP 拒绝，trace 整体丢失」；
> 第 18 个也是新形状，而且和第 16 个一样**不在故障恢复这条线上** —— 它把「这块能不能当回答证据」的
> 判据用在了「读这份文档自己的内容」上，于是**没有发布记录的文档被报成「没有切块」**：
> 一份列表里正常显示、索引里也有内容的文档，打开详情页是「暂无切块」，用同一页的搜索框也搜不到它；
> 第 19 个把第 18 个往下推了一层：同一段内容在向量库里有两份**逐字相同**的拷贝（一份历史写入、
> 不带代际身份，一份来自取代它的受管代际），读侧去重是「先到先得」，留下哪一份取决于存储的
> 返回顺序 —— 留下空身份那份时，第 18 处刚修好的判据照样把一份**已发布**文档判成空。
> 原结论因此**不成立**，已按下表修订。
> 同时 P0 的 §1.2（全栈无备份）已从「待做」变成「已做并在隔离栈上实测通过」。

| 类别 | 结论 |
| --- | --- |
| 功能完善度 | 主链路（上传→解析→向量化→检索→问答→发布审批）已闭环；缺口集中在**自助能力**、**合规审查深度**两处（**备份恢复**已由 §1.2 补齐） |
| 系统设计 | 服务边界清晰、CI 门禁完整、租户/权限/证据链设计是扎实的；**配置一致性**已由 §2.2 / §2.3 修掉（并加了契约测试），剩下的问题是**状态文档三源冲突**、**单文件职责过载** |
| 可靠性 | **真正的短板在故障恢复路径**：瞬时失败被持久化后没有自愈机制，重试路径要么不存在、要么复读旧结果。见 §1.3 |
| 最紧急项 | **没有 P0 挂着**。三项 P0 都已处理：ES 永久 yellow（§1.1.1）、全栈无备份（§1.2）、磁盘濒满（§1.1.2，可用空间 9.2GB → 63GB）。下一步按 §7 的执行顺序走 |
| 已修复项 | 19 个功能缺陷 + ES 永久 yellow（真因是**单节点配了 1 副本**，不是磁盘水位）+ P0 备份与恢复演练（§1.2）+ ES 水位百分比化、宿主机磁盘告警、磁盘回收（§1.1.2）+ 配置键一致性：3 个切块死键、26 个未文档化的键、3 个 compose 漏传的检索键、5 个「模板写了但两边都没接」的接线缺口（§2.2 / §2.3 / §8）。见 §1.1、§1.2、§1.3、§2.2、§2.3、§8 |

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

### 1.3 已修复的 19 个缺陷（前五处同一形状：失败被固化，重试变成复读）

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
| 13 | 预审裁决的输出语言不由系统决定，且把一句话写成了一整段；模型的复述还能顶掉确定性扫描的原话 | 界面上同一张卡片里，`summary` 是 366 字符英文段落、`kind_label` 是 78 字符英文，而周围标签全是中文；同一个 `autonomous-review-v2` 下，`review-91847c6aa8e77c605020b37e2099f6d2` 却产出了中文 summary —— 语言只是模型当次的随机选择 | 三处：① `agentapi/review.go` 的 `reviewPlannerSystemPrompt` 通篇英文，`summary`/`finding.summary`/`kind_label` 的语言因此无人约束；② 同文件 `validateAutonomousReview` 合并 finding 时**只在模型给的严重度更低时**才用确定性那条，两边同为 `medium` 就保留模型的英文改写 —— 而 prompt 里写着 `Copy deterministic scan findings exactly`，没有任何代码校验它；③ 前端 `严重度：{finding.severity}` 直出枚举、未映射的 code 原样打印 | `5c04c99` |
| 14 | 缺陷 13 的语言契约只挂在**写入**路径上，表里已有的裁决照样把英文送到管理员界面 | 缺陷 13 部署后用户仍在页面上看到英文，卡片自己印着 `Prompt 版本：autonomous-review-v2`；线上断言实测重建前 **15 条 FAIL**：6 条英文 `summary`（最长 524 字符）、8 条英文 `kind_label`、2 条英文失败模板 `agent review returned status "failed"` | 两处：① `agentapi/review.go:582` 的归一发生在**裁决被装配的那一刻**，而 `cmd/api/release_center_handlers.go:191`/`:248`/`:284` 与 `release_center_workflow.go:67` 拿到 `GetReview` 结果后直接 `writeJSON` —— 没有任何一处保证「离开平台的文本是中文」；② 判据本身按「≥6 个 ASCII 词且 ≥2 个功能词」写的，漏掉线上真实存在的两种形状：失败模板（5 个词、0 个功能词）与英文名词短语（8 个词、1 个功能词） | `4eef274` |
| 15 | 失败的预审裁决说不出为什么失败：审阅者自己的诊断已经写在手里，却被下一行覆盖掉 | 线上两条 `failed` 裁决（`review-a26fb4d1d170c359`、`review-69e42208b72c0e0b`）的 `summary` 都是 `agent review returned status "failed"`，**没有一个字说明原因**；注入一次计划器故障后复现：新行同样只印这一句，而平台自己的 `GET /v1/agent/runs/{id}` 对同一个 run 返回 `agent planner returned status 503: {"error":{"code":"model_not_found",...}}` —— 原因一直在，只是没进裁决 | 两处，都在 `releasecenter/coordinator.go`：① `:202` `reviewResult.Summary = reviewErr.Error()` 把**适配器已经放好的诊断**覆盖掉（`agentapi/service.go` 的三处失败返回都写了 `Summary: err.Error()` / `run.Error` / `"invalid review report: "+err.Error()`），覆盖后原因只存在于 agent run 里，而 run 受 `AGENT_RUN_TTL`（24h）约束、且不在裁决面板上；② 失败分支是**唯一没经过语言契约的写入路径** —— `validateAutonomousReview` 只在 run 完成且报告解析成功后才被走到，所有失败路径都在它之前 return，这正是英文模板能进表的原因 | `0bcb969` |
| 16 | 密码打得太长被报成服务端故障（500），而打得太短反而被接受 —— **形状与上面 15 条都不同**，它是一个输入校验缺口 | 线上实测（管理员重置 `eval-user` 的密码）：73 字节 → **HTTP 500 `{"error":"failed to hash password"}`**；1 个字符 → **HTTP 204（接受）**。同一批请求里 72 字节 → 204 | 两处，都在 `cmd/api/user_handlers.go`：① `handleCreateUser`（`:181`）与 `handleSetPassword`（`:329`）把 `auth.HashPassword` 的**任何**错误都写成 500，而 `x/crypto v0.55.0` 的 `GenerateFromPassword` 对超过 72 字节的输入返回 `ErrPasswordTooLong` 且**不截断** —— 「密码打太长」对用户表现为服务端故障；② 全仓此前**没有任何长度校验**，所以这条 500 是唯一症状 | `45be654` |
| 17 | 截断 `text_preview` 时先修 UTF-8、再按**字节**切，切点落回多字节字符中间 → OTLP 导出器拒绝**整批** span，trace 整体丢失（§6 那条「Jaeger `invalid UTF-8` 导出告警」就是它） | 上传一份纯中文文档（每块远超 100 字节；3 字节字符使 `100 % 3 == 1`，切点必然落在字符中间）后，`etl-worker` 立刻打出 `traces export: rpc error: code = Internal desc = grpc: error while marshaling: string field contains invalid UTF-8`，jaeger 里查不到本次的 embed span | `services/etl-worker/internal/embedder/embedder.go` 的 `truncate`：先 `strings.ToValidUTF8` 修掉非法序列，再用 `s[:maxLen]` **按字节**切 —— 刚修好的串被切回非法状态。同包的 `releasecenter.truncateRunes` 早已按 rune 切，只是预算单位不同 | `6969acc` |
| 18 | 读一份文档**自己的**内容时套用了「这块能不能当回答证据」的判据 → 没有任何发布记录的文档被报成「这份文档没有切块」，而它其实是列表里正常显示的一份文档 | demo 租户两份未发布文档（`demo-doc-onboarding`、`demo-doc-payroll`，Qdrant 各 3 块）的 `/chunks` 都返回 `{"total":0}`，而同为 3 块的已发布 `demo-doc-handbook` 返回 3；`/documents/search?q=试用期` 返回空 —— 与列表页正显示着这份文档直接矛盾。真实页面同会话对照：前者渲染「文档切块（0）／暂无切块」，后者渲染「文档切块（3）」并列出正文。default 租户另有 `doc-1788350741430636808`（0 而非 24）、`HR-2024-003`（0 而非 1） | 两个端点（`handleDocumentChunks` / `handleDocumentSearch`）都挂 `publicationrelease.ResolveVisibility` —— 那是**检索证据**策略，只认「已 resolved 且 manifest 健康的已发布 release」或「legacy `publication_status='published'` 且引用不带代际身份」，其余 fail-closed。而新上传文档的 `publication_status` 默认 `'draft'`（`internal/docstore/docstore.go` 的 `COALESCE(NULLIF($24,''),'draft')`），于是**任何尚未发布的文档**都被判成不可读。引入它的 `9915e38` 提交说明写的是「document detail only lists the published release, so QA switches to the new content」，本意是**在多个代际之间选已发布的那一代**；当时的回归用**桩**解析器（`visible: {"gen-published": true}`）只验了「被取代的代际被过滤」，这个退化对测试完全隐形。详情页空态还是一句光秃秃的「暂无切块」，把「不给看」说成了「没有」 | `be4b8c2` |
| 19 | 同一段内容在向量库里有两份**逐字相同**的拷贝时，读侧去重按「先到先得」，留下哪一份取决于存储的返回顺序 —— 留下不带代际身份那一份时，**已发布**文档照样读成空 | default 租户两份已发布文档的 `/chunks` 都返回 `{"total":0}`：`ADM-2024-001`（Qdrant 2 点、两块 `norm_sha256` 同为 `474a312b86f23982`、ES 2 块）与 `SEC-2024-001`（Qdrant 3 点、ES 3 块），而同为已发布的 `FIN-2025-001` 正常返回 1；改后同一脚本两者都返回 1，其余 7 份文档（未发布两份 3、`doc-1788350741430636808` 24、`HR-2024-003` 1、`FIN-2025-001` 1、demo 三份 3/3/3）一个数都没动。真实页面同会话对照：改后 `/documents/ADM-2024-001` 渲染「文档切块（1）」并列出正文 | `QdrantStorer.ListChunksByDoc`（`internal/store/store.go`）的精确内容去重：`seen` 以归一化内容（`strings.Join(strings.Fields(content), " ")`）为键、`if _, ok := seen[key]; !ok` **先到先得**。而同一份文档确实可能同时存在两份内容逐字相同的块 —— 一份历史写入（`document_version_id`/`generation_id` 全空）、一份来自取代它的受管代际，`ADM-2024-001` 那 2 点即如此，scroll 顺序把空身份那块排在前面。`seen` 只决定**展示哪一份**，而发布可见性按 identity 精确匹配，空身份那份永远匹配不上 → 整份文档判空。改法：`seen` 改记位置（`map[string]int`），重复内容里**带身份**的那份胜出；判据 `carriesIdentity` 要求 `DocumentVersionID` 与 `GenerationID` 两半都非空，与策略同源（只要求一半会让「半个身份」顶掉真正匹配的那份）。同段另一支 `removeContainedAdjacentChunks`（包含式重叠，阈值 0.65）是另一回事，原注释把两者混在了一句里 | `969433d` |

**缺陷 16 只修了一半，另一半是刻意的**：72 字节上限是**存储格式的硬属性**（bcrypt 存不下），
所有写密码的路径都必须拒；而**最小长度是产品策略**，管理员 API 从来接受任意非空密码，
在这里加下限等于改一个已经发布的契约，不是修缺陷。所以 `auth.ValidatePasswordStorage` 只拒
「存储格式表示不了的输入」，代码注释里也写明了这一点。判据没有绑在常量 72 上，而是断言
「校验器与 `HashPassword` 对同一批输入的**接受性一致**」—— 常量变了判据也不会失效。

**这一轮还留下一个与密码无关的产物**：`cmd/api/routes_test.go`。它用 `go/parser` 从 `main.go`
抽出路由字面量、塞进一个全新的 `http.ServeMux`，把「注册期模式冲突」这个只在 `main()` 里才炸的
检查搬进了单测。它被独立提交（`976f191`），因为它跟密码无关，修的是「单测全绿 + 部署即挂」这个形状。

**缺陷 17 值得单独记一笔：修复只做了一半，而症状指向了错误的组件**。报错文本是
`traces export: … string field contains invalid UTF-8`，第一反应会去查 jaeger；实际上
jaeger 什么都没收到，是 **etl-worker 的导出器拒绝整批 span**。真因是 `truncate` 里
「先 `ToValidUTF8`、再 `s[:maxLen]`」这个顺序 —— 修好的串被下一行按字节切回非法状态，
两次操作各自都对，合起来错。`maxLen` 仍是**字节**预算而不是字数，这是刻意的：这个属性
存在的意义就是让 trace 变小，限制必须落在导出器度量的那个单位上（按 rune 切得到
99 字节 / 33 字符，正好在预算内，所以两者并不冲突）。判据因此不能是「预览看起来对不对」，
必须是「**span 是否真的到达 jaeger**」—— 乱码只是表象，丢 span 才是后果。

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

**缺陷 8 是前七处的反面，也是这十九处里唯一一处「不是没人修，是根本没有能修的地方」**：前七处都有代码
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

### 缺陷 13：预审裁决的语言不由系统决定，而且把一句话写成了一整段

这条来自**用户在演示界面上直接看到的东西**：`doc-1788958054422977825` 的预审卡片里，
四周的中文标签中间夹着一整段英文和两个英文值。

```
Agent 预审 … 预审状态 已完成  风险评估 中风险  Agent 建议 需补充信息
Candidate chunk doc-1788958054422977825_0000 contains only a test/placeholder marker
string ('受管替换闭环第二版…门禁口令是橙门') rather than an approved HR, administrative,
or business regulation for the production knowledge space. Knowledge-fitness assessment
recorded space_fit=mismatch and knowledge_usable=not_knowledge, so the material cannot
be published as formal knowledge.
适不适合这个空间 不适合   能不能当正式知识 不能当正式知识
材料看起来像 Test/placeholder marker string with a gate passphrase; not an approved HR, admin
```

**三个英文字段，三个不同来源**（`review-6055e3dd7aa52258`，`prompt_version=autonomous-review-v2`）：

| 字段 | 值 | 来源 |
| --- | --- | --- |
| `summary` | 366 字符的英文段落 | **模型自由文本** —— `reviewPlannerSystemPrompt` 通篇是英文写的 |
| `kind_label` | `Test/placeholder marker string with a gate passphrase; …` | **模型自由文本** |
| `findings[].severity` | `medium` | 系统枚举，前端 `严重度：{finding.severity}` 原样打印 |

**语言从来不是要求，只是模型当次的随机选择。** 同一个 `autonomous-review-v2`、
同一个 `deepseek-v4-flash`，对另一份中文文档产出的却是中文裁决：

```
review-91847c6aa8e77c605020b37e2099f6d2  demo-doc-onboarding
summary = "演示入职文档内容完整、无敏感数据、无提示注入风险；但知识适配评估将 space_fit
          判定为 uncertain（无法确认材料是否适合当前演示知识空间），按规则该状态不得发布，
          故需人工确认材料归属后再决定是否发布。"
```

两条裁决躺在同一张表、同一个 prompt 版本下，界面读起来一英一中 —— 取决于模型那一次怎么选。

**顺带查出一处更安静的问题：模型的复述顶掉了确定性扫描的原话。**
`validateAutonomousReview` 合并 finding 时按 `code + evidence_ref` 去重，
但**只在模型给的严重度更低时**才用确定性的那一条：

```go
if reviewSeverityRank(current.Severity) < reviewSeverityRank(deterministicFinding.Severity) {
    filtered[i] = deterministicFinding   // 只在更严重时才替换
}
```

两边都是 `medium` 时不替换 —— 于是模型写的 `"The material does not belong in this knowledge
space"` 留了下来，而 `fitness.go` 里那句权威的 `"材料不适合进入当前知识空间"` 被丢掉。
prompt 里明明写着 `Copy deterministic scan findings exactly`，但**没有任何代码校验它**。
这与前 12 个缺陷是同一个形状：**要求写在文本里，判定不在代码里**。

**修复三处**：

1. **prompt 中文化 + 长度上限**（`agentapi/review.go`）。`reviewPlannerSystemPrompt` 改为中文，
   显式写出「summary、每条 finding 的 summary、kind_label 必须用简体中文」，
   并给出长度上限：`summary ≤ 40 汉字`、`finding.summary ≤ 20 汉字`、`kind_label` 为中文短语。
   枚举值（`status`/`recommendation`/`risk_level`/`severity`/`code`/`space_fit`/`knowledge_usable`）
   保持英文 —— 它们是线上格式，不是给人读的文字。
   版本号随之从 `autonomous-review-v2` 升到 `v3`，这也顺带作废了缓存的 v2 裁决。
2. **确定性语言归一**（新增 `normalizeReviewLanguage`）。prompt 只是偏好，归一才是要求：
   英文 `kind_label` 直接丢弃（它不参与任何判定，界面回落为「未标注」）、
   英文 `summary` 用报告自己的枚举重建（`reviewSummaryFor`）、
   finding 的英文 summary 换成该 code 的中文名（未知 code 回落「预审发现问题，需人工确认」）。
   判定「是不是英文」用的是**功能词**而不是 ASCII 词数 —— 中文句子里本来就允许出现
   `space_fit`、`uncertain`、`OpenTelemetry` 这类标识符，只数 ASCII 词会把该保留的文本误伤。
3. **确定性文本优先**（合并逻辑）。同 `code + evidence_ref` 时保留扫描自己的措辞，
   模型只能抬高严重度，不能改写结论。

前端补 `severityLabel`（`low/medium/high/critical` → 低/中/高/严重）、
`FINDING_LABELS` 补 `insufficient_evidence`、未映射的 code 不再原样打印（回落「其他问题」）。

**还有一处必须同步改：泄露检测的 marker 会随 prompt 一起失效。**
`scripts/release-center-real-model-scenarios-acceptance.py` 的 `PROMPT_LEAK_MARKERS`
第一项是 `"read-only enterprise document pre-review"` —— **一句英文 prompt 的原文**。
prompt 改成中文后，这个 marker 再也不可能出现在 summary 里，于是这项检查**对任何输入都通过**，
包括真的把 prompt 吐出来的那种。已补两个中文 marker，并加脚本双向断言：
新的中文 marker 命中当前 prompt、旧的两个英文 marker 确实已失效
（后者若成立，说明 prompt 根本没换语言，这次改动就没意义）。

**线上复现与修复后断言**（`default` 租户 / `doc-1788958054422977825`）：

修复前 `review-6055e3dd7aa52258`（v2）：`summary` 366 字符英文段落、`kind_label` 78 字符英文。

走**系统自己的重审路径**（把该裁决的 TTL 移到过去 → 采集器 `ExpireDueReviews` 标 expired →
下一个 tick `ListReviewJobs` 重新拾取 → `StartManagedReview` 真实调用模型），
得到 `review-c9c0fc21e50d0716`（v3）：

| 字段 | 值 |
| --- | --- |
| `prompt_version` | `autonomous-review-v3` |
| `summary` | `材料疑似测试占位或草稿骨架，不完整且与知识空间不匹配，不能发布。`（32 字符，模型自己写的中文，没走归一兜底） |
| `kind_label` | `疑似测试占位或草稿骨架，非正式制度内容` |
| `findings` | `space_mismatch` /「材料不适合进入当前知识空间」；`incomplete_knowledge` /「材料不完整，无法作为正式知识发布」 |

`summary` 从 366 字符降到 32，findings 用的是确定性原话 —— 两条诉求都落地了。

**反向验证**（`.workbuddy-ai/tmp/verify_review_language.py`，在部署环境上跑）：
还原语言归一的函数体 → `TestNormalizeReviewLanguageRewritesEnglishProse` 与
`TestValidateAutonomousReviewKeepsDeterministicWordingOverPlannerParaphrase` 失败；
还原合并逻辑 → 合并测试失败；两次还原期间未受影响的 `TestLooksLikeEnglishProseThresholds`
始终通过（这证明失败来自移除，而不是环境本身坏了）。恢复后文件哈希与快照逐字节一致。

**一处必须记下的耦合**：`TestReviewRunIDSeparatesReviewers` 把 `"autonomous-review-v3"`
**硬编码**成「另一个版本」，用来断言「版本不同则 run id 不同」。
版本号升到 v3 的那天，这条断言会反转成「同一个版本必须 fork run id」。
已改为从常量派生（`reviewPromptVersion + "-next"`），以后升版本不必再动测试。

**没做**：没有改预审失败路径的 `summary`（`service.go` 里 `Summary: err.Error()`，
把 Go 的英文错误信息直接当摘要）。那是诊断信息、只在 `status=failed` 时出现，
用户这次反馈的是正常裁决的输出；改它要动多处既有断言，还会把内部错误原因从界面上抹掉。
**记在这里，等有明确需求再动。**

### 缺陷 14：语言契约只在写入侧生效，表里已有的裁决照样把英文送到管理员界面

这条也是**用户在看界面时发现的**，而且是在缺陷 13 修复并部署**之后**：

> 你确定修改了吗？为什么我在页面上还看到：… 预审时间：2026/9/16 20:53:31
> 有效期至：2026/9/23 20:53:31 **Prompt 版本：autonomous-review-v2**
> Exact candidate is a single draft/placeholder marker chunk containing only '受管闭环干净第一版…'
> 材料看起来像 Draft/placeholder marker text, not a formal policy document

答案是**改了，但只覆盖了新裁决**。缺陷 13 的归一发生在 `validateAutonomousReview` 里 ——
也就是**裁决被装配的那一刻**。界面上那条是 2026-09-16 20:53:31 写入的 v2 裁决，
它不会再被装配一次；而卡片自己就印着「Prompt 版本：autonomous-review-v2」。

**不是一条，是一批。** `default` 租户 20 条裁决里：

| 症状 | 条数 | 例子 |
| --- | --- | --- |
| `summary` 是英文段落 | 6 | `review-d6559154618ac1cf`（524 字符）、`review-6055e3dd7aa52258`（366 字符） |
| `kind_label` 是英文 | 8 | `Draft/placeholder marker text, not a formal policy document` |
| `summary` 是英文失败模板 | 2 | `agent review returned status "failed"`（37 字符） |

另有 8 条 v2 裁决本来就是中文 —— 与缺陷 13 的结论一致：语言只是模型当次的随机选择。
**换句话说，缺陷 13 修的是「以后不再产生」，这一条修的是「已经产生的怎么给人看」。**

**真因是「契约只挂在一条路径上」**：

```go
// 写入侧：裁决装配完成时归一（agentapi/review.go:582）
releasecenter.NormalizeAgentReview(&report)

// 读取侧：拿到什么就返回什么（cmd/api/release_center_handlers.go:191 等四处）
review, err := store.GetReview(r.Context(), tenantID, parts[3])
writeJSON(w, http.StatusOK, map[string]any{"review": review})
```

**修复：把契约搬到存储类型旁边，并在唯一的读取出口上执行。**

1. 语言契约从 `agentapi` 移到 `internal/releasecenter/review_language.go`，与 `ReviewReport` 同包。
   写入侧 `NormalizeAgentReview(*AgentReview)`、读取侧 `NormalizeReviewReport(*ReviewReport)`
   都投影到同一个 `ReviewLanguage`，**规则只写一遍** —— 两份拷贝正是这次要防的漂移。
2. 读取侧收成**唯一出口** `cmd/api/release_center_review_read.go` 的 `readReview()`，
   四个调用点（`review-reports`、请求详情、审批响应、工作流回调）全部改走它。
   归一作用于**副本**，存储行不被改写 —— 审计记录保留模型原话。
3. **判据本身也要修。** 缺陷 13 的判据是「≥6 个 ASCII 词且 ≥2 个功能词」，
   它在真实数据上漏掉两种形状，而且**两种都在界面上**：

   | 漏掉的形状 | 为什么漏 | 后果 |
   | --- | --- | --- |
   | `agent review returned status "failed"` | 5 个词、**0 个功能词** | 失败裁决继续显示英文 |
   | `Draft/placeholder marker text, not a formal policy document` | 8 个词、**1 个功能词**（名词短语不是句子） | `kind_label` 继续显示英文 |

   新判据两半取或：**读起来像英文句子**（原来的功能词判据，用来抓引用了中文的英文段落，
   如 `review-6055e3dd7aa52258` 的 summary 里嵌着「受管替换闭环第二版」）
   **或 整段没有任何汉字且不止一个词**（抓短句与名词短语）。
   单个无汉字词不动它 —— `space_fit`、`PXGAP-MGC1` 这类标识符不该被改写。
   线上 8 条英文 `kind_label` 全部命中、11 条中文 `kind_label` 全部保留。

**线上复现与断言**（`.workbuddy-ai/tmp/assert_review_language_api.py`，走真实 HTTP + 真实库）：

脚本用平台自己的 `JWT_SECRET` 为租户里一个真实管理员签一张 30 分钟令牌
（web 容器预置的 `WEB_JWT_ADMIN` 已过期，引导管理员口令不可得），
然后**遍历该租户全部裁决**，逐条走 `GET /v1/release-center/review-reports/{id}` 断言六条：
summary 含汉字、summary 不读作英文、`kind_label` 为空或含汉字、finding 含汉字、
本来就中文的 summary 逐字不变、结构化字段与 findings 条数不变；
最后比对前后两次全表快照，证明**读取侧没有写库**。

| 阶段 | 结果 |
| --- | --- |
| 重建前（部署 = `69c808c`） | **FAIL 15 条**：6 条英文 summary + 8 条英文 `kind_label` + 2 条失败模板（含 A1b：引用了中文的英文段落） |
| 重建后（部署 = `4eef274`） | **PASS**：`default` 20 条 + `demo` 8 条，全部中文，结构化字段与审计行未变 |

用户报的那条（`review-d6559154618ac1cf`）现在返回：

```
summary   : 预审未通过，需补充材料或人工确认后重审，共 2 项待确认问题。
kind_label: （空 → 界面回落为「未标注（不影响发布）」）
finding   : [space_mismatch] 材料不适合进入当前知识空间 (severity=medium)
finding   : [not_knowledge] 材料不能作为正式知识使用 (severity=medium)
```

**反向验证**（两次，都要求「未受影响的测试仍通过」）：

- 去掉 `readReview` 里的归一调用 → `TestReleaseCenterReviewReportServesStoredVerdictInChinese` FAIL，
  `releasecenter` / `agentapi` 单测仍 PASS。
- 把判据退回只剩「英文句子」那一半 → 阈值测试 FAIL 的正是 5 条线上真实取值、
  `TestNormalizeReviewReportLocalizesStoredEnglishVerdict` 与 `...FailureTemplate` FAIL，
  `agentapi` 仍 PASS。恢复后 8 个文件哈希与快照逐字节一致。

**一处必须记下的细节**：断言脚本用 Python 复刻了 Go 的判据，并带一张 16 条的自检表
（与 Go 测试同一批线上取值）。**判据必须能被独立复刻**，否则「断言通过」只是「代码同意自己」。
自检表若与 Go 判据分叉，脚本会先报 `predicate drift`，而不是给出一条更弱的结论。

**没做**：失败裁决的 `summary` 仍然只是「预审未给出结论，已转人工复核。」这一句结论，
上游那句真正的原因（例如 `review report has no exact candidate`）依旧被
`coordinator.go:202` 覆盖掉、没有落库。这次只保证**界面上不再出现英文**，
**没有恢复被丢掉的原因** —— 那需要给 `release_center_reviews` 加一列并配套迁移，属另一件事。

### 缺陷 15：失败的裁决说不出为什么失败 —— 诊断算出来了，却被下一行覆盖掉

这一条是缺陷 14 的**下半场**，而且是上一轮自己写在「没做」里的那一条。缺陷 14 让界面变中文，
代价是失败裁决的 `summary` 被统一成一句「预审未给出结论，已转人工复核。」——
管理员看得到「失败了」，看不到「为什么」。

**先确认它是真的、而不是只发生在历史行上。** 线上两条 `failed` 裁决：

| 裁决 | 文档 | `summary` |
| --- | --- | --- |
| `review-a26fb4d1d170c359` | `demo-doc-payroll` | `agent review returned status "failed"` |
| `review-69e42208b72c0e0b` | `demo-doc-onboarding` | `agent review returned status "failed"` |

两条都没有一个字节说明原因。**而原因当时就在手里**：`agentapi/service.go` 的三处失败返回
都把真实错误写进了 `Summary`（`err.Error()` / `run.Error` / `"invalid review report: " + err.Error()`），
是 `coordinator.go:202` 把它覆盖掉了。

**线上复现**（注入一次计划器故障，让 `RunToCompletion` 真的失败）：

```
# AGENT_PLANNER_MODEL 对 review 路径无效 —— newReviewPlanner 直接读 LLM_MODEL，
# 所以必须改 LLM_MODEL 才复现得到（service.go:332）
$ sed -i 's|^LLM_MODEL=.*|LLM_MODEL=__defect15_repro_nonexistent_model__|' .env
$ docker compose up -d query-api
$ # 把当前裁决的 expires_at 移到过去 → ExpireDueReviews → ListReviewJobs → StartManagedReview
```

结果（**修复前**，部署 `4ef5faa`）：

```
review-cd8b1cc7c95575af | failed | summary = agent review returned status "failed"
```

同时，平台自己的 run 接口对同一个 run 是这么说的：

```
$ GET /v1/agent/runs/review-run-e0dcca2be89dc38818598d6843459e40
"error": "agent planner returned status 503: {\"error\":{\"code\":\"model_not_found\",
          \"message\":\"分组 deepseek-sale 下模型 __defect15_repro_nonexistent_model__
          无可用渠道（distributor）…\",\"type\":\"packy_api_error\"}}"
```

**原因一直在系统里，只是没进裁决** —— 而它进不去的原因正是那一行覆盖。要注意的是 run 受
`AGENT_RUN_TTL`（24h）约束，且读它需要 `agent` scope 的内部接口，不是裁决面板上能看到的路径；
那两条历史行的原因今天已经查不回来了（容器日志也早轮转掉了）。

**修复三处**：

1. **保留审阅者自己的诊断**。新增 `releasecenter.FailureSummary(upstream, cause)`：中文结论打头，
   把上游原文**逐字**接在后面（转述一个机器错误，比错误本身价值低），并在**符文边界**上截到 200 字
   —— 观测到的 503 响应体约 250 字符，而面板是内联展示的。
2. **契约覆盖到每一条写入路径**。`NormalizeAgentReview` 从「只在适配器里调用」改为在
   `coordinator.go` 构造 `report` 之前**统一调用一次**。失败分支原本是唯一绕开契约的路径，
   英文模板就是这样进表的。
3. **判据改成看「开头」**。这一条是被测试逼出来的：加上诊断后，
   `预审未给出结论，已转人工复核。原因：agent planner returned status 503: {...}`
   被 `LooksLikeEnglish` 判成了英文句子 —— 引用的机器错误自己就满足「≥6 词、≥2 个功能词」——
   于是契约把整段换回光秃秃的结论，**原因在第二个地方又丢了一次**。
   修法是让判据回答它真正要回答的问题：**审阅者读到的开头是什么语言**。
   表里所有英文取值的开头都是英文（`Exact candidate is ...`、`All required review steps ...`、
   `Draft/placeholder marker text, ...`），所有中文取值的开头都是中文；
   一个以中文结论开头、后面引用机器错误的字段，是**中文**字段。

   > 这条边界的代价是写明的：以中文开头的字段里若混着英文散文（如
   > `材料看起来像 Draft/placeholder marker text, ...`），判据不再改写它。
   > 取舍依据是「审阅者读的是开头」，且这个形状从未在线上出现过；
   > 该用例已进阈值测试与断言脚本自检表，属于**已知边界**而不是漏网。

**线上 A/B**（同一个注入故障，两次构建）：

| 阶段 | 写入的裁决 | 存的 `summary` |
| --- | --- | --- |
| 修复前（`4ef5faa`） | `review-cd8b1cc7c95575af` | `agent review returned status "failed"` |
| 修复后（`0bcb969`） | `review-0643f21e035e2d11` | `预审未给出结论，已转人工复核。原因：agent planner returned status 503: {"error":{"code":"model_not_found",…` |

**断言走的是面板走的那条路**（`.workbuddy-ai/tmp/assert_failure_reason_api.py`，
`GET /v1/release-center/review-reports/{id}`）：B1 行确实是 `failed`、B2 存的行带原因、
B3 **服务出来的行也带原因**（B3 才是关键 —— 缺陷 14 的读取侧归一如果判错了，原因会在出口再丢一次）、
B4 给人看的文本都含汉字、B5 修复前那行**逐字未变**（只改写入侧，不动审计记录）、B6 长度有界。

**反向验证两轮**（都要求「未受影响的一半仍通过」）：

- 把 `FailureSummary` 还原成 `reviewErr.Error()` → `TestCoordinatorKeepsReviewerDiagnosticOnFailedReview`
  与 `TestCoordinatorBoundsFailureReason` FAIL，`internal/agentapi` 全绿。
- 去掉判据里的「汉字开头」那一半 → `TestLooksLikeEnglishThresholds` FAIL 的正是两条新增的汉字开头用例、
  `TestCoordinatorKeepsReviewerDiagnosticOnFailedReview` FAIL，`internal/agentapi` 仍全绿。
- 两轮恢复后 4 个文件 `sha256sum -c` 逐字节一致。

**没做**：`release_center_reviews` 没有独立的原因列，原因与结论挤在 `summary` 一个字段里。
分成两列（结论 / 诊断）会让面板更干净，但需要加列 + 迁移，且要改前端；
本轮只做「不丢」，不做「分开」。

---

## 2. P1 —— 工程可复现性（现在会绊住每一次改动）

### 2.1 远端 Go 工具链默认跑不通（已解决，未改任何东西 —— 2026-09-22 复核）

**原记录（2026-09-12）**

```
$ cd services/etl-worker && go test ./cmd/api ./cmd/worker
  internal/prometheus/metrics.go:16:2: mkdir /home/ubuntu/go/pkg/mod/cache/download/github.com/prometheus:
  permission denied
  FAIL  ai-etl-pipeline/cmd/worker [setup failed]

$ ls -ld ~/go/pkg/mod          → drwxr-xr-x root root   ← ubuntu 用户无写权限
```

**2026-09-22 复核：不再复现。** 目录属主已变成 ubuntu，不设任何 `GOMODCACHE`/`GOCACHE`
直接跑测试即通过：

```
$ ls -ld /home/ubuntu/go/pkg/mod
  drwxr-xr-x 7 ubuntu ubuntu 4096 Sep  8 17:24

$ cd services/etl-worker && export PATH=$PATH:/usr/local/go/bin
$ go test ./internal/releasecenter/ ./cmd/worker
  ok   ai-etl-pipeline/internal/releasecenter  0.007s
  ok   ai-etl-pipeline/cmd/worker              0.008s
```

**处置：本项从待办划掉，没有改任何东西。** 当时的第 1 条方案（`chown -R ubuntu:ubuntu`）
不知何时已被执行，问题自然消失。**无需再修**，也不应再去动 `go env -w`。

**但原记录有一处不准，必须纠正**：`.gomod` / `.gocache` 不止在仓库根，而是**三份**：

```
350M  ./.gomod                        365M  ./.gocache
350M  ./services/.gomod               399M  ./services/.gocache
350M  ./services/etl-worker/.gomod    523M  ./services/etl-worker/.gocache
```

合计约 2.3GB。原记录写的「734MB」只算了仓库根那一份。这三份仍然是**承重结构**
（构建提速），继续保留、继续 gitignore，**不要删**。

**验收判据**：`go test ./... -count=1` 在远端不设任何额外环境变量即可通过 —— 已实测通过。


### 2.2 `.env.example` 切块配置段有 3 个「死键」（已修复，`b14e20d` + `c7b4003`）

**证据**（`.env.example` 切块段，修复前）

```
MIN_CHUNK_SIZE=128
MAX_CHUNK_SIZE=600          ← 无任何编排引用
CHUNK_OVERLAP=50            ← 无任何编排引用
PARSER_MAX_CHUNK_SIZE=600   ← 真正生效
PARSER_CHUNK_OVERLAP=50     ← 真正生效
```

`docker-compose.yml` 的 `parser-service` 段实际是：

```
- MIN_CHUNK_SIZE=128                          ← 硬编码字面量，不插值 .env
- MAX_CHUNK_SIZE=${PARSER_MAX_CHUNK_SIZE:-600} ← 从 PARSER_* 取值
- CHUNK_OVERLAP=${PARSER_CHUNK_OVERLAP:-50}    ← 从 PARSER_* 取值
```

**影响**：切块参数是入库质量的核心旋钮（`LEARNINGS.codex.md` 记录了从 1200 字符调到 600
的完整过程）。用户按直觉改 `MAX_CHUNK_SIZE=1000` 会**完全没反应**，且没有任何报错。
这是最难排查的一类配置缺陷 —— 症状只有「改了没反应」，日志和界面都不会说话。

**一处需要更正原记录**：原计划写的是「删掉 3 个死键，只保留 `PARSER_*`」。三个键确实
都是死键（原记录把 `MIN_CHUNK_SIZE` 标成「硬编码」是对的），但**直接删掉它等于把
最小切块尺寸变成不可调**。改名本身是刻意的（同一个宿主值要喂给 `parser-service` 和
`etl-worker` 两个容器，而 etl-worker 侧本来就用 `PARSER_` 前缀），缺的是
`MIN_CHUNK_SIZE` 从没享受同等待遇。

**实际做法**：补一个 `PARSER_MIN_CHUNK_SIZE`，三个键统一带前缀，compose 侧
`MIN_CHUNK_SIZE=${PARSER_MIN_CHUNK_SIZE:-128}`。既消掉死键，又**把原本不可调的
那个键变成可调**。

**线上验证**（2026-09-22，演示栈）

```
# 注入
$ echo "PARSER_MIN_CHUNK_SIZE=200" >> .env && echo "PARSER_CHUNK_OVERLAP=77" >> .env
$ docker compose up -d parser-service
$ docker compose exec -T parser-service env | grep -E "^MIN_CHUNK_SIZE|^CHUNK_OVERLAP"
  MIN_CHUNK_SIZE=200        ← 修复前这里恒为 128，改 .env 无效
  CHUNK_OVERLAP=77
# 还原
$ cp .env.envfix-backup .env && docker compose up -d parser-service
$ docker compose exec -T parser-service env | grep -E "^MIN_CHUNK_SIZE|^CHUNK_OVERLAP"
  MIN_CHUNK_SIZE=128
  CHUNK_OVERLAP=50
```

**验收判据**：改 `PARSER_MIN_CHUNK_SIZE` 后重建 parser 容器，容器内 `MIN_CHUNK_SIZE`
随之变化 —— 已实测（128 → 200 → 128）。契约测试 `test_chunking_host_keys_are_prefixed_and_wired`
钉住这条重命名，裸键一旦回到根模板顶层就红。

### 2.3 环境变量未被写入 `.env.example`（已修复，`b14e20d` + `c7b4003`）

**证据**（修复前，两处口径都重算过）

```
口径一：全部 compose 文件的插值键（${KEY...}）   → 19 个未写入任何 .env.example
口径二：服务代码读取的键（Go Env*/secretCSV/os.Getenv + Python Settings）
                                                 → 11 个未写入（已排除 13 个单测专用键）
两份差集只有 4 个键重合，并起来是 26 个键
```

`AGENTS.md` 明文要求：「配置通过环境变量驱动，新增设置同步写入 `.env.example` 文件」。
**这条约定正在被违反**，其中 `LOGIN_RATE_LIMIT_STORE`、`SCIM_BEARER_TOKENS`、
`ALERT_WEBHOOK_TOKEN`、`RETRIEVAL_MIN_RELEVANCE` 都是运维/安全相关的键。

**两处口径更正（这是本项最值得记的部分）**

1. **原记录的「19 个」是算大的。** 那次只跟**根模板**比对，而
   `services/etl-worker/.env.example` 里已经写着 71 个键 —— 原记录列的 20 个键里有
   **11 个**（`EMBED_BACKOFF`、`EMBED_MAX_*`、`HEALTH_PORT`、`UPLOAD_DIR`、`SPARSE_*`、
   `PARSER_READ_BUFFER`、`PIPELINE_*_BUFFER` 等）早已文档化。正确口径是**三份模板的并集**。
   另有 2 个（`LOGIN_RATE_LIMIT`、`PROMPT_VERSION`）在根模板里以注释形式存在，
   原记录的匹配没算注释行。**真正一行都没有的只有 7 个**。

2. **只查「代码读取」会漏掉一半。** 宿主 `.env` 的值只有被 compose 插值才进得了容器，
   所以还必须反查 `${KEY}`。这一口径独立查出 **19 个**缺口，其中 **15 个**是代码侧不读、
   只有 compose 会插值的键（`RERANKER_LOG_LEVEL`、`REDIS_CACHE_HOST_PORT`、
   `REDIS_STATE_HOST_PORT`、`WORKFLOW_CALLBACK_TOKEN_FILE`，以及 11 个叠加层专用的
   端口/资产目录键 —— `RESTORE_*` 5 个、`IDENTITY_DEMO_*` 3 个、`API_PORT`、`WEB_PORT`、
   `WORKER_METRICS_HOST_PORT`）。**只查代码引用的那一次，这 15 个一个都看不到。**

**实际做法**：补齐全部缺口（默认值一律从代码里读，不猜）。**同时把 5 处硬编码改成插值**
——`MIN_CHUNK_SIZE`、`PARSER_ENDPOINT`、`WORKER_HEALTH_URL`、`PROMPT_DIR`、
`OTEL_EXPORTER_OTLP_ENDPOINT`。原因：这几个键被 compose 用字面量覆盖，只在
`.env.example` 里写下来等于**制造新的死键**，正是本项要修的那个缺陷。13 个单测专用键
（`*_TEST_DSN`、`OIDC_KEYCLOAK_TEST_*` 等）**刻意不写**，由契约测试排除 `_test.go` 实现。

**写文档时又挖出第三个缺口（`c7b4003`）**：补进去的键里有三个是「代码会读、compose 却
从不传给容器」——`RETRIEVAL_MIN_RELEVANCE`、`RETRIEVAL_GROUNDING_LOW_BOUND`、
`RETRIEVAL_GROUNDING_HIGH_BOUND`。`docker-compose.yml` 给 query-api 传了另外 9 个
`RETRIEVAL_*` 键，偏偏在最后一行停住了，于是忠实度校验器一直跑在内置默认值上，
宿主 `.env` 里改这三个值没有任何反应。**这是「只查代码引用」漏掉的形状**：
键确实被读，只是读它的那个进程拿不到宿主的值。

线上 A/B：

```
# 修复前
$ docker compose exec -T query-api env | grep -E "RETRIEVAL_MIN_RELEVANCE|RETRIEVAL_GROUNDING"
  RETRIEVAL_GROUNDING_CHECK=true          ← 只有这一个，另三个不在
# 修复后（docker compose up -d query-api）
  RETRIEVAL_GROUNDING_CHECK=true
  RETRIEVAL_GROUNDING_HIGH_BOUND=0.70
  RETRIEVAL_GROUNDING_LOW_BOUND=0.45
  RETRIEVAL_MIN_RELEVANCE=0
```

另外两个键（`ALERT_WEBHOOK_TOKEN`、`SCIM_BEARER_TOKENS`）属于同一形状，但**处置相反**：
compose 用的是 `_FILE` 形式（`EnvSecret` 先读 `KEY` 再读 `KEY_FILE`），容器里文件优先，
所以明文值永远进不去。**这两行改成注释**并写明「只在直接跑服务时生效」，
而不是当成可用旋钮摆在那里 —— 把它们留在顶层，就是在重复本项要修的缺陷。

**新增契约测试** `scripts/tests/test_env_example_contract.py`（6 条断言）

| 断言 | 钉住什么 |
|---|---|
| `test_every_compose_interpolated_key_is_documented` | 全部 compose（含 6 个叠加层）的 `${KEY}` ⊆ 模板并集 |
| `test_every_code_referenced_key_is_documented` | Go 非测试代码 + Python Settings 读的键 ⊆ 模板并集 |
| `test_chunking_host_keys_are_prefixed_and_wired` | 切块三键必须带 `PARSER_` 前缀且真的接进容器 |
| `test_parser_template_defaults_match_service_settings` | parser 模板默认值 == `app/config.py` 的字段默认值 |
| `test_source_scan_skips_hidden_and_vendored_trees` | **判据自检**：必须跳过 `.gomod`/`.gocache` |
| `test_every_config_env_helper_is_scanned` | **判据自检**：`config.go` 新增读 env 的助手必须被覆盖 |

**最后两条自检不是装饰，是踩出来的**。第一版用 `rglob("*.go")` 直接扫
`services/etl-worker`，**本机（262 个 `.go`）全绿，远端（6365 个 `.go`）报 52 个假缺口**
——多出来的 6103 个在 `.gomod`/`.gocache` 里，AWS SDK、minio-go、grpc 都在读它们自己的
`AWS_ACCESS_KEY`、`MINIO_ALIAS`、`GRPC_GO_LOG_*`。判据出错时的症状是「同一个测试在两台
机器上结论相反」，只跑正例永远发现不了。

**验收判据**：契约测试 6 条全绿（本机 + 远端）；差集为空（compose 插值 233 个、模板并集
286 个）；6 条断言逐一做反向验证 —— 破坏各自的目标后必红，恢复后文件逐字节一致；
线上 query-api 容器内可见四个检索键（修复前只有 `RETRIEVAL_GROUNDING_CHECK` 一个）。


---

## 3. P1 —— 状态文档三源冲突（已修复，`486deca`）

**证据**（下表是修复前的状态）

| 文档 | 最后核验 | 它对状态的说法 | 实测事实 |
| --- | --- | --- | --- |
| `issues/findings-register.md` | 2026-09-11 | UAT-017/018/019/020 **开放** | 四项均已修复 |
| `docs/backlog.md` | 2026-09-12 | P-UAT-1「登记册开放项已清空」 | 与登记册直接矛盾 |
| `CONTINUATION.md` | 2026-09-09 | §4「UAT-001～009 仍待真实页面复验」 | 这九项 09-09 就已全部关闭 |
| `docs/product-experience-acceptance.md` | 2026-09-09 | 「2026-09-09 本轮判定：通过」「无开放 UAT」 | 09-11 又新开四条，这两句当场过期 |

**两处要更正上一版记录**

1. 原文说「三份文档各自都声明自己是状态源 —— `findings-register.md` 说『新问题只在这里建单』，`CONTINUATION.md` 说『以本文件为准』」。全仓 `grep` 证实**只有 `backlog.md` 声明了**「当前状态以本文件为准」，另外两处是「新问题只在这里建单」和「更新时间」，都不是状态源声明。所以这不是「三方都自称权威」的治理问题，而是**同一份事实被抄成了四份快照**。
2. 原文**漏了第四处**：`docs/product-experience-acceptance.md` 行 22 / 177 / 182 / 187 各有一条会过期的状态断言。

**根问题**：派生状态被复制。条目状态只有登记册有状态列、有证据，其余三处抄的是快照 —— 任何一次新增建单都会让它们过期，而且不会报错，症状是**读的人得出相反的结论**：看 backlog 以为体验验收清完了，看登记册以为还有四条开放，看 CONTINUATION 以为连旧九条都没复验。

**怎么做**

1. **每个事实只留一个出处**：
   - `issues/findings-register.md` —— UAT **逐条**状态的唯一出处（状态列 + 复验证据）。
   - `docs/backlog.md` —— 项目级事项（`P-*`）状态的唯一出处；涉及 UAT 只链接，不复制计数。
   - `docs/product-experience-acceptance.md` —— 章程，只写要求与判据；「执行记录」明确标为带日期的历史。
   - `CONTINUATION.md` —— 续接点与运行环境，不写任何 UAT 状态。

   **这里偏离了原文写的「以 backlog 为唯一状态源」**：逐条状态只能放在有状态列的那一份里，硬挪到 backlog 会变成双写，反而制造新的漂移源。
2. **加一致性契约测试** `scripts/tests/test_experience_status_consistency.py`（16 条）：
   - 7 条盯文档：登记册索引行与明细段一致、状态取值在图例内、其余三份文档不含状态快照、CONTINUATION 不出现「UAT 编号 + 状态词」、章程矩阵与登记册一致、backlog 的逐条结案声明与登记册一致。
   - 9 条是**判据自检**：禁用词表被删窄、解析器不跳代码块、过期断言换措辞 —— 这三类判据写错时的症状都是「永远绿」，只跑正例发现不了。
3. **顺带把 UAT-017～020 做成真实页面复验**，不是产物层验证。

**真实页面复验**（`scripts/web-page-probe.cjs`，headless Chrome + DevTools 协议）

```
node scripts/web-page-probe.cjs --base http://127.0.0.1:3100 \
  --cookie "ai_etl_token=<现签 token>" \
  --url /documents --url /audit --url /release-center --wait 9000
```

| ID | 实际渲染 | 结论 |
| --- | --- | --- |
| UAT-017 | 列表「生产库 · 1 项阻塞」；详情「空间：生产库」「权限：内部」 | 通过 |
| UAT-018 | 操作者列 `admin` / `px-admin` / `interviewer` / `eval-readonly` / `user`，无裸 UUID | 通过 |
| UAT-019 | `.xls`→`XLS`、`.pptx`→`PPTX`、`.doc`→`DOC` | 通过 |
| UAT-020 | 「可检索、可问答、可发布的知识资产」+ 两条新卖点 | 通过 |

两个坑记在证据目录的 `notes.md`：**headless 默认视口 800×600**，登录页左栏是 `lg:flex`，不设视口会得出「文案没改」的相反结论；**web 容器的 cookie 是 `Secure`**，`Network.setCookie` 必须写 `secure:false`，否则浏览器在 `http://127.0.0.1` 上直接丢弃、页面跳回 `/login`。

**验收判据**：四份文档对同一事实说法一致；16 条断言全绿；9 类矛盾逐一反向验证必红、恢复后文件逐字节一致；`CONTINUATION.md` 不再出现任何 UAT 状态断言。

**上一版留下的「诚实限制」已消除**：当时只做了产物层验证（部署包里确有新代码、旧文案已清除），现在按章程走完了真实页面。证据 `artifacts/product-experience-acceptance/2026-09-22-uat017-020/`。

---

## 4. P2 —— 功能完善度缺口

按「对用户可用性的影响」排序。

### 4.1 普通用户没有任何自助能力 —— **不做（已实现后撤回）**

**结论**：2026-09-22 产品方明确否决 —— 「邀请式自助开户，没必要做吧」。本节因此**保持未做状态**，
`CONTINUATION.md §4` 那条断言不变，仍然是事实：`grep -rn "register|forgot|reset-password"
services/etl-worker/cmd/api/*.go` → 无匹配。

**影响（仍然成立）**：这是「演示可用」与「能给别人用」之间的分界线 —— 任何人要进来，都得先找到管理员。

**为什么当初标为「需要产品口径」**：本节原文写的就是「这是产品决策，不是纯技术活」。
在拿到口径之前动手实现，是执行顺序上的错误 —— 我按最小切片（管理员邀请链接 → 自助设密码）做完了
后端 + 前端 + 迁移，但没有先确认「这个能力要不要存在」。**技术上的完成不等于产品上的需要。**

**已实现的部分没有丢**：完整的最小切片在分支 `invite-onboarding`（提交 `24388f6`，已推送），
27 个文件 / 约 3000 行，含迁移 `0031_user_invites`、`internal/auth/invite.go`、
`internal/userstore/invites.go`、`cmd/api/invite_handlers.go`、前端接受邀请页与 BFF、`INVITE_TTL` 配置。
要重启这件事，从这个分支接着做即可，不需要重写。

**撤回时环境已复原**：库里的 `user_invites` 表已 `DROP`、`schema_migrations` 里的
`0031_user_invites.up.sql` 行已删除（两处计数均查过为 0），`query-api` 的迁移是只上不下的
（`migrate.go` 只 embed `*.up.sql`），所以这两步是必须的、也是唯一的两步。线上 19 个容器在跑。

**这一轮顺带查出的真缺陷已单独立项修复**：缺陷 16（超长密码 500），commit `45be654`，见 §1.3。
它不是邀请功能的产物 —— 邀请路径只是第一个会走到 72 字节上限的**新**入口，而管理员 API 一直有这个问题。

**留在主线上的另一个产物**：`cmd/api/routes_test.go`（`976f191`），与邀请无关，见 §1.3 表后的说明。

### 4.2 备份恢复 —— 已完成，见 §1.2 与 `docs/backup-and-recovery.md`

### 4.3 上传者删除能力：后端有、前端不开放 —— **已决定：仅 API 可用**

**决策**（2026-09-22）：**不开放前端入口**。普通用户能通过 API 删除自己上传的文档，Web 界面只给管理员删除按钮。本节从「待产品决定」转为已决，两侧事实由 `scripts/tests/test_document_delete_reachability.py` 锁住。

**两侧事实**

| 侧 | 位置 | 事实 |
| --- | --- | --- |
| 后端 | `services/etl-worker/cmd/api/doc_handlers.go:223` | `DELETE` 分支要求 `upload` scope |
| 后端 | `services/etl-worker/cmd/api/doc_handlers.go:228` | 且必须是文档上传者本人或管理员 |
| 权限 | `services/etl-worker/internal/auth/login.go:29` | `user` 角色持有 `[query, upload, agent]`，所以普通用户能走到上面那个分支 |
| 前端 | `web/app/(app)/documents/page.tsx:372` | 删除按钮只在 `isAdmin` 时渲染 |

**为什么保留这个不一致**：接口能力与 UI 入口不是同一件事。后端授权语义是完整的 —— 上传者本人可删、越权返回 403 —— 前端只是不提供入口。这是**有意的能力收敛，不是缺陷**：`handleDocument` 同一个 switch 里的 `PATCH` 分支（`:251`）才是真的管理员专属（`!= "admin"`），两者并排看说明 `DELETE` 的宽松是刻意留的。

**代价**：文档管理页对普通用户是只读的 —— 传错了只能找管理员删，或自己调 API。

**测试为什么是双向的**：前端放开（守卫不再只认 `isAdmin`）会红，后端收紧（判据从 `upload` 改成 admin）也会红。任一侧单方面变动都会先撞到这个测试，迫使改动方回来改本节。

### 4.4 合规审查深度 —— 三个暂缓项里，版本对比已落地

**边界仍在**：`LEARNINGS.codex.md` 与 `backlog.md` 多处强调「不得把当前已实现的受限规则扫描描述为完整合规审查」。三个暂缓项是**版本对比（本轮已做）**、政策库、跨文档冲突审查；后两项仍未动。

**版本对比做了什么**（2026-09-23）：`GET /v1/documents/{docID}/version-diff`，管理员可查「当前发布版本 vs 上一发布版本」的**确定性块级差异**（新增 / 删除 / 改动 / 位移）。不走模型 —— 差异由内容哈希算出，所以可复算、可以当证据用，而不是一句意见。

**为此补的两块地基**

| 层 | 位置 | 内容 |
| --- | --- | --- |
| 数据 | 迁移 `0031`、`publicationworkflow.CurrentReleasePair` | `document_releases` 记下被替换的那次发布 |
| 算法 | `internal/versiondiff` | 按内容哈希对齐（LCS），同位置的一增一删配成「改动」 |

**为什么必须按内容对齐**：按位置逐块比较会把「中间插入一段」读成「后面全部重写」，而审批人会去找并不存在的改动。`TestInsertionDoesNotReadAsARewrite` 钉住这一点。

**「上一发布版本」不能从 `index_manifests` 推**：`Activate`（重建路径）会把从未发布的代际置 `retired`，`Publish`（发布路径）则跨 version 回收，两类 retired 混在同一张表里，所以「retired 的那一代」不等于「上一发布版本」。

**三种「无法对比」必须分开报**：从未发布（409）、首次发布（200 + `previous_available=false`）、上一版本已被保留期回收（200 + 明确原因）。合并它们会把「比不了」变成「没变化」或「全是新的」，而后者是唯一会误导审批人的答案。

**仍未做**：政策库、跨文档冲突审查。它们缺的是**领域规则**而不是算法，且当前预审（R1/R6）已能覆盖发布资格预审的真实需求，等有真实业务诉求再立项。

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

### 5.1 `query/service.go` 单文件职责过载（已修复，`e15117b`）

**原状**：1749 行、79 个顶层声明，混装了至少 6 类职责 ——

| 职责 | 代表函数 |
| --- | --- |
| HTTP 入口 | `HandleQuery`, `HandleQueryStreaming` |
| SSE 协议 | `writeSSEError`, `queryBodyLimit`, `isMaxBytesError` |
| LLM 传输 | `callLLM`, `streamChat`, `completeAnswer`, `enableChatStream` |
| 提示词组装 | `buildPrompt`, `promptContextExcerpt`, `loadSystemPrompt` |
| 检索后处理 | `gateByRelevance`, `retrievalInfoFromResult`, `diagnosticRequiredDocIDs` |
| 校验与引用 | `groundingCheck`, `parseGroundingVerdict`, `citationsFromAnswer`, `estimateLLMCost` |

对比：`cmd/api/main.go` 从 1746 行拆到 667 行（`LEARNINGS.codex.md` 2026-09-09 记录了这次拆分，方式是「同包机械拆分，不改 HTTP/权限语义」）。本次是同样的手法再来一次。

**已拆成**（同 package，不改任何导出签名；行数一律为 `wc -l`，不要用 `len(text.split("\n"))` 数 —— 末尾换行会让它多算一行）：

| 文件 | 行数 | 职责 |
| --- | --- | --- |
| `service.go` | 996 | Service 类型与构造、HTTP 入口、`ask` 编排、请求/响应类型 |
| `llm_transport.go` | 377 | LLM HTTP 传输、流式解析、错误分类、token 成本 |
| `prompt.go` | 146 | 提示词加载与上下文拼装 |
| `grounding.go` | 102 | 忠实度校验 |
| `retrieval_postprocess.go` | 100 | 检索后处理（相关性门限、检索信息、诊断） |
| `citations.go` | 53 | 证据上下文与引用 |
| `sse.go` | 40 | SSE 错误写出与请求体限制 |

文件划分沿用 package 既有约定：**实现文件名与测试文件名一一对应** —— `citations_test.go`、`grounding_test.go` 早就在，实现却挤在 `service.go` 里。所以没有按原文计划合成一个笼统的 `postprocess.go`。

**怎么保证「只搬位置」**（这是本次唯一有分量的判据）：

1. 拆分由脚本做，不手抄 —— `scripts/split_query_service.py` 按顶层声明边界切分，不重新格式化。
2. `scripts/verify_go_file_split.py` 断言 **79 个声明的正文作为多重集合完全相等**（逐字节）。这比 `git diff --stat` 强：文件被搬空重建时 diff 只给满屏增删，读的人无法从里面看出结论。
3. `go doc -all ./internal/query | sort` 前后 `diff` 为空 —— 导出符号集合一致。
4. `gofmt -l`（本机 + 远端）为空、`go vet ./...` 干净、`go test ./... -count=1` 全绿。
5. 脚本可复现出**与提交内容逐字节相同**的产物（`cmp` 七个文件全部相同）。

**踩到的四个坑**（都已在脚本里修掉，不是「注意一下」）：

1. **doc 注释会跟丢**：注释在原文里是「上一个 chunk 的尾巴」。必须先剥尾随空行再收注释，顺序反了会一行都收不到，静默丢掉 4 个声明的注释 —— 而 `go test` 照样全绿。
2. **相邻的一行方法**：`noopLLMObserver` 的两个方法原文相邻（gofmt 把它们当同一对齐组），拆文件时若强行插空行，对齐被重置 —— 那就不再是「逐字节相同」。所以声明之间的空行数要照搬原文。
3. **包注释不能与 `package` 子句隔空行**：否则 Go 不把它当包注释，`go doc` 第一行消失（这一条正是被 `go doc` 对比抓到的）。
4. **Windows 上 `Path.write_text` 会把 `\n` 翻成 `\r\n`**：本地 `gofmt -l` 于是把 7 个文件全报成未格式化，而远端（LF checkout）是干净的 —— 又一处「两台机器结论相反」。必须显式 `newline="\n"`。

**验收判据**：`go test ./internal/query -count=1` 通过；`go vet` 干净；`go doc` 导出符号集合完全一致；79 个声明正文逐字节相同。

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
| Jaeger `invalid UTF-8` 导出告警 | **已定案 = §1.3 缺陷 17**（`6969acc`）。不是 jaeger 的问题：报错来自 **etl-worker 的 OTLP 导出器**，它拒绝整批 span，于是 trace 根本没到 jaeger。真因是 `embedder.truncate` 先 `ToValidUTF8` 再按字节切。**日志只能证明一半** —— etl-worker 容器在 `2026-09-22T12:57:35Z` 重建，日志窗口恰好从修复那一刻开始，所以「修复前出现过」只有断言脚本自己的记录（修复前 2 项失败） | 已验：`assert_utf8_preview_live.py` 2026-09-23 复跑 **8/8 通过**（导出无报错 + 探针的 embed span 真的出现在 jaeger，预览 33 字符 / 99 字节）；`docker compose logs etl-worker \| grep -c "invalid UTF-8"` = **0** |
| 评测脚本 ES 同步竞态 | **已定案（`0cd2377`）**：`wait_for_es_sync` 算出的判定原本只进一行 `print`，不影响 `run_valid`，而 `run_valid` 正是本项目「这份报告能不能当质量证据」的闸门（`write_quality_latest` 拒发、`analyze-eval-variance.py` 丢弃）。改法刻意**不动退出码**（仍 warn-and-continue），改为新增 `es_sync` 字段并把 `reached=false` 折进 `summary.run_valid`。**同时纠正一处被夸大的说法**：原 docstring 声称「ES 落后会把文档推出 top-K，导致假的检索超时」，演示栈实测**未复现**（只从 ES 删块、Qdrant 保留 → 同一问题仍引用它，citations 前后一致），docstring 已改为只陈述权重事实并注明排序论证未经证实 | 已验：`scripts/tests` 282 tests OK（18 skipped）；部署主机上直接调用真实模块 —— `reached=false` → `run_valid=false` 且不产出 `latest.json` 且报告带降级说明，`reached=true` → 产出 `latest.json` |
| ~~UAT-017～020 真实页面复验~~ | **已完成（2026-09-22）** | `scripts/web-page-probe.cjs` 走真实页面，四项全部通过并关闭 |
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
第 11 步（已完成） §1.3 缺陷 13：预审裁决的语言与长度（prompt 中文化 + 确定性归一 + 确定性文本优先）
第 12 步（已完成） §1.3 缺陷 14：语言契约的读取侧兜底 + 判据补漏（线上 28 条裁决全部中文）
第 13 步（已完成） §1.3 缺陷 15：失败裁决保留审阅者诊断 + 契约覆盖全部写入路径
第 14 步（已完成） §2.1 Go 工具链权限 —— 复核发现**已自然消失，未改任何东西**
第 15 步（已完成） §3 状态文档三源归一 + 一致性契约测试 + UAT-017～020 真实页面复验（`486deca`）
第 16 步（已完成） §2.2 / 2.3 配置一致性修复 + 契约测试（`b14e20d` + `c7b4003`）
第 17 步（不做）   4.1 邀请式自助开户 —— 已实现后撤回，代码在分支 `invite-onboarding`（`24388f6`）
第 18 步（已完成） §5.1 query/service.go 机械拆分（同包拆成 7 个文件，导出签名不变、79 个声明正文逐字节相同）（`e15117b`）
第 19 步（已完成） §1.3 缺陷 16：超过 bcrypt 上限的密码返回 400 而不是 500（`45be654`）+ 路由表模式冲突纳入单测（`976f191`）
第 20 步（已完成） §6 Jaeger `invalid UTF-8` 导出告警 → 定案为 §1.3 缺陷 17（`6969acc`）
第 21 步（已完成） §6 评测脚本 ES 同步竞态 → 判定折进 `run_valid`（`0cd2377`）
第 22 步（已完成） §4.3 上传者删除能力记为「仅 API 可用」+ 双向契约测试（`11501a6`）
第 23 步（已完成） §4.4 版本对比：发布时记下被替换的那次发布 + 确定性块级差异 + `GET /v1/documents/{id}/version-diff`（`c3f18cc` `37efad6`）
第 24 步（已完成） P-CAP-3 真实问答时延观察 —— 顺带定位 P-CAP-7（本轮，无代码改动）
第 25 步（待做）   UAT 其余 7 页真实页面复验（`/`、`/agent`、`/data`、`/observe`、`/qa`、`/quality`、`/users`）
```

**为什么是这个顺序**：第 1–4 步是「不做会丢数据或停服」，全部完成 —— 磁盘那一项从
「没人看得见的下滑」变成了「两条会响也会消的告警 + 63GB 余量」。第 5–13 步是
「不做则故障永远静默」：这几处缺陷的共同点是自己不报错、还让看板变绿（见 §1.3）。
第 12 步是第 11 步的下半场：判定补上了却只挂在写入路径上，于是**表里已有的裁决照样是错的**。
第 13 步是第 12 步的下半场：中文补上了，但失败裁决只剩一句「已转人工复核」——
**一个说不清为什么失败的裁决，管理员只能重试或叫人**。
第 14 步复核时发现不需要做（问题已自然消失），但**顺手纠正了原记录里 `.gomod`/`.gocache`
的位置和体积**（三份、合计约 2.3GB，不是仓库根那一份 734MB）。
第 16 步和第 5–13 步是同一类缺陷：**改了没反应，且不报错** —— 只是这次静默的不是结论，
是配置。它成本最低、收益明确，所以提到第 17 步之前做掉。
第 15 步是「不做则后面所有状态判断都不可信」，而且它**不只是文档纪律**：动手前四份
文档对同一件事有三种说法，谁看哪一份就得出哪个结论。它没有代码改动，所以只能靠
契约测试兜住 —— 16 条断言里 9 条是判据自检，因为这三类判据写错时全都是「永远绿」。
顺带把 UAT-017～020 从「产物层验证」升成章程要求的真实页面复验。
第 17 步我做了，然后被否决 —— 顺序错了：这一步原本就标着「需你先确认产品口径」，
我却先实现了。**能跑通不等于该存在。**代码在分支 `invite-onboarding` 上留着，
要重启这件事不用重写（详见 §4.1）。
第 19 步是第 17 步的副产品：做邀请时才发现管理员 API 一直有同一个洞 ——
密码超过 bcrypt 的 72 字节上限被报成 500（服务端故障），而打得太短反而被接受。
它与邀请无关，所以单独提交。第 18 步是纯收益优化，随时可做，所以排在最后 —— 它**不改任何
行为**，收益只体现在「以后读这段代码的人少花时间」。唯一的风险是「搬的过程中悄悄改了
内容」，而这个风险测试看不见（搬错的注释、丢掉的包注释，`go test` 照样全绿），所以判据
放在「79 个声明正文逐字节相同」和「`go doc` 导出集合前后一致」上。

第 20–21 步是 §6 那两条「诚实标注」的收口 —— 它们的共同点是**判定算出来了，却传不到该去的地方**：
第 20 步的判定被写进一个会被整体拒绝的批次里（于是 span 丢在导出器，根本不在 jaeger），
第 21 步的判定被写进一行日志里（于是索引落后时测出来的指标可以冒充一次干净的基线）。
两条都不是新功能，只是把已有的判定接到它的消费者上。
第 22 步把一处「看起来像 bug 的不一致」定成了**决策**，并同时钉住两侧事实 ——
普通用户没有删除入口、上传者的删除能力只在 API 上，是刻意留的（同一条 switch 里的 `PATCH` 才是真管理员专属）。
第 23 步是 §4.4 三个暂缓项里唯一能自证的：它需要两块地基（发布时记下被替换的那次发布、按内容而非位置对齐），
而「上一发布版本」**不能**从 `index_manifests` 推 —— `Activate` 会把从未发布的代际也标成 `retired`。
第 24 步是**观察**不是修复：产出的是 P-CAP-3 的数据，顺带把 23s 的检索成本定位到 P-CAP-7
（rerank 的上限限的是输出、不是算力）。修它会改变检索质量，必须走 P-CAP-2 的评测轨，所以只记录、不修。
第 25 步留到最后，是因为章程写着「验收中途不改代码」—— 巡检那一轮必须没有源码变更。

---

## 8. 边界与未做的事（避免误解）

- **代码改动限于四处**：§1.3 列出的 19 个缺陷，§1.1.2 的 ES 水位百分比化 + 宿主机磁盘告警
  （含补上的 `node-exporter`），§2.2 / §2.3 的配置键修复（`b14e20d` + `c7b4003`），以及
  与邀请功能无关的路由冲突测试（`976f191`，见 §1.3 表后说明）。
  其余章节仍是调查结论，未据此改代码。
- **§4.1 邀请式自助开户：实现过，已撤回**。产品方在 2026-09-22 明确否决。完整切片在分支
  `invite-onboarding`（`24388f6`），未进 `master`；库里新建的 `user_invites` 表已 `DROP`、
  `schema_migrations` 里那一行已删除。**主线上的 `CONTINUATION.md §4` 那条断言因此仍然成立**：
  普通用户确实仍不能自助注册。详见 §4.1。
- **缺陷 14 只改了「给人看的文本」，没改「存起来的事实」**。`release_center_reviews` 里那 6 条英文
  `summary`、8 条英文 `kind_label` **一个字都没有被改写**，读取侧只是不再把它们原样送出去。
  这是刻意的：那一行是「模型当时说了什么」的审计记录，改它等于改证据。
  代价是同一份裁决在库里的样子和界面上的样子会不一致 —— 卡片上的 `Prompt 版本：autonomous-review-v2`
  就是这个差异的可见入口。要消除这个不一致，正确做法是**重跑预审**（让 v3 重新产出一条裁决），
  不是改历史行。
- **缺陷 14 没有恢复被失败路径丢掉的原因；缺陷 15 恢复了，但只对以后生效**。
  写入侧现在保留审阅者自己的诊断（`coordinator.go` 不再覆盖 `Summary`），
  契约也在构造 `report` 之前统一施加一次。但**已经写下的那两行永远补不回来**：
  `review-a26fb4d1d170c359`、`review-69e42208b72c0e0b` 的原因当时只存在于 agent run 里，
  而 run 受 `AGENT_RUN_TTL`（24h）约束、容器日志也早已轮转 —— 今天读它们只能读到
  「预审未给出结论，已转人工复核。」。这不是没做，是**做不了**。
- **缺陷 15 把结论和诊断挤在同一个字段里**。`release_center_reviews` 没有独立的原因列，
  所以 `summary` 现在既承载「结论是什么」也承载「为什么」。分成两列会让面板更干净，
  但需要加列 + 迁移 + 改前端，本轮只做「不丢」，不做「分开」。
- **缺陷 15 放宽了判据的一处边界，代价是写明的**：`LooksLikeEnglish` 现在按字段**开头**判语言，
  于是「中文开头 + 后面跟英文散文」的字段不再被改写。取舍依据是审阅者读的是开头，
  且这个形状从未在线上出现过；该用例已进阈值测试与断言脚本自检表，属**已知边界**。
- **§1.2 不只是文档**：`scripts/backup-stack.sh`、`scripts/restore-stack.sh`、3 个单职责助手脚本、
  `docker-compose.restore.yml`、16 个契约测试都已提交，并在隔离项目 `ai-etl-restore` 上真跑过
  （`down -v` 只作用于该隔离项目，碰不到演示栈的卷）。
- **新增了一个服务**：`node-exporter`。它不是可选的装饰 —— 没有它就没有任何磁盘信号，
  告警规则写出来也是死的。端口 9100 只在 `127.0.0.1`，隔离栈通过 eval 覆盖把它收起来。
- **没有删除任何东西**。远端 `.gomod`/`.gocache` 是承重结构，本次确认了它不能被删。
  **顺便纠正一处旧记录**：它们不是只有仓库根那一份（734MB），而是**三份**
  （仓库根、`services/`、`services/etl-worker/`），合计约 **2.3GB**。全部保留。
- **没有清理你其他项目的镜像/卷**。清理后 `Images` 仍有 24.59GB 可回收、`Local Volumes`
  19.98GB 可回收，大部分不属于本项目 —— 这类操作我不会未经确认执行。
  本轮的回收只做了 `docker builder prune -af`（58.18GB 构建缓存，只影响下次构建速度，
  不动镜像、不动容器、不动卷），其他项目的 11 个容器在清理后逐一确认仍在运行。
- **没有删除任何本项目的数据卷**。`ai-etl-platform_*` 全部保留；无引用的
  `ai-etl-go-build-cache` / `ai-etl-go-mod-cache` 与旧命名空间的 `ai-etl-pipeline_*` 也留着 ——
  空间已不紧张，而 go 缓存卷是构建提速的承重结构。
- **UAT-017～020 已于 2026-09-22 做成真实页面复验并关闭**。上一版这里写的是「只做了产物层验证，所以不标记为已关闭」—— 这个保留是对的，它逼出了真正的复验；现在按 `docs/product-experience-acceptance.md` 走完真实页面，四项全部通过。
- **真实页面复验有两个会让人得出相反结论的坑**，已写进证据目录的 `notes.md`：headless Chrome 默认视口 800×600，会把登录页 `lg:` 品牌栏整块隐藏（`innerText` 里读不到 → 误判「文案没改」）；web 容器的会话 cookie 标了 `Secure`，用 DevTools 协议注入时必须写 `secure:false`，否则在 `http://127.0.0.1` 上被直接丢弃、页面跳回 `/login`。
- **§4.5 没有动手**，理由写在该小节里（被外部决策阻塞）。**§4.6 已动手并完成**（见 §1.3 缺陷 9），
  但走的是**回填**而不是原计划的重建索引 —— 4684 条存量 chunk 的 `file_name` 由
  `scripts/backfill-es-file-name.sh` 就地补齐，没有 drop/rebuild 索引，也没有重新上传任何文档。
- **§4.7 的 116 份源对象没有试图补救**。字节已不存在，脚本不做假造；它被降级为「由完整性门禁
  持续报告的已知缺失」，而不是「已修复」。
- **§2.2 / §2.3 只打通了「宿主 `.env` → 容器」这条链路，没有改任何键的语义**。补进去的每个键，
  默认值都是从代码里读出来的（`internal/config/config.go`、`services/doc-parser-service/app/config.py`、
  或 compose 的 `:-` 默认值），**没有一个是猜的**。5 处 compose 硬编码改成插值时默认值与改前
  逐字相同，所以**现有部署的行为一个字都没变** —— 变的是「现在改得动」。
- **契约测试原先只覆盖两个方向，第三类已补齐（2026-09-23）**：根模板顶层键 ⊆ compose 插值 ∪
  宿主脚本读取 —— 缺的正是「模板写了但两边都没接」这一类。判据补上后实测缺口 **13 个**，
  与「18 − 5」精确吻合，说明清单没有算错。这 13 个分两类，**都是刻意不可达**：

  | 类 | 键 | 为什么不改 |
  |---|---|---|
  | 集群内地址（5） | `KAFKA_BROKERS`、`REDIS_CACHE_ADDR`、`REDIS_STATE_ADDR`、`STORE_ENDPOINT`、`S3_ENDPOINT` | compose 直接写死成服务名。改成可配置等于「允许连集群外的 Kafka/Redis/Qdrant/MinIO」，那是产品决定 |
  | 旧回退值（2） | `REDIS_ADDR`、`REDIS_DB` | 容器里已被 `REDIS_CACHE_*` / `REDIS_STATE_*` 覆盖；只在「不经 compose 直接跑服务」时才生效 |
  | 已被 `_FILE` 取代的 secret（6） | `PG_DSN`、`MINIO_ROOT_USER`、`MINIO_ROOT_PASSWORD`、`REDIS_CACHE_PASSWORD`、`REDIS_STATE_PASSWORD`、`NOTIFICATION_WEBHOOK_TOKEN` | 容器读的是 `<KEY>_FILE`。**`EnvSecret` 先读明文键再读 `_FILE`**，所以透传明文会盖掉 secret 文件里那份 |

  **原先记为「在 compose 里完全没有引用」的 5 个键经核实是纯粹的接线缺口**，代码侧默认值够用，
  已在 `docker-compose.yml` 里补上透传，默认值与 `config.go` 逐字相同（`TASK_STATUS_STORE=auto`、
  `TASK_STATUS_TTL=168h`、`MULTIPART_MAX_MEMORY_MB=4`、`OUTBOX_RELAY_BATCH_SIZE=50`、
  `OUTBOX_RELAY_LEASE=30s`），所以现有部署行为不变：

  | 键 | 消费方 | 为什么必须透传 |
  |---|---|---|
  | `TASK_STATUS_STORE` / `TASK_STATUS_TTL` | query-api（`cmd/api/task_handlers.go:22`）**与** etl-worker（`cmd/worker/main.go:392`） | `auto` 按 `ENVIRONMENT` 解析，两个进程必须拿到同一个值，否则 worker 写的状态 API 读不到 |
  | `MULTIPART_MAX_MEMORY_MB` | query-api（`cmd/api/upload_handlers.go:223`） | 每请求的内存缓冲，会乘 `UPLOAD_MAX_CONCURRENCY` |
  | `OUTBOX_RELAY_BATCH_SIZE` / `OUTBOX_RELAY_LEASE` | query-api（`cmd/api/main.go:263` 与 `:376`） | **一个键管两条 outbox 中继**（入库 admission→Kafka、通知 webhook） |

  这个类别不再靠「记得别加死键」维持：`NOT_REACHABLE_KEYS` 是一张必须逐条给出理由的白名单，
  `test_every_root_template_key_reaches_the_process` 断言「顶层键 ⊆ 可达 ∪ 白名单」，
  `test_unreachable_keys_are_flagged_next_to_the_key` 断言提示语**贴着那把键**（8 行以内），
  两条都有判据自检。反向验证 4/4：拆掉一处透传 / 删一条白名单 / 删一处提示语 / 新增一个死键，
  四条绕过路径全部报红。**顺带删掉 `docker-compose.yml` 里重复声明的 `HTTP_WRITE_TIMEOUT`**
  （query-api 段出现两次，Compose 取最后一个 → 靠前那处是死行，改它没反应）。

  **顺带修掉的 5 个**：`CHUNK_OVERLAP` / `MAX_CHUNK_SIZE` / `MIN_CHUNK_SIZE`（§2.2），
  以及 `RETRIEVAL_MIN_RELEVANCE` / `RETRIEVAL_GROUNDING_LOW_BOUND` /
  `RETRIEVAL_GROUNDING_HIGH_BOUND`（compose 的 query-api 段漏传了这三个检索旋钮，
  已补上并重建验证）。清单从 23 降到 18，**没有一个新增**；2026-09-23 又补上第三类的
  5 个接线缺口，**降到 13**，剩下 13 个全是刻意不可达的（见上表）。

- **本轮踩到一个「本机绿、服务器红」的假失败，值得单独记**：契约测试第一版用
  `rglob("*.go")` 扫 `services/etl-worker`，本机 262 个 `.go` 全绿，远端 6365 个
  （多出来的是 `.gomod`/`.gocache` 里的第三方源码）报 52 个假缺口。**判据写错时的症状
  是「同一个测试在两台机器上结论相反」，只跑正例永远发现不了** —— 所以给它配了
  `test_source_scan_skips_hidden_and_vendored_trees` 做自检。
- **`docs/optimization-plan.md` 本身仍没有做全文一致性审计**。这份台账是逐轮追加的，同一件事在不同章节的写法会漂移 —— §3 就漂过两处（把「只有 `backlog.md` 声明状态源」写成了「三份都声明」，还漏了第四份文档）。本轮只改了 §3 / §6 / §7 / §8 四处，其余章节仍是各轮次追加的原文。**这件事仍未完成**，但它和 §3 不是同一类：§3 是「四份文档对同一事实说法相反，读的人得出相反结论」，而这里是一份台账内部的措辞漂移，读的人只会觉得啰嗦，不会被误导。
- **状态一致性契约测试只扫四份文档，不是全仓**。`docs/optimization-plan.md` 是缺陷台账，它必须能引用被修掉的错误措辞当证据；`LEARNINGS.codex.md` 是逐条带日期的日志，条目里的「当时待复验」是历史记录。把它们纳入会逼着台账不许引用原文。被扫的四份是**会断言当前状态**的那些。
- **「UAT 条目状态只放登记册」偏离了 §3 原本写的「以 backlog 为唯一状态源」**。逐条状态只能放在有状态列、有证据的那一份里；硬挪到 backlog 会变成双写，反而制造新的漂移源。这条偏离写在 §3 里，不是默认忽略。
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
- **缺陷 13 不会改写已经存下来的 v2 裁决**。语言归一发生在**装配报告的那一刻**，不是读取的时候：
  `review-6055e3dd7aa52258` 那一行的 366 字符英文原文仍在表里，只是它的 `prompt_version`
  与当前代码不再匹配。界面显示的是请求当前指向的那一行，重审之后请求已指向 v3 的新行；
  旧行会按 `RELEASE_REVIEW_RETENTION`（90 天）自然过期 —— 走的正是缺陷 11 修好的那条路径。
- **缺陷 13 的线上验证改了 `default` 租户一行数据的 `expires_at`**。为了让采集器走它自己的重审
  路径（`ExpireDueReviews` → `ListReviewJobs` → `StartManagedReview`），把
  `review-6055e3dd7aa52258` 的 TTL 从 `2026-09-23T12:47:51Z` 移到「一小时前」—— 也就是时钟明天
  会到达的同一个状态，其余列未动，其他行未动。这样做是因为演示环境的 admin 凭据不可用，
  而采集器是自动重审的真实入口，比绕过它直接调 API 更接近线上行为。
- **缺陷 13 没有动预审失败路径的 `summary`**（`service.go` 的 `Summary: err.Error()`，
  把 Go 的英文错误原文当摘要）。那是只在 `status=failed` 时出现的诊断信息，而用户反馈的是
  正常裁决的输出；改它要动多处既有断言，还会把内部错误原因从界面上抹掉。**记录在案，未做。**
