# 产品体验问题登记册

最后核验：2026-09-23。

这是产品体验验收的整改主文档。新问题只在这里建单。
**UAT 条目的状态只在本文件维护**；其他文档只链接，不复制条目状态，也不写任何计数快照。
验收章程和页面矩阵见 [`../docs/product-experience-acceptance.md`](../docs/product-experience-acceptance.md)。
项目级事项状态见 [`../docs/backlog.md`](../docs/backlog.md)。
历史口头问题的原文已写入各条「原始反馈」，不再另存文件。

## 状态

| 状态 | 含义 |
| --- | --- |
| 待复验 | 来自历史反馈，尚未在本轮真实页面确认 |
| 开放 | 已确认仍存在，等待整改 |
| 待产品确认 | 可能是设计取舍，不是单纯缺陷 |
| 已修复 | 页面复验通过 |
| 不修复 | 有明确产品决策，记录理由 |

## 字段

每条问题至少包含：ID、类型、级别、页面、角色、状态、摘要、复现、期望/实际、证据、建议。

类型：美观 UX / 使用逻辑 / 功能缺陷 / 性能体验。级别：S0 / S1 / S2 / S3。
未完成复验的历史项级别标 `待定`。

## 登记索引

| ID | 类型 | 级别 | 页面 | 角色 | 状态 | 摘要 | 原始反馈 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| UAT-001 | 使用逻辑 | S3 | `/data` | user / admin | 已修复 | 数据接入空间/上传下拉意义不清 | 数据接入中的用户上传下拉框我不知道存在有什么意义？ |
| UAT-002 | 使用逻辑 | S3 | `/observe` | 全部 | 已修复 | 可观测只展示 7 个服务，看起来不全 | 系统可观测，服务有7个，是所有的服务吗？我感觉不全，为什么有些服务没有展示？ |
| UAT-003 | 性能体验 | S2 | `/qa` | 全部 | 已修复 | 问答耗时十几秒，体验不可接受 | 用户问答的速度很慢，要十几秒甚至更长的时间，肯定不行的，如何优化？ |
| UAT-004 | 使用逻辑 | S2 | `/qa` | 全部 | 已修复 | 问答过程没有当前阶段反馈 | 用户问答的时候，回答修改为当前阶段正在做什么？就像chatgpt回答时候展示的一样，而不是一直阻塞。 |
| UAT-005 | 功能缺陷 | S2 | `/qa` | 全部 | 已修复 | 问答未使用 SSE 流式输出 | 回答为什么不用sse? |
| UAT-006 | 功能缺陷 | S1 | `/qa` | user / admin | 已修复 | 「日常巡检」文档在问答中找不到 | 我最新上传的文档，“日常巡检”在问答工作台也没找到答案. |
| UAT-007 | 使用逻辑 | S2 | `/quality` | 全部 | 已修复 | 检索质量页展示离线评测报告 | 2026-09-09 复验：`/evals/latest.json` 200，Recall@1 55% |
| UAT-008 | 使用逻辑 | S2 | `/release-center` | admin | 已修复 | Run ID 输入框与空治理字段让人不知道下一步 | Agent发布治理，打开RUN ID输入框存在的意义是什么？点击开始检查，基本上都是责任人不存在，生效日期不存在，存在的意义是什么? |
| UAT-009 | 使用逻辑 | S2 | `/audit` | admin | 已修复 | 审计日志缺少分页和检索 | 审计日志存在的意义是什么？既然存在了，为什么既没有分页，也没有检索。 |
| UAT-010 | 功能缺陷 | S1 | `/qa` | readonly | 已修复 | 只读用户无法问答列表中已发布的公开文档 | 2026-09-09 复验：只读可见空间下拉并命中「赴港流程」 |
| UAT-011 | 性能体验 | S2 | `/qa` | user / admin | 已修复 | 问答 SSE 在最终答案后不结束事件流 | 2026-09-09 复验：`px-user` 巡检问答 3642ms 出现「回答已完成」 |
| UAT-012 | 使用逻辑 | S2 | `/data` | readonly | 已修复 | 只读用户上传按钮可点，提交后才 forbidden | 2026-09-09 复验：按钮/文件选择禁用，页面说明无权限 |
| UAT-013 | 美观 UX | S3 | `/login` | 全部 | 已修复 | 登录失败提示为英文 invalid credentials | 2026-09-09 复验：错误显示「用户名或密码错误」 |
| UAT-014 | 功能缺陷 | S2 | `/documents/[id]` `/qa` | admin | 已修复 | 受管「上传新版本」发布后问答仍引用旧切块 | 2026-09-09 复验：第二管理员批准后问答 120 元，详情不再列出旧切块 |
| UAT-015 | 功能缺陷 | S2 | `/release-center` | admin | 已修复 | 知识发布中心间歇出现红色 Fail to fetch | 2026-09-11 复验：静默轮询无红字；手动刷新显示「同步暂时失败，请稍后重试」 |
| UAT-016 | 美观 UX | S3 | 全站工作台 | 全部 | 已修复 | 页面语言和壳层与产品稿不一致 | 对照产品稿与 2026-09-11 现网截图 |
| UAT-017 | 美观 UX | S3 | `/release-center` | admin | 已修复 | 发布中心仍显示空间 raw id 与英文权限 | 2026-09-11 现网复验：空间 production / 权限 internal |
| UAT-018 | 美观 UX | S3 | `/audit` | admin | 已修复 | 审计操作者列显示 UUID 而不是用户名 | 2026-09-11 现网复验 |
| UAT-019 | 美观 UX | S3 | `/documents` `/documents/[id]` | 全部 | 已修复 | xls/pptx 类型显示为 FILE | 2026-09-11 现网复验 |
| UAT-020 | 美观 UX | S3 | `/login` | 全部 | 已修复 | 登录品牌文案与产品稿不一致 | 2026-09-11 现网复验：仍写可量化 / 语义检索 |
| UAT-021 | 美观 UX | S3 | `/agent` `/release-center` `/documents/[id]` | admin | 已修复 | 责任人显示用户 UUID 而不是人名或部门 | 2026-09-23 复验：`/agent` 与 `/release-center` 显示「责任人：人力资源部」，详情页「责任人」为「财务部」 |
| UAT-022 | 美观 UX | S3 | `/documents` `/documents/[id]` | 全部 | 已修复 | 上传者列显示截断 UUID 而不是用户名 | 2026-09-23 复验：列表三行与详情页「上传者」均为 `demo-user`，页面不再出现 `c189da83…` |
| UAT-023 | 使用逻辑 | S3 | `/documents/[id]` | 全部 | 已修复 | 元数据块直出内部检索字段，与同页中文空间名打架 | 2026-09-23 复验：块头自报「内部检索字段」并说明不代表业务分类，同页「知识空间」仍渲染「用户上传」 |
| UAT-024 | 功能缺陷 | S2 | `/documents/[id]` `/documents` | 全部 | 已修复 | 没有任何发布记录的文档读不出自己的切块：详情页显示「暂无切块」，同一页的搜索框也搜不到它 | 2026-09-23 复验：`/documents/demo-doc-onboarding` 显示「文档切块（3）」并列出正文 |
| UAT-025 | 功能缺陷 | S2 | `/documents/[id]` | 全部 | 已修复 | 已发布文档显示空切块：同一段内容在向量库里有逐字相同的两份拷贝，去重留下不带代际身份那一份 | 2026-09-23 复验：`/documents/ADM-2024-001` 显示「文档切块（1）」并列出正文 |

2026-09-09 已完成 D1 复验，旧九条不再处于「待复验」。UAT-001～016 于 2026-09-09 至 2026-09-11 关闭。UAT-017～020 于 2026-09-22 真实页面复验关闭，逐条证据见各条「复验」。

2026-09-22 起，真实页面复验改用 `scripts/web-page-probe.cjs`（headless Chrome + DevTools 协议）：注入 `px-admin` 会话后抓取页面渲染文本，并强制 1440×900 视口。目的是不让「代码里已经改了」被当成「页面已经通过」——headless 默认 800×600 会把 `lg:` 面板整块隐藏，`innerText` 里读不到，只跑代码会得出相反的结论。

2026-09-23 补走其余页面：`/`（重定向到 `/qa`）、`/agent`、`/data`、`/observe`、`/qa`、`/quality`、`/users`，另加 `/documents` 与 `/documents/[id]`。加上 2026-09-22 的登录、文档、审计、发布中心，章程页面矩阵 11 页都已在真实页面上走过一轮。本轮只巡检，未改产品代码，新开 UAT-021～023。证据 `artifacts/product-experience-acceptance/2026-09-23-uat-rest/`。

2026-09-23 修复 UAT-021 与 UAT-022（`94b9941`、`e9c0857`），并在重建后的 `:3100` 上复验关闭。两条都是「同一件事在库里是 id、在界面上应该是名字」，但根因在两端：UAT-022 是展示层拿不到名字（`uploaded_by` 永远是 UUID），UAT-021 是演示种子把用户 id 写进了人读列。**UAT-022 没有按本条原先的建议做**——原建议让两个页面改用 `actorDisplay(doc.uploaded_by, users)`，而 `GET /v1/users` 是 admin-only（`cmd/api/main.go:508`）、文档列表服务所有角色，照做会让 readonly 与 user 账号拿到 403。改成在服务端随行下发 `uploaded_by_name`。证据 `artifacts/product-experience-acceptance/2026-09-23-uat021-022/`。

2026-09-23 修复 UAT-024（`be4b8c2`），并在重建后的 `:3100` 上复验关闭。这条**不是**用户反馈出来的，是去查「发布中心验收矩阵第 3 项」为什么在审批前读不到 `chunk_ids` 时挖到的：`GET /v1/documents/{docID}/chunks` 与 `GET /v1/documents/search` 两个端点都挂 `publicationrelease.ResolveVisibility`，而那是**检索证据**策略、对没有已发布权威的文档 fail-closed —— 于是「读这份文档自己的内容」被「这块能不能当回答证据」的判据管住了。证据 `artifacts/product-experience-acceptance/2026-09-23-document-content-visibility/`。

2026-09-23 修复 UAT-025（`969433d`），并在重建后的 `:3100` 上复验关闭。它是 UAT-024 修复时顺带查清、当时只登记为「未建单缺口」的那一条（`ADM-2024-001` / `SEC-2024-001` 已发布却显示空切块），本轮立单并修完：真因不在可见性策略（策略 SQL 逐字命中、manifest 健康、Qdrant 里也有身份匹配的块），而在端点数据源的**精确内容去重** —— `QdrantStorer.ListChunksByDoc` 的 `seen` 按归一化内容先到先得，同一段内容的两份逐字相同拷贝里留下了不带身份的那份。与 UAT-024 **方向相反**：一个是「没有发布记录却被当成不可读」，一个是「有发布记录但去重留下了旧块」。证据 `artifacts/product-experience-acceptance/2026-09-23-chunk-identity/`。

三条被查过但**不建单**的观察写在文件末尾「本轮未建单的缺口」，含 `/agent` 与 `/release-center` 上的 `Fetch: net::ERR_ABORTED`：它在 CDP 里是 `canceled=true` + `initiator=script`，失败对象是 Next 对 `/documents/demo-doc-handbook?_rsc=…` 的 RSC 预取，页面既无红色横幅也无失败文案，属噪声而非缺陷。

2026-09-23 修复 UAT-023，并在重建后的 `:3100` 上复验关闭。取的是本条建议里的第一个解法（**块头自报身份**），不是第二个（把空间相关的键译成中文）——`applicable_scope` 取的是空间的 `Kind`，把它译成「生产库」正是这条问题描述的误读方向，等于把误读固化进界面。改动只在 `web/app/(app)/documents/[id]/page.tsx` 的展示块，键值对本身仍是接口字段名原样直出（可核对），**不碰检索过滤逻辑**。证据 `artifacts/product-experience-acceptance/2026-09-23-uat023/`。

续跑完成：不重跑 D0–D4。UAT-013 已修复并复验。证据 `artifacts/product-experience-acceptance/2026-09-09-fix/`。

## 明细

### UAT-001 数据接入下拉意义不清

- 类型：使用逻辑 · 级别：S3 · 状态：已修复
- 页面：`/data` · 角色：user / admin
- 复现：2026-09-09 用 `px-user` / `px-admin` 打开数据接入。
- 期望：一眼能区分个人上传自动发布与受管空间需审批，并知道密级选项的权限后果。
- 实际：下拉文案为「用户上传（个人上传，处理完成自动发布）」「生产库（受管，需审批发布）」；普通用户仅 public/internal，并提示「机密级别需管理员上传」；管理员有 confidential 与治理字段。
- 证据：`artifacts/product-experience-acceptance/2026-09-09/screenshots/PX-03-user.png`、`PX-03-admin.png`；`results.json` 的 UAT-001 / UAT-001-admin。
- 建议：无需再改文案。只读上传控件问题见 UAT-012。

### UAT-002 可观测服务列表看起来不全

- 类型：使用逻辑 · 级别：S3 · 状态：已修复
- 页面：`/observe` · 角色：全部
- 复现：三角色打开系统可观测。
- 期望：用户理解这是 query-api 聚合的主链路依赖，而不是全部容器。
- 实际：服务总数 7、全部在线；页脚写明「Kafka、PostgreSQL、MinIO、监控面板及一次性初始化容器属于基础设施/运维组件，不在此健康探测白名单中」。
- 证据：`PX-08-readonly.png`、`PX-08-user.png`、`PX-08-admin.png`。
- 建议：关闭。

### UAT-003 问答过慢

- 类型：性能体验 · 级别：S2 · 状态：已修复
- 页面：`/qa` · 角色：全部
- 复现：对已发布「日常巡检.txt」提问「机房日常巡检温度是多少？」
- 期望：有可接受等待，或至少能看到卡在检索还是生成。
- 实际：SSE 首包约 140ms，约 1.1s 出现「正在根据证据生成回答」，约 3.2s 出答案；页面总耗时约 2.4s。原「十几秒无反馈」不再成立。流结束后仍挂起见 UAT-011，不与本单混写。
- 证据：`followup.json` 的 `user-xunjian` / `admin-xunjian`；`user-xunjian-qa-final.png`、`admin-xunjian-qa-final.png`。
- 建议：关闭本单；性能残留跟 UAT-011。

### UAT-004 问答缺少阶段反馈

- 类型：使用逻辑 · 级别：S2 · 状态：已修复
- 页面：`/qa` · 角色：全部
- 复现：提问过程中观察阶段条。
- 期望：展示当前正在检索/生成/校验。
- 实际：阶段条含确认范围、检索文档、筛选证据、生成回答、校验引用、整理结果；过程中出现「正在检索相关文档…」「正在根据证据生成回答…」。
- 证据：`user-xunjian-qa-1.png`、`user-xunjian-qa-2.png` 及对应 SSE events。
- 建议：关闭。

### UAT-005 问答未使用 SSE

- 类型：功能缺陷 · 级别：S2 · 状态：已修复
- 页面：`/qa` · 角色：全部
- 复现：提问时检查响应 `Content-Type`。
- 期望：问答走 SSE。
- 实际：`ct=text/event-stream`，status 200，阶段与答案分事件到达。
- 证据：`followup.json` / `complete2.json` 的 qa.sse。
- 建议：关闭。流不结束另见 UAT-011。

### UAT-006 「日常巡检」问答无结果

- 类型：功能缺陷 · 级别：S1 · 状态：已修复
- 页面：`/qa` · 角色：user / admin
- 复现：本轮上传 `日常巡检.txt` 到个人空间并自动发布，选择「用户上传」后提问机房温度。
- 期望：已发布且当前用户可见的文档能被引用。
- 实际：user/admin 均答出「24 摄氏度」，引用 `doc-1788926467846799128`。草稿受管文档提问拒答。选错空间会问不到，属操作问题不是缺陷。
- 证据：`PX-04-user-search-xunjian.png`、`user-xunjian-qa-final.png`、`admin-xunjian-qa-final.png`。
- 建议：关闭。只读角色问答失败是新问题 UAT-010，不是本单回归。

### UAT-007 检索质量页意义不清

- 类型：使用逻辑 · 级别：S2 · 状态：已修复
- 页面：`/quality` · 角色：全部
- 复现：2026-09-09 用 `px-user` 打开 `/quality`。
- 期望：说明这是离线评测回归，不是当次问答评分；报告可读取时展示 Recall 指标。
- 实际：`/evals/latest.json` 200，页面展示离线评测说明、bge-m3 Recall@1 55%。空目录不再覆盖烘焙报告。
- 证据：`artifacts/product-experience-acceptance/2026-09-09-fix/screenshots/UAT-007-user-quality.png`；`uat007.json`。
- 归属：前端 / 运维挂载
- 建议：关闭。

### UAT-008 发布中心 Run ID 与空治理字段

- 类型：使用逻辑 · 级别：S2 · 状态：已修复
- 页面：`/release-center`、`/agent` · 角色：admin
- 复现：管理员打开知识发布中心，查看主流程与缺治理字段引导。
- 期望：主流程展示业务发布状态和审批动作；Run ID 仅作审计；缺字段时说明去文档详情补齐。
- 实际：主列表为需补齐/处理中/待审批/已发布等业务状态，无 Run ID 主输入。缺责任人/生效日期时显示「需补齐」和确定性门禁阻塞。文档详情提示「补齐责任人和生效日期不会自动发布」。`/agent` 同一工作台。
- 证据：`PX-06-admin.png`、`PX-07-admin.png`、`PX-05-admin-gov-saved.png`；`results.json` PX-07-same。
- 建议：关闭。机密双审未跑完是覆盖缺口，不回写本单。

### UAT-009 审计日志无分页无检索

- 类型：使用逻辑 · 级别：S2 · 状态：已修复
- 页面：`/audit` · 角色：admin
- 复现：管理员打开审计日志，关键词 `login` 并翻到第 2 页。
- 期望：能检索并翻页。
- 实际：有存在理由、关键词、操作类型筛选；全量 486 条，login 169 条；分页页码变化，第 2 页记录时间早于第 1 页。非管理员直链禁止。
- 证据：`PX-11-admin.png`、`PX-11-admin-search.png`、`PX-11-admin-page2.png`。
- 建议：关闭。操作者显示 UUID 是抛光项，不单独立项。

### UAT-010 只读用户无法问答可见的已发布公开文档

- 类型：功能缺陷
- 级别：S1
- 状态：已修复
- 页面：`/qa`、`/documents`
- 角色：readonly
- 复现：
  1. 以 `px-readonly` 打开文档管理，可见「赴港流程 .docx」，公开、user-uploads、已发布。
  2. 打开问答工作台，无「用户上传 / 生产库」空间选择。
  3. 提问「赴港流程是什么？」
- 期望：只读用户能对授权范围内已发布公开文档问答，或明确提示当前没有可检索知识空间。
- 实际：问答页无空间可选；SSE 返回「未找到相关文档」，检索 0 段 0 份文档。文档列表与问答检索边界不一致。普通用户/管理员主路径不受阻。
- 证据：`PX-02-readonly.png`、`PX-04-readonly.png`、`readonly-docs-gang.png`、`readonly-gang3-qa-final.png`；`complete2.json` 的 `readonly-gang3`（spaces=[]）。
- 归属：前端 / 检索权限
- 建议：只读账号授予可见空间并显示选择器；或文档列表与检索共用同一授权空间。这是本轮唯一 S1。
- 修复：问答列出授权空间（含单个）；问句匹配已发布文件名后用 ES `doc_id` 召回；已发布但无 generation 的文档允许作为证据。
- 复验：2026-09-09，`px-readonly` 提问「赴港流程是什么？」引用 `赴港流程 .docx`；证据 `artifacts/product-experience-acceptance/2026-09-09-fix/`。

### UAT-011 问答 SSE 最终答案后不结束流

- 类型：性能体验
- 级别：S2
- 状态：已修复
- 页面：`/qa`
- 角色：user / admin
- 复现：
  1. 选择「用户上传」，提问已发布日常巡检温度。
  2. 观察页面总耗时与 EventSource 是否关闭。
- 期望：最终答案和引用到达后结束 SSE，客户端不必空等。
- 实际：页面总耗时约 2.4s 且答案已渲染，但流无结束事件，巡检脚本等到约 70550ms / 70564ms。拒答短问题可在约 1.2s 结束。
- 证据：`followup.json` steps `user-xunjian 70550ms`、`admin-xunjian 70564ms`；同条 qa 的 UI「总耗时 2.405s」。
- 归属：后端 query-api / 前端 EventSource
- 建议：最终事件后关闭流；与 UAT-003 分开，因为用户可见等待已可接受。
- 修复：`querySSE` 收到 `event: done` / `error` 后立即结束并 cancel reader，不等 HTTP body EOF；问答页在 done 态展示「回答已完成」。
- 复验：2026-09-09，`px-user` 提问「机房日常巡检温度是多少？」3642ms 出现「回答已完成」，不再空等约 70s。证据 `uat011.json`、`UAT-011-qa-final.png`。

### UAT-012 只读用户上传按钮未禁用

- 类型：使用逻辑
- 级别：S2
- 状态：已修复
- 页面：`/data`
- 角色：readonly
- 复现：
  1. 只读用户打开数据接入，上传按钮 `disabled=false`。
  2. 选择文件提交。
- 期望：只读角色前端禁用上传，并说明无权限。
- 实际：按钮可点，提交后页面出现 `forbidden`。后端拒绝成立，前端未对齐。
- 证据：`followup.json` `readonly-upload disabled=false`；`readonly-upload-attempt.png`。
- 归属：前端
- 建议：按角色禁用并改文案，不要只靠 API 错误。
- 修复：`canUploadDocuments` 仅 admin/user 可上传；只读禁用文件选择、空间/密级和上传按钮，并展示「没有数据接入权限」。
- 复验：2026-09-09，`px-readonly` 上传按钮 `disabled=true`，文件选择禁用，页面说明无权限，未出现 `forbidden`；`px-user` 选文件后按钮仍可点。证据 `uat012.json`、`UAT-012-readonly-upload.png`。

### UAT-013 登录失败提示为英文

- 类型：美观 UX
- 级别：S3
- 状态：已修复
- 页面：`/login`
- 角色：全部
- 复现：输入错误密码提交。
- 期望：中文错误提示，与登录页其余文案一致。
- 实际：表单显示 `invalid credentials`。密码可见性、空表单禁用、品牌区正常，不因此判 PX-01 失败。
- 证据：`PX-01-login-error.png`。
- 归属：前端
- 建议：本地化错误文案。
- 修复：`localizeLoginError` 将 `invalid credentials` 映射为「用户名或密码错误」；登录页与 BFF 不再透传英文 API 文案。
- 复验：2026-09-09，错密登录 API 401 返回「用户名或密码错误」，页面红字同文案且无 `invalid credentials`。证据 `uat013.json`、`UAT-013-login-error.png`。


### UAT-014 受管上传新版本后问答未切到新内容

- 类型：功能缺陷
- 级别：S2
- 状态：已修复
- 页面：`/documents/[id]`、`/qa`、`/release-center`
- 角色：admin
- 复现：
  1. 管理员批准已预审的受管文档 `managed-with-governance.txt`（`doc-1788934557877171712`）。
  2. 问答「PX-MANAGED-20260909 差旅市内交通补贴标准是多少？」得到 80 元。
  3. 在详情页上传新版本 `managed-with-governance-v2.txt`（120 元）。
  4. 换版过程中再问原问题，仍得到 80 元。
  5. 文档状态变为已发布后，详情同时列出旧切块 80 元与新切块 120 元。
  6. 问「PX-MANAGED-V2-20260909 …」拒答；改问文件名仍答 80 元。
- 期望：受管替换后旧发布在新版本独立审批前继续可问；新版本发布后问答切到新内容，旧切块不再作为回答依据。
- 实际：换版过程中旧版可问，符合辅助闭环前半。新版本发布后旧切块未清理，问答未切到 120 元。
- 证据：`artifacts/product-experience-acceptance/2026-09-09-gap/`（`pend-v1-qa-final.png`、`pend-during-qa-final.png`、`pend-v2-doc-final.png`、`pend-v2b-qa-final.png`、`results.json`）
- 归属：后端发布/替换切块生命周期
- 建议：已修复。已发布文档换版后继续走独立预审/审批；发布时退役旧 generation，详情与问答只保留已发布版本。
- 复验：2026-09-09，`px-admin-2` 批准 `managed-with-governance-v2-full.txt` 后，问答「PX-MANAGED-V2-20260909 …」得到 120 元；详情不再列出 v1 标记。证据 `artifacts/product-experience-acceptance/2026-09-09-gap/`。


### UAT-015 发布中心间歇红色 Fail to fetch

- 类型：功能缺陷 · 级别：S2 · 状态：已修复
- 页面：`/release-center` · 角色：admin
- 复现：管理员打开知识发布中心并停留。页面每 8 秒并行刷新 documents/requests/overview；任一次浏览器或 BFF `fetch` 失败都会把 `Failed to fetch`/`Fail to fetch` 写进红色横幅，且后续成功刷新不会清除。
- 期望：瞬时网络/代理抖动不长期占据红字；自动刷新失败可重试；手动刷新才提示「同步暂时失败，请稍后重试」。
- 实际：query-api 近 4 小时 overview 202 次全是 200，不是业务 5xx。红字来自 `refreshReleaseCenter` 把原始 `Error.message` 直接渲染，且成功路径不清空 `error`。
- 证据：`scripts/tests/test_release_center_fetch_error.py`；`web/app/(app)/agent/page.tsx`；query-api metrics `/v1/release-center/overview` status=200。
- 归属：前端
- 建议：静默轮询忽略瞬时 fetch 失败；成功刷新清掉横幅；BFF 捕获上游 `fetch failed` 返回 503 JSON，避免把连接失败泄漏成浏览器 Failed to fetch。
- 复验：2026-09-11，`px-admin` 打开 `/release-center`。停留约 18s（12 次轮询）无 `Fail to fetch`；中断 overview 后静默轮询仍无红字；手动「刷新状态」显示「同步暂时失败，请稍后重试」，恢复后横幅消失。证据 `artifacts/product-experience-acceptance/2026-09-11-uat015/`。

### UAT-016 工作台页面语言与产品壳层不一致

- 类型：美观 UX
- 级别：S3
- 状态：已修复
- 页面：登录、问答、文档、数据接入、可观测、检索质量、知识发布中心、用户管理、审计日志
- 角色：全部
- 复现：对照产品稿 `zhijing-product.html` 与 `artifacts/product-experience-acceptance/2026-09-11-ux/screenshots/`。
- 期望：统一页头、管理员「管理」分组、业务中文、文件名优先、问答依据默认收起。
- 实际：各页标题样式不统一；问答露出检索指标和引用 score；数据接入阶段写 Kafka/embedding；文档列表以文档 ID 为主列。
- 证据：`artifacts/product-experience-acceptance/2026-09-11-ux/screenshots/`；契约测试 `scripts/tests/test_product_shell.py`。
- 归属：前端
- 建议：按产品稿统一壳层与页面语言，不改发布策略/预审。
- 修复：新增 `PageHeader`；侧栏「管理」分组；问答依据折叠、引用文件名优先；数据接入中文阶段与文档编号；文档列表文件名和空间名优先；发布中心共用页头；文档详情页使用同一页头、空间名和中文治理状态。
- 复验：2026-09-11，契约测试覆盖壳层文案。同日 `px-admin` 打开重建后的 `http://localhost:3100`：侧栏「管理」、PageHeader、数据接入中文阶段、文档文件名/空间名、发布中心新页头均已上现网。证据 `artifacts/product-experience-acceptance/2026-09-11-ux-retest/`。剩余密度/ID 见 UAT-017～020。

### UAT-017 发布中心空间与权限仍是内部 ID

- 类型：美观 UX
- 级别：S3
- 状态：已修复
- 页面：`/release-center`
- 角色：admin
- 复现：2026-09-11 用 `px-admin` 打开知识发布中心。
- 期望：空间显示名称（如生产库），权限显示公开/内部/机密。
- 实际：列表与详情写 `production` / `enterprise-demo`，权限写 `internal`。页头与业务状态筛选已中文。
- 证据：`artifacts/product-experience-acceptance/2026-09-11-ux-retest/screenshots/release.png`
- 归属：前端
- 建议：发布中心列表/详情复用空间名与权限中文标签，不改审批/预审逻辑。
- 修复：发布中心列表与详情改用 `spaceLabel` / `permissionLabel`（`web/lib/docDisplay.ts`）。
- 复验：2026-09-22，`px-admin` 打开 `http://localhost:3100/release-center`。列表显示「生产库 · 1 项阻塞」「演示知识库 · 3 项阻塞」；点开 `gap-managed-d1.txt` 后详情显示「空间：生产库」「权限：内部」。全页无 `production` / `enterprise-demo` / `internal`。证据 `artifacts/product-experience-acceptance/2026-09-22-uat017-020/`。

### UAT-018 审计操作者显示 UUID

- 类型：美观 UX
- 级别：S3
- 状态：已修复
- 页面：`/audit`
- 角色：admin
- 复现：打开审计日志第一页。
- 期望：操作者显示用户名，必要时才露出 ID。
- 实际：操作者列为 `96ca72d2-682d-4e60-b3b1-32d133401b23管理员`。
- 证据：`artifacts/product-experience-acceptance/2026-09-11-ux-retest/screenshots/audit.png`
- 归属：前端
- 建议：优先渲染 username，UUID 放到详情展开。
- 修复：操作者列改用 `actorDisplay`（`web/lib/docDisplay.ts`），命中用户列表时渲染 username，未命中时截断 UUID 并保留完整值作 `title`。
- 复验：2026-09-22，`px-admin` 打开 `/audit`（共 729 条）。操作者列显示 `admin`、`px-admin`、`interviewer`、`eval-readonly`、`user` 等用户名加角色徽章；登录失败行显示 `—`；全页无裸 UUID。证据 `artifacts/product-experience-acceptance/2026-09-22-uat017-020/`。

### UAT-019 表格把 xls/pptx 显示成 FILE

- 类型：美观 UX
- 级别：S3
- 状态：已修复
- 页面：`/documents`、`/documents/[id]`
- 角色：全部
- 复现：文档列表第一页有 `.xls` / `.pptx`。
- 期望：类型列显示 XLS / PPTX。
- 实际：类型徽章为 `FILE`。文件名和空间名已正确。
- 证据：`artifacts/product-experience-acceptance/2026-09-11-ux-retest/screenshots/documents.png`
- 归属：前端
- 建议：`getFileTypeMeta` 补 xls/xlsx/pptx。
- 修复：`getFileTypeMeta` 的类型表补齐 `xls` / `xlsx` / `xlsm` / `ppt` / `pptx` / `pptm`（`web/lib/docDisplay.ts`），`FILE` 只作兜底。
- 复验：2026-09-22，`px-admin` 打开 `/documents`（共 116 篇）。`个人所得税专项附加扣除信息表.xls` 与 `附件一_在职人员年休假信息收集表(1).xls` 徽章为 `XLS`，`1_财务制度培训2019.10.28.pptx` 为 `PPTX`，`阿里商旅试行管理通知.doc` 为 `DOC`，无 `FILE` 兜底。证据 `artifacts/product-experience-acceptance/2026-09-22-uat017-020/`。

### UAT-020 登录品牌文案与产品稿不一致

- 类型：美观 UX
- 级别：S3
- 状态：已修复
- 页面：`/login`
- 角色：全部
- 复现：打开登录页。
- 期望：产品稿为「可检索、可问答、可发布的知识资产」；卖点「多路召回 · 只回答已发布知识」「权限隔离 · 引用可追溯」。
- 实际：仍是「可量化的知识资产」「多路召回 · 语义检索 / 忠实度校验 · 权限隔离」。表单与错误中文正常。
- 证据：`artifacts/product-experience-acceptance/2026-09-11-ux-retest/screenshots/login.png`
- 归属：前端
- 建议：只改登录品牌区文案，不改认证流程。
- 修复：品牌区标题与两条卖点改为产品稿文案（`web/app/login/page.tsx`）。
- 复验：2026-09-22，1440×900 打开 `/login`。品牌区为「让文档沉淀为 可检索、可问答、可发布的知识资产」，卖点为「多路召回 · 只回答已发布知识」「权限隔离 · 引用可追溯」；全页无「可量化的知识资产」「语义检索」「忠实度校验」。证据 `artifacts/product-experience-acceptance/2026-09-22-uat017-020/`。

### UAT-021 责任人显示用户 UUID

- 类型：美观 UX · 级别：S3 · 状态：已修复
- 页面：`/agent`、`/release-center`、`/documents/[id]` · 角色：admin
- 复现：
  1. 以 `demo-admin` 打开 `/agent`（兼容路由，与发布中心同一工作台）。
  2. 在「统一业务记录」里选中 `员工手册.md`，看详情卡「责任人」。
  3. 再打开 `/documents/demo-doc-handbook`，看治理区「责任人」。
- 期望：显示人读的责任人。`default` 租户同一位置渲染的是「体验验收员」（2026-09-23 实测），历史记录里还有「小卡拉米」「财务部」。
- 实际：三处都显示 `4f60802f-6de7-4652-b59a-27109d85c912`。库里 `demo` 租户三份文档的 `owner` 全是这串（= `demo-admin` 的用户 id）；`default` 租户同一个字段是人读文本（实测渲染「体验验收员」，另有文档为空串、页面显示「未填写」）。`owner` 的定义在 `internal/migrations/0004_document_governance.up.sql:20` 写的是「Accountable owner (business role or user)」，与记录上传者的 `uploaded_by` 明确区分，是人读文本，不是用户 id 列。
- 证据：`artifacts/product-experience-acceptance/2026-09-23-uat-rest/probe-7pages.json`（`/agent` 渲染文本含该 UUID）、`probe-docdetail.json`（`/documents/demo-doc-handbook`）、`probe-agent-network-demo.txt` 与 `probe-agent-network-default.txt`（同一条路径在两个租户下的对照）
- 归属：后端（演示种子）
- 建议：`cmd/api/demo_showcase.go` 的 `INSERT INTO documents (… owner …)` 不再把 `adminID` 传进 `owner`，改传可读责任人（人名或部门）。**展示层修不通用**：`owner` 是自由文本（`cmd/api/upload_handlers.go:864` 直接取表单值），不能假定它一定是 UUID 再映射回用户名——那是把种子的错变成展示层的猜测。
- 修复：按上述建议改种子，三份文档的 `owner` 改为 `人力资源部` / `人力资源部` / `财务部`，`adminID` 挪到 `$14` 只用于收敛判断（`94b9941`）。**收敛分支是必须的**：`ON CONFLICT ... DO UPDATE` 只在 `WHERE` 里列出的列变动时才执行，没有 `owner=CASE WHEN documents.owner=$14 THEN EXCLUDED.owner ELSE documents.owner END` 加对应 `WHERE` 子句，修复就只对全新数据库生效，已播种的部署会永远渲染那个 UUID。`CASE` 同时保证不覆盖管理员手工编辑过的责任人。
- 复验：2026-09-23，重建 `query-api` 后（重启即重新播种）同一批已存在的行从 `4f60802f-6de7-4652-b59a-27109d85c912` 收敛为部门名；1440×900 打开 `/agent` 与 `/release-center` 渲染「责任人：人力资源部」，`/documents/demo-doc-payroll` 治理区「责任人」渲染「财务部」，四页均不再出现 `4f60802f…`。另做两条运行时反向验证：把一份文档的 `owner` 手工改成「手工指定的责任人」后重启，值**未**被覆盖（ELSE 分支成立）；改回 UUID 后重启，收敛回「人力资源部」（并还原演示状态）。证据 `artifacts/product-experience-acceptance/2026-09-23-uat021-022/`。

### UAT-022 上传者列显示截断 UUID

- 类型：美观 UX · 级别：S3 · 状态：已修复
- 页面：`/documents`、`/documents/[id]` · 角色：全部
- 复现：
  1. 以 `demo-admin` 打开 `/documents`：三行的「上传者」都是 `c189da83…`。
  2. 打开 `/documents/demo-doc-handbook`：「上传者」同为 `c189da83…`。
  3. 换 `px-admin`（default 租户）打开 `/documents`：每一行都是 `96ca72d2…` 或 `62da5a2e…`。
- 期望：显示用户名。判据与 UAT-018（审计操作者列）相同。
- 实际：两个页面都走 `formatUploader(doc.uploaded_by)`（`web/lib/docDisplay.ts:48`）。该函数只对**字面量字符串** `demo-user` / `demo-admin` 返回「演示用户」「演示管理员」，其余 UUID 一律截断成前 8 位加省略号。而 `uploaded_by` 永远来自 `auth.GetUserID()`（`cmd/api/upload_handlers.go:488`、`:503`、`:543`），落库即用户 UUID —— 两个中文名分支在真实数据下**不可达**，所有行都走截断分支。`/audit` 用的是另一个函数 `actorDisplay(id, users)`，带用户目录，所以 UAT-018 只把它修好了。
- 证据：`artifacts/product-experience-acceptance/2026-09-23-uat-rest/probe-7pages.json`、`probe-docdetail.json`、`probe-default-documents.json`
- 归属：前端
- 建议：`/documents` 与 `/documents/[id]` 改用 `actorDisplay(doc.uploaded_by, users)`（`listUsers` 在 `/audit` 已经这样用过），删掉 `formatUploader` 里不可达的两个字面量分支。契约测试补一条与 `scripts/tests/test_product_shell.py::test_audit_prefers_username_over_raw_uuid` 同形的断言——只有 `/audit` 被测试钉住，正是这两个页面漂移出去的原因。
- **建议已被推翻（2026-09-23 修复时发现）**：`GET /v1/users` 挂在 `requireScopes(auth.ScopeAdmin)` 下（`cmd/api/main.go:508`），是 admin-only；而 `GET /v1/documents` 服务所有角色。照原建议让两个页面调 `listUsers`，readonly 与 user 账号会拿到 403。`/audit` 能用是因为审计页本来就只给管理员看。
- 修复：改为在**服务端**解析。`internal/docstore/docstore.go` 的 `documentColumns` 加子查询取 `users.username`，随行下发 `uploaded_by_name`（`Get` / `GetByHash` / `List` 都经这一个列清单与 `scanDocument`，改一处即覆盖）；`cmd/api/doc_handlers.go` 的 `documentView` 加 `uploaded_by_name`；前端 `formatUploader(id, name)` 两参，删掉 `demo-user` / `demo-admin` 两个字面量分支（`git show 3a16e7f` 确认它们从引入那天起就不可能命中）。两个写法是刻意的：比较的是 uuid 那一侧（`u.id::text = documents.uploaded_by`），反过来 `documents.uploaded_by::uuid` 遇到非 UUID 值会抛错把整个列表打挂，而这种值线上确实存在（`gov-admin`、`interviewer`、`ingestion-capacity-user`、`demo-user`）；子查询用 `COALESCE(..., '')`，因为 `scanDocument` 读普通 `string`，解析不出名字时前端回退到截断 UUID 而不是空白。
- 复验：2026-09-23，重建 `query-api` 与 `web` 后，`GET /v1/documents` 返回 3/3 行带 `uploaded_by_name`（`c189da83-…` → `demo-user`）；1440×900 打开 `/documents`，表头「上传者」列三行均为 `demo-user`；`/documents/demo-doc-payroll` 的「上传者」同为 `demo-user`；两个页面均不再出现 `c189da83`。反向验证 7 个变异（去掉子查询、把 cast 换到 TEXT 列、前端改回单参、helper 忽略 `name`、种子写回 `adminID`、去掉收敛分支、`owner` 字面量改回 UUID）各自对应的测试全部变红。证据 `artifacts/product-experience-acceptance/2026-09-23-uat021-022/`。

### UAT-023 文档详情元数据块直出内部检索字段

- 类型：使用逻辑 · 级别：S3 · 状态：已修复
- 页面：`/documents/[id]` · 角色：全部
- 复现：打开 `/documents/doc-1789044422928850395`（default 租户，用户上传空间），看治理区下方的「元数据」。
- 期望：要么说明这是给运维看的原始检索字段，要么把空间相关的键译成与同页「知识空间」一致的中文。
- 实际：显示 `applicable_scope: production`、`knowledge_base_id: user-uploads`、`knowledge_space_id: user-uploads`，而同一页的「知识空间」写「用户上传」。`applicable_scope` 取的是空间的 `Kind`（`cmd/api/upload_handlers.go:305`），检索期当过滤 token 用（`internal/query/scope.go:43`），不是业务词汇。两块并排时读的人会以为这份文档属于「生产库」。
- 证据（改前）：`artifacts/product-experience-acceptance/2026-09-23-uat-rest/probe-default-documents.json`
- 归属：产品 / 前端
- 建议：先确认这个块给谁看。给运维看就补一句「内部检索字段」的说明；给业务用户看就把 `knowledge_space_id` / `knowledge_base_id` 走 `spaceLabel`，把 `applicable_scope` 折进「系统详情」。渲染处是 `web/app/(app)/documents/[id]/page.tsx:371-382`，纯展示改动，不碰检索过滤逻辑。
- **采用第一个解法**：块头 `元数据` 改为 `内部检索字段`，并加一句「以下为检索与过滤使用的系统字段，键名是接口字段名，不代表业务分类（例如 `applicable_scope` 取的是知识空间的类型标识）」。**刻意不采用第二个**——把 `applicable_scope: production` 译成「生产库」正是本条问题描述的误读方向，那样做等于把误读固化进界面；键值对本身仍原样直出接口字段名，可核对。判据来自章程第 91 行：本条属「使用逻辑」，而该行把「术语和业务对不上」明确写在这一类里。
- 修复：`web/app/(app)/documents/[id]/page.tsx:371-382` 的展示块（`internal` 侧零改动）。回归测试 `scripts/tests/test_product_shell.py::test_document_detail_labels_metadata_as_internal_retrieval_fields` 四条断言，其中最后一条是双向断言（新标题在、旧的 `>元数据</dt>` 已清），防「只加不改」。
- 反向验证：基线正例先通过，三个变异各自让上面那条测试报红——块头改回裸标题「元数据」（原缺陷）、只留标题去掉说明、**把内部 token 说成业务归属**（这条问题警告的误读方向）；每条跑完按 sha256 逐字节复原。第三个变异是关键对照：若测试只钉「标题改了」，把 token 说成业务归属照样能过。
- 复验：2026-09-23，重建 `web` 后以 default 租户 `px-admin` 打开 `/documents/doc-1789044422928850395`（1440×900）：渲染「内部检索字段」+ 上述说明 + 三个内部键，同页「知识空间」仍渲染「用户上传」，`consoleErrors` 为空。证据 `artifacts/product-experience-acceptance/2026-09-23-uat023/`（`page-after-docdetail.json` 已按章程从第一个 `文档切块（` 起截断，另记 `full_text_sha256`）。
- 未做：没有把 `knowledge_space_id` / `knowledge_base_id` 走 `spaceLabel`，也没有把 `applicable_scope` 折进「系统详情」。若产品方要的是第二个解法，需按新口径重做。


### UAT-024 没有发布记录的文档读不出自己的切块

- 类型：功能缺陷 · 级别：S2 · 状态：已修复
- 页面：`/documents/[id]`、`/documents` · 角色：全部
- 复现：
  1. 打开 `/documents/demo-doc-onboarding`（demo 租户，`publication_status='draft'`，Qdrant 里 3 块）。
  2. 页面「文档切块（0）」+「暂无切块」，看起来这份文档没有内容。
  3. 在 `/documents` 的搜索框里搜「试用期」（只出现在这份文档里）——列表里明明显示着它，却搜不到。
- 期望：一份文档自己的切块应当能读；空态要区分「真的没有块」和「有块但不给看」。
- 实际：`GET /v1/documents/{docID}/chunks` 返回 `{"total":0}`，`GET /v1/documents/search?q=试用期` 返回空。两份未发布演示文档（`demo-doc-onboarding`、`demo-doc-payroll`）都是 0，而同为 3 块的已发布 `demo-doc-handbook` 是 3。default 租户另有 `doc-1788350741430636808`（0 而非 24）、`HR-2024-003`（0 而非 1）。
- 真因：两个端点都挂 `publicationrelease.ResolveVisibility`，那是**检索证据**策略 —— 只有「已 resolved 且 manifest 健康的已发布 release」或「legacy `publication_status='published'` 且引用不带代际身份」才可见，其余 fail-closed。可这两个端点问的是另一个问题：「这份文档现在有哪些块」。新上传文档的 `publication_status` 默认 `'draft'`（`internal/docstore/docstore.go` 的 `COALESCE(NULLIF($24,''),'draft')`），于是**任何尚未发布的文档**都被判成不可读。引入该过滤的 `9915e38` 提交说明写的是「document detail only lists the published release, so QA switches to the new content」，意图是**在多个代际之间选已发布的那一代**，不是「未发布文档不可读」；当时 `docsearch_test.go` 用的是**桩**可见性解析器（`visible: {"gen-published": true}`），只验了「被取代的代际被过滤」，这个退化对回归完全隐形。
- 修复：在 `internal/publicationrelease/postgres.go` 增加 `ResolveDocumentContentVisibility`，与 `ResolveVisibility` **只在「从未发布过的文档」上不同** —— 没有已发布代际就没有需要隐藏的旧代际，全部可见；有已发布代际的文档保持严格匹配，所以「替换发布后详情页切到新代际」的行为不变。两个端点改用新的 `documentContentVisibility` 接口（**刻意不复用 `retrieval.VisibilityResolver`**，注释写明原因）。判据必须是「identity 非空」而不是「`document_releases` 里有行」——那张表对每份入库文档都有行，`published_version_id` 保持 NULL 直到真正发布。
- 反向验证：基线正例先通过，三个变异（改回不可见 / 有 release 行即视为有代际 / 放宽已发布匹配）各自让新测试报红，每次按 sha256 复原。
- 权限：**没有跟着放宽**。两个端点各自保留独立的权限过滤（`permissionAllowed` + `ListChunksByDoc(allowed)` / `AllowedPermissionsForRole`）。线上对照：同一关键词「员工手册」，admin 看到 `demo-doc-payroll`（confidential），user 看不到；user 直读 payroll 的 chunks 得 `404`。
- 证据：`artifacts/product-experience-acceptance/2026-09-23-document-content-visibility/`
- 归属：后端
- 建议：已修复。顺带查清的**另一件事**（`ADM-2024-001` / `SEC-2024-001` 已发布却显示空切块）已立为 UAT-025 并修复（`969433d`）。

### UAT-025 已发布文档显示空切块（去重留下了不带代际身份的那一份）

- 类型：功能缺陷 · 级别：S2 · 状态：已修复
- 页面：`/documents/[id]` · 角色：全部
- 复现：
  1. 打开 `/documents/ADM-2024-001`（default 租户，`publication_status='published'`，有健康的已发布代际）。
  2. 页面「文档切块（0）」+「暂无切块」。
  3. 同一文档在 Qdrant `documents-v2` 里有 2 个点、ES 里有 2 块，其中一块的 `document_version_id`/`generation_id` 与已发布代际完全匹配。
- 期望：已发布文档应当列出属于当前已发布代际的块。
- 实际：`GET /v1/documents/{docID}/chunks` 返回 `{"total":0}`。`SEC-2024-001` 同样（Qdrant 3 点、ES 3 块，改前也是 0）；对照 `FIN-2025-001` 正常返回 1。
- 真因：**不在可见性策略**。策略 SQL 逐字复制出来跑是命中的，`index_manifests` 的 `state=active` / `expected_count` / `qdrant_count` / `elasticsearch_count` 三者相等、两个摘要都对得上，Qdrant 里也确实有一块身份完全匹配 —— 排除到最后一层才发现问题在端点数据源的**精确内容去重**：`QdrantStorer.ListChunksByDoc`（`internal/store/store.go`）的 `seen` 以归一化内容为键、`if _, ok := seen[key]; !ok` **先到先得**。而同一份文档确实可能同时存在两份内容逐字相同的块：一份历史写入（两个身份字段全空）、一份来自取代它的受管代际 —— `ADM-2024-001` 那 2 点的 `norm_sha256` **完全相同**（`474a312b86f23982`），scroll 顺序把空身份那块排在前面。`seen` 只决定**展示哪一份**，而发布可见性按 identity 精确匹配，空身份那份永远匹配不上，于是整份文档被判空。
- 修复：`seen` 改记位置（`map[string]int`），遇到重复内容时若已在位的那份不带身份、而新来的带身份，则替换掉它 —— 结果与存储返回顺序无关。判据 `carriesIdentity` 要求 `DocumentVersionID` 与 `GenerationID` **都非空**，与发布策略同源；只要求一半会让「半个身份」的块顶掉真正匹配的那份。同段另一支去重 `removeContainedAdjacentChunks`（包含式重叠，阈值 0.65，用来挡历史 parser 的重叠块）不受影响，注释原先把两者混在了一句里，已拆开写清。
- 反向验证：基线正例先通过，四个变异各自让新测试报红 —— 改回「先到先得」（原缺陷）、判据反转、判据退化成只看 `GenerationID`、metadata 不再写进块；每条跑完按 sha256 逐字节复原。
- 线上断言（同一脚本跑改前改后两态，9 项）：`ADM-2024-001` 0 → **1**、`SEC-2024-001` 0 → **1**；`doc-1788350741430636808` 24、`HR-2024-003` 1、`FIN-2025-001` 1、`doc-1788338541964346783` 0、demo 三份展示文档 3/3/3 **全部不变**。把「改前预期」跑在改后代码上，只有 `ADM-2024-001` 与 `SEC-2024-001` 两项 MISMATCH。
- 权限：未改动，与 UAT-024 一样只放宽了内容可见性，权限过滤仍是独立的一层。
- 证据：`artifacts/product-experience-acceptance/2026-09-23-chunk-identity/`（`assert-before.txt` / `assert-after.txt` / `assert-before-expectations-on-after-code.txt` / `reverse-verify-chunk-identity.txt` / `page-after-adm.json`）
- 归属：后端
- 建议：已修复。

## 本轮未建单的缺口

- 机密双审已于 2026-09-09-fix 补测通过，不再作为缺口。
- 文档退役后问答、管理员新建用户、个人空间上传新版本已于 2026-09-09-gap 补测通过。
- 受管替换版本：旧发布在换版过程中仍可问；新版本独立审批后切换，UAT-014 已复验关闭。
- 2026-09-22 真实页面复验只覆盖 UAT-017～020 对应的登录、文档、审计、发布中心四页。其余页面本次未走，不代表已通过。
- 2026-09-23 已补走其余页面（`/`→`/qa`、`/agent`、`/data`、`/observe`、`/qa`、`/quality`、`/users`，加 `/documents`、`/documents/[id]`）。七页全部返回 200 且有渲染文本，`consoleErrors` 除下述噪声外为空。
- `/agent` 与 `/release-center` 的 `Fetch: net::ERR_ABORTED` **不是缺陷**。CDP 事件显示 `canceled=true`、`initiator=script`，失败对象是 Next 对 `/documents/demo-doc-handbook?_rsc=…` 的 RSC 预取（`?_rsc=` 是预取的标记），即浏览器侧主动取消的投机请求，不是服务端失败（服务端失败会带状态码而不是 `ERR_ABORTED`）。同一形状在两次重载与 `/release-center` 基线上各复现一次，页面无 `role="alert"` / 红色横幅 / 失败文案，且列表与详情数据完整渲染。默认视口下不设 1440×900 也复现同样结果，与视口无关。判据与原始日志见证据目录 `notes.md`。
- **`/qa` 提交问题后那条 `Fetch: net::ERR_ABORTED` 也不是缺陷，但成因与上一条不同**（2026-09-24 判）。把 `requestId` 还原成 URL 后，失败对象是 **`POST /api/query` 本身**，栈是 `onSubmit` → `stream`：`web/lib/querySSE.ts` 的 `consumeQuerySSEStream` 收到终止事件后**主动** `reader.cancel()`，刻意不在 `done` 之后空等服务端关连接 —— 这个意图由契约测试钉着（`scripts/tests/test_query_sse_client.py::test_query_sse_returns_after_done_or_error_without_waiting_for_body_eof`）。它是**竞态**：`cancel()` 赢在服务端 EOF 之前 Chrome 就记一条，服务端先关好就不记；本轮两次运行各占一边（一次 `loadingFailed: 1`、一次 `0`），两次的页面结果完全一致（终态「回答已完成」、拒答卡片与解释都在、无失败横幅），既不丢数据也不改结论。**同一症状在不同路由上成因不同**：`?_rsc=` 是预取噪声，`/api/query` 是 SSE 客户端按设计提前取消。证据 `artifacts/product-experience-acceptance/2026-09-24-qa-refusal/`。
- **证据目录的引用能不能核到，取决于 `.gitignore`。** `/artifacts/product-experience-acceptance/` 是 Git 忽略的（`.gitignore:51`），所以本文件里的证据路径只在**产出它的那份工作副本**上存在；换一份检出就核不到，而引用本身不会报错。2026-09-23 复核时 8 个被引用的目录里有 7 个在本机不存在（`2026-09-09*`、`2026-09-11*`、`2026-09-22-uat017-020`、`2026-09-23-uat-rest`），其中 `2026-09-23-uat-rest` 的内容还在 `.workbuddy-ai/tmp/evidence-2026-09-23/`，已补回该目录名；另有两处把 `2026-09-23-chunk-identity` 误写成 `2026-09-23-chunks`（已更正）。**这一条不是产品缺陷**，是台账的自证条件：引用的目录名必须与产出时一致，且要接受「旧目录可能已不在」。若要根治，得把证据目录纳入版本库或改用可寻址的存储，本文件不擅自改这条约定。
- `/quality` 自报「最近一次真实评测 2026-09-11」，是页面主动显示的日期，未判为缺陷。
- ~~`scripts/web-page-probe.cjs --login` 在 Chrome profile 已存在会话时会失败（登录页直接跳走，找不到体验登录按钮）~~ → **2026-09-23 已修**（工具缺陷，不是产品缺陷）。真因：`/login` 对已登录访客直接跳走（实测落到 `/qa`），所以体验登录按钮**永远不会进 DOM**，`--login` 只能抛错。修法：按「`/login` 把访客送走了」这一条**证据**判定复用会话（`loginOutcome = already-authenticated`，输出里记 `loginRedirectTo`），只在**还停在 `/login` 且按钮缺失**时才抛错；两个判据由 `--self-check` **执行**验证（8 条），不是读源码。**顺带补上一个静默通过的口子**：受保护路由落回 `/login` 时页面照样有渲染文本，原来的「渲染为空才算坏」判据会把它读成通过 —— 现在 `bouncedToLogin` 折进退出码（无会话访问 `/documents` 实测 exit 1 + `bounced to /login -- no usable session`）。证据 `artifacts/product-experience-acceptance/2026-09-23-probe-session/`（同一 profile 连跑两次：`clicked` → `already-authenticated`，两次 exit 0）。
- ~~**已发布文档在详情页显示空切块，而 Qdrant 里有当前代际的块**~~（2026-09-23 查 UAT-024 时顺带查清）**已于同日立为 UAT-025 并修复（`969433d`）**。原先记在这里的理由是「修法需要先定『同一文档跨代际内容相同的块该保留哪一块』，会动到去重策略本身」——实际动手时发现判据不用新定：与发布策略同源即可（`document_version_id` 与 `generation_id` **都非空**的那一份胜出），改动只有一处替换条件加一个 helper。原先的诊断里有一处需要更正：`ListChunksByDoc` 里的去重其实有**两支**，出问题的是 `seen` 的**精确内容**去重（先到先得），而注释里拿来解释它的「挡历史 parser 包含式重叠」是另一支 `removeContainedAdjacentChunks` 的职责。详见 UAT-025 明细。

## 新增问题模板

复制后追加到索引表和明细，ID 使用下一个 `UAT-00N`。

```md
### UAT-0xx 标题

- 类型：美观 UX | 使用逻辑 | 功能缺陷 | 性能体验
- 级别：S0 | S1 | S2 | S3
- 状态：开放 | 待产品确认
- 页面：
- 角色：
- 复现：
  1.
  2.
- 期望：
- 实际：
- 证据：`artifacts/product-experience-acceptance/YYYY-MM-DD/`
- 归属：产品 / 前端 / 后端
- 建议：
```
