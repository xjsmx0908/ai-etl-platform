# 未完成事项（唯一出处）

> **这个文件为什么存在**：2026-09-26，用户问「还有哪些没有做的工作」，我凭回忆列了一份，
> 把**我该做而没做完**的东西（对账 CI 从没跑过、切块改动没跑评测轨）归进了「可做可不做」那一堆。
> 用户反问「你为什么会分类错呢」。
>
> 根因有两条：① 我用「要不要用户参与」这个维度分堆，却给其中一堆起了带价值判断的名字 ——
> 维度是「谁来做」，名字承诺的是「重不重要」；② **清单是凭回忆列的，而回忆会漏** ——
> 台账里早就写着「未端到端验证」「没跑过」，机械扫描就能拿到，我却没去扫。
>
> 所以这份清单**不是写给人凭印象读的**：它由 `scripts/extract-open-items.py` 与台账对账，
> 台账里每一处欠账词都必须在下面被分类（登记成 OPEN 条目，或写进「不是欠账」并给理由）。
> 分类从此是数据，不是判断。

**`owner` 的四个取值就是这份清单的全部意义**：

| owner | 含义 | 数量 |
| --- | --- | --- |
| `agent` | 我该做、没做完 —— **不许再叫它「可做可不做」** | 见 `--check` 输出 |
| `user` | 需要用户点头或定口径 | 同上 |
| `external` | 外部依赖（业务签字 / IdP 选型 / 预算） | 同上 |
| `declined` | 已明确决定不做（**必须带理由**） | 同上 |

运行 `python3 scripts/extract-open-items.py --check` 会打印四个计数。**`agent` 那个数就是
被「可做可不做」掩盖掉的量。**

---

## agent：我该做、没做完

### OPEN-01 对账 CI job `index-consistency` 没在真正的 GitHub runner 上绿过
- owner: agent
- 判据: 一次真实的 GitHub Actions 运行里该 job 为绿。绿 → 加进 `required-checks`；红 → 如实写清卡在哪一步
- 出处: `docs/optimization-plan.md` §8；`.github/workflows/ci.yml` 的 `index-consistency`
- 锚: 没有在真正的 GitHub runner 上绿过一次
- 锚: 所以它还没有进 `required-checks`，等一次真绿
- 锚: 它**每次 push 都在跑**，只是**从来没绿过**
- 锚: 在 Docker Hub 上没了
- 锚: 拿 `documents-v2` 当 Qdrant 集合名
- 已修掉的两条（2026-09-26）: ① job 猜栈的身份（管理员 `admin`、集合 `documents-v2`）—— 改成读
  `run-evals.py` 写出的 `stack-descriptor.json`；② `minio/minio` 被 Docker Hub 删除导致起栈就失败 ——
  改成 `chainguard/minio` 按摘要固定。隔离栈上实测 `documents checked: 47`、零发现、退出码 0
- 为什么它排第一: 整条跨层对账的意义就是「下一次漂移会被自动发现」，而这句话**没有证据** ——
  这恰恰是本轮一路在修的病（判定存在，却传不到会触发动作的地方），收口上又犯了一次。
  而且它比原先以为的更糟：不是「没跑过」，是**一直在跑、一直红、没人看**（不在 `required-checks` 里）

### OPEN-21 对象存储依赖的是已归档的 MinIO 社区版
- owner: user
- 判据: 定一个口径 —— 继续用 Chainguard 构建的 MinIO（能跑、但上游社区分发已结束、不会有新的安全修复），
  或换成还在维护的 S3 实现（RustFS / Garage / SeaweedFS）。换的话要动 `docker-compose.yml` 的 minio 服务并
  跑一次真实摄入/检索回归
- 出处: `docs/optimization-plan.md` §8；`docker-compose.yml` 的 minio 服务
- 锚: 对象存储依赖的是已归档的 MinIO 社区版
- 锚: 换不换对象存储是一次决定
- 为什么归 `user`: 这是**依赖选型**（供应链 + 长期维护），不是修一个坏掉的东西 —— 现在这套能跑，
  换与不换的代价不对称，该由用户定口径

### OPEN-22 `run-evals.py --keep-services` 的「再跑一次」走不通
- owner: agent
- 判据: 让「同一个栈上再跑一次」能成功（例如按租户区分身份，或让运行器复用上一次的租户），
  或明确写清它不支持并让调用方知道
- 出处: `docs/optimization-plan.md` §8；`scripts/run-evals.py` 的 `create_eval_user` / `login_eval_user`；
  迁移 `0001_init.up.sql` 的 `users_username_key`
- 锚: `run-evals.py --keep-services` 的「再跑一次」走不通
- 锚: 身份按用户名全局唯一：users_username_key
- 实测: 第二次播种在第 1 篇文档失败（`status: duplicate`，`duplicate_of` 指向上一次运行租户里的文档）；
  `eval-user` / `eval-readonly` 在 `users` 表里只存在一行，`users_username_key` 是 `lower(username)` 上的唯一索引

### OPEN-02 切块改动（缺陷 21 / 22）未走 P-CAP-2 检索评测轨
- owner: agent
- 判据: 跑一次 P-CAP-2（60 题 retrieval-only）并记录 Recall 数字；或明确论证它对本改动不适用
- 出处: `docs/optimization-plan.md` §8
- 锚: P-CAP-2 检索评测轨没有重跑
- 锚: 本轮**没有重跑**，原因是仓库规定演示栈在跑时不得另起 eval Compose
- 锚: 所以这一项如实记为未验证。

### OPEN-03 对账基线的 13 条「已归因的先决条件」
- owner: agent
- 判据: `expires`（2026-12-31）到期前，每条要么修掉、要么重新论证；过期后脚本会重新报出来
- 出处: `scripts/index-consistency-baseline.json`；`docs/optimization-plan.md` §8
- 锚: | 已知未修 | **跨层索引一致性的剩余项**
- 锚: | `citation_unverifiable` |
- 锚: **剩余 5 份 `citation_unverifiable` 已逐条归因
- 注意: 这份清单**本身是债，不是豁免**；`expires` 就是逼这件事发生的机制

### OPEN-04 9 条 `repair_attempts` 已达上限的 failed 行永久不可认领
- owner: agent
- 判据: 决定是抬高 `maxRepairs`、还是把这 9 行显式标成「不可修复」并停止把它们算作待修
- 出处: `docs/optimization-plan.md` §8
- 锚: `ClaimFailedRepairs` 的条件是 `state='failed' AND repair_attempts < $1`
- 影响: 9 份文档的失败世代**永远不会被重放环认领**，且 `*_bad` 计数恒为 0 是假象

### OPEN-05 弃答路径丢 `prompt_version` 与 `token_usage`
- owner: agent
- 判据: 先定「弃答算不算一次生成」；定了就把字段补上
- 出处: `docs/optimization-plan.md` §8 末段
- 锚: 弃答路径仍然丢 `prompt_version`（空串）与 `token_usage`

### OPEN-06 告警「到不到人」没有证据
- owner: agent
- 判据: 下一次磁盘越 flood_stage 或死信非零时**观察告警是否到人**；或在告警链路上加一条「有人收到」的确认
- 出处: `docs/optimization-plan.md` §8「更正（2026-09-24 晚…）」
- 锚: 信号有没有人看，仍未被验证
- 说明: 90 条死信躺了三周，而日志、指标、告警规则、通知链路**全都在**。事故证据已被重启与保留期
  覆盖，所以「为什么没到人」我查不到 —— 这条记的是**已知的未知**

### OPEN-07 Alertmanager 自身指标未被 Prometheus 抓取
- owner: agent
- 判据: 加一个 `job_name: alertmanager` 抓取任务；或在文档里写清为什么不加
- 出处: `infrastructure/prometheus.yml`（有 `alertmanagers:` 但 `job_name` 里没有 alertmanager）
- 台账未提及: 台账里没写过这一条 —— 它是查 `prometheus.yml` 时才发现的，所以机械扫描覆盖不到

---

## user：需要用户点头或定口径

### OPEN-08 宿主机磁盘没有安全余量
- owner: user
- 判据: 用户决定是否动那约 34GB 未引用镜像（含别人刻意留的回滚点）与 19.98GB 未引用卷
- 出处: `docs/optimization-plan.md` §8 磁盘条目
- 锚: | 已知未修 | **跨层索引一致性的剩余项**
- 现状: 95%（11GB 可用）vs flood_stage 97%；**一次重建约 5GB**；安全可回收的只剩构建缓存私有 1.5GB
- 为什么不是「agent 自己能做」: 能立刻腾出空间的对象**全是别人的**（`openclaw` 旧 tag、`golang:*` 等共享基镜像、
  别的项目的 bot 栈卷），动它们要人点头

### OPEN-09 `release_center_reviews` 的两处历史遗留
- owner: user
- 判据: 决定要不要给表加独立的原因列；决定要不要改写表里那 6 条英文 `summary` / 8 条 `kind_label`
- 出处: `docs/optimization-plan.md` §1.3 缺陷 15 后 / §8
- 锚: `release_center_reviews` 没有独立的原因列
- 锚: 缺陷 14 只改了「给人看的文本」，没改「存起来的事实」
- 锚: 8 条英文 `kind_label` **一个字都没有被改写**
- 锚: 这一半没做（见上面那条）
- 说明: 读取侧已经不再把它们原样送出去，所以**表里的英文只影响直接读库的人**；加列要配迁移，属产品决定

### OPEN-10 弃答率告警阈值 0.9
- owner: user
- 判据: 用线上真实弃答率定一个数，替换现在拍出来的 0.9
- 出处: `docs/optimization-plan.md` §1.3 缺陷 27；`infrastructure/rules/` 的 `QueryRefusalRateNearTotal`
- 锚: QueryRefusalRateNearTotal

### OPEN-11 `refusalMarkers` 仍是宽子串匹配
- owner: user
- 判据: 定口径 —— 哪些模型输出算「拒答」；定了再收窄
- 出处: `docs/optimization-plan.md` §8；`internal/query/service.go`
- 锚: `refusalMarkers` 仍是宽子串匹配
- 影响: 模型写出的**部分可答**内容只要含「无法回答」「参考文档不足」这类子串，就会被整段丢掉换成拒答句

### OPEN-12 `RETRIEVAL_MIN_RELEVANCE` 默认 0
- owner: user
- 判据: 定一个相关度地板（或明确论证 0 是对的），让「没有证据」这一支真的走得到
- 出处: `docs/optimization-plan.md` §8；`internal/config/config.go`
- 锚: `RETRIEVAL_MIN_RELEVANCE=0`（`config.go:392` 的默认值

### OPEN-13 `documents.chunks_done` / `chunks_total` 按哪一代算
- owner: user
- 判据: 定这两列的语义（当前是「最后一次入库」，而已发布代际可能不是最后一次）；定了才谈改
- 出处: `docs/optimization-plan.md` §8
- 锚: **仍有一个没定的口径**：这两个数按**哪一代**算

---

## external：外部依赖

### OPEN-14 backlog 里 5 项 blocked / pending decision
- owner: external
- 判据: 依赖项到位后各自完成 —— P1.9 要业务责任人签名 artifact；P2.3 要批准保留期与部署责任；
  P2.5-PROD/STAGE 要选 IdP 与 connector owner；P2.6 要批准 SLO/RPO/RTO/预算
- 出处: `docs/backlog.md` Active work；`docs/optimization-plan.md` §4.5
- 锚: 5 项全部 blocked / pending decision
- 锚: | 企业身份生产 | 全部 blocked 在外部决策
- 锚: **§4.5 没有动手**，理由写在该小节里
- 说明: **这些不是技术债，是决策债** —— 决策到位前动手只会白做

---

## declined：已明确决定不做（每条都必须带理由）

### OPEN-15 政策库 / 跨文档冲突审查
- owner: declined
- 理由: 它们缺的是**领域规则**而不是算法，且当前预审（R1/R6）已能覆盖发布资格预审的真实需求 —— 等有真实业务诉求再立项
- 判据: 出现真实业务诉求时重新立项
- 出处: `docs/optimization-plan.md` §4.4
- 锚: ### 4.4 合规审查深度 —— 三个暂缓项里
- 锚: 政策库、跨文档冲突审查；后两项仍未动
- 锚: 政策库、跨文档冲突审查。它们缺的是**领域规则**

### OPEN-16 备份 cron 不打开 `REQUIRE_INTEGRITY`
- owner: declined
- 理由: §4.7 的 116 份缺失是**已接受的残留**（源对象已不存在，不可恢复），打开它只会让日常备份永远报错
- 判据: 若将来那批残留被清掉，重新评估
- 出处: `docs/optimization-plan.md` §4.7 / §8
- 锚: **刻意没有改的事**：**没有让 cron 打开 `REQUIRE_INTEGRITY`**
- 锚: 缺陷 12 没有改变日常备份策略

### OPEN-17 邀请式自助开户（§4.1）
- owner: declined
- 理由: 2026-09-22 **产品方明确否决** ——「邀请式自助开户，没必要做吧」
- 判据: 产品方改变决定时重启；切片在分支 `invite-onboarding`（`24388f6`，未合并），不用重写
- 出处: `docs/optimization-plan.md` §4.1
- 锚: 2026-09-22 产品方明确否决

### OPEN-18 死信重放
- owner: declined
- 理由: `RetryMessage` 是**整块 chunk 的替换式写入**，重放等于用故障时刻的旧副本替换当前索引；
  真缺口的正确修复是重跑摄入（对账已能发现并调度）
- 判据: 若将来出现「非替换式」的失败类型，重新评估
- 出处: `docs/optimization-plan.md` §1.3 缺陷 28 / §8
- 锚: **不做重放**，理由写在 §1.3 第 28 行的处置列

### OPEN-19 多轮问答
- owner: declined
- 理由: 单轮边界是**刻意**的（ADR 0012）；指代上文的问句走 `context_required` 弃答而不是猜
- 判据: 产品方要求多轮时重启，ADR 0012 已写清实现顺序与体量
- 出处: `docs/adr/0012-single-turn-qa-boundary.md`
- 台账未提及: 台账只写了「若真要做多轮」的实现顺序，没有把它登记为欠账 —— 它是**设计边界**，不是欠账

### OPEN-20 `applicable_scope` 译成中文
- owner: declined
- 理由: `applicable_scope` 取的是空间的 `Kind`，把它译成「生产库」**正是那条问题描述的误读方向** ——
  等于把误读固化进界面；已改成块头自报身份（UAT-023 第一解法）
- 判据: 不改；若将来要改，先改 `Kind` 的语义
- 出处: `issues/findings-register.md` UAT-023
- 台账未提及: 台账里没有这一条 —— 它是 UAT-023 修法取舍的结论，登记在这里以免被当成「漏了」

---

## 含欠账词但已确认不是欠账

> 台账里出现欠账词的地方不都是欠账：**缺陷表里描述「当初怎么坏的」、反向验证里说「还原后」、
> 已完成项里说「当时没做、后来做了」**，都会命中。这一节把它们显式判掉 ——
> 这样「台账里每一处欠账词都有人表过态」就成了可机械检查的事。

| 锚（台账原文片段） | 为什么不是欠账 |
| --- | --- |
| 凡未实测的均明确标注为「未验证」 | 编制方式说明，不是在说有什么没做 |
| 「会自己恢复」这件事没做到 | §0 开头在复述初稿结论，不是当前状态 |
| 补第三十处 —— 缺陷 28 的另一半 | 修订说明行；它提到的 job 未端到端在 §8 另有 OPEN-01 登记 |
| 30 个功能缺陷（第 23–25 个 | 「已修复项」汇总行，记的是做完的事 |
| 30 个在跑的容器与它们引用的镜像一个没动 | 磁盘回收的执行记录（证明没伤到别人），不是欠账 |
| `failed` 之后没有任何自动流程会认领它们 | 缺陷 8 的成因叙述，该缺陷已修 |
| 预审的瞬时失败被永久缓存，重试变成复读 | 缺陷 5 的表行，已修 |
| `state='failed'` 的生成没有任何自愈路径 | 缺陷 8 的表行，已修 |
| 读一份文档**自己的**内容时套用了 | 缺陷 18 的表行，已修 |
| 同一段内容在向量库里有两份**逐字相同**的拷贝时 | 缺陷 19 的表行，已修 |
| 弃答只有一句话，四种原因共用 | 缺陷 26 的表行，已修 |
| 因为修复尚未提交），两项守卫测试都失败 | 反向验证过程叙述（说明为什么用快照而不是 git checkout） |
| 没有改预审失败路径的 `summary` | 缺陷 13 当时的取舍；后来被缺陷 15 做掉了 |
| 失败裁决的 `summary` 仍然只是「预审未给出结论 | 缺陷 15 的起点叙述，该缺陷已修 |
| 这一条是缺陷 14 的**下半场** | 缺陷 15 的定位叙述 |
| 处置：本项从待办划掉，没有改任何东西 | 已关闭的项（Go 工具链权限已自然消失） |
| §4「UAT-001～009 仍待真实页面复验」 | §3 漂移对照表的一行，漂移已在状态文档归一那轮修掉 |
| 派生状态被复制。条目状态只有登记册有状态列 | §3 的成因分析，状态源已归一 |
| 两个坑记在证据目录的 `notes.md` | 真实页面复验的经验记录，不是欠账 |
| 文档对同一件事有三种说法 | §7 在解释为什么第 15 步排在那里，该步已完成 |
| 第 23 步是 §4.4 三个暂缓项里唯一能自证的 | 第 23 步已完成；剩下两项已登记为 OPEN-15 |
| 队列要能说出每一条为什么留下，磁盘要能说出每一 GB 为什么没动 | 第 35 步的原则陈述，不是在说有什么没做 |
| ## 8. 边界与未做的事（避免误解） | 章节标题 |
| 上一版把它记成「未修」，本轮连同 | 缺陷 27 的更正叙述，该缺陷已修 |
| 同一轮的最后一条 `ChunksProcessed` 没有接线，而是删掉了 | 已处置（判据是「这个事实已经有主」） |
| 已转人工复核。」。这不是没做 | 台账自己已经明确判为「不是欠账」 |
| 真实页面复验有两个会让人得出相反结论的坑 | 经验记录，已写进证据目录 |
| §2.2 / §2.3 只打通了「宿主 `.env` → 容器」这条链路 | 刻意不改键的语义（那是另一件事） |
| 缺的正是「模板写了但两边都没接」这一类 | 配置键缺口已修（13 个），并加了契约测试 |
| 全文一致性审计已于 2026-09-23 完成 | 已完成 |
| **没有做数据迁移**：把旧 run 迁移到新身份需要 | 刻意不做：旧 run 会按 TTL 自然过期，迁移会动审计记录 |
| 它们的 `expires_at = 2026-09-28`，窗口还没到 | 说明保留期窗口，不是在说欠账 |
| 其余列未动，其他行未动。这样做是因为 | 数据改动范围说明（证明只动了该动的那一列） |
| 缺陷 13 当时没有动预审失败路径的 `summary` | 历史取舍，后被缺陷 15 做掉 |
| **这个「未做」后来被缺陷 15 做掉了** | 已做 |
| 我凭回忆列了一份，把**我该做而没做完**的东西 | §8 在解释这份清单**为什么存在**，不是在登记新的欠账 |
| 归进了「可做可不做」那一堆 | 同上：复述 2026-09-26 那次分类错误本身 |
| 机械扫描就能拿到，我却没去扫 | 同上：说明机械扫描这个机制要解决的问题 |
| `owner` 只有四个取值：**`agent`（我该做、没做完） | §8 在解释这份清单的字段定义 |
| 第 34 步的 CI 部分当时记为「未端到端验证」 | 引用的是一句**被推翻的旧判断**（§7 第 34 步的说明 2026-09-26 已改），用来说明判断怎么变的；真欠账是 §8 与 OPEN-01 |
| 它每次 push 都在跑，只是从来没绿过 | 复述被推翻的旧结论，不是新欠账；真欠账是 OPEN-01 |
