# 产品体验问题登记册

最后核验：2026-09-11。

这是产品体验验收的整改主文档。新问题只在这里建单。
验收章程和页面矩阵见 [`../docs/product-experience-acceptance.md`](../docs/product-experience-acceptance.md)。
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

2026-09-09 已完成 D1 复验。旧九条不再处于「待复验」。UAT-001～015 已关闭。

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

## 本轮未建单的缺口

- 机密双审已于 2026-09-09-fix 补测通过，不再作为缺口。
- 文档退役后问答、管理员新建用户、个人空间上传新版本已于 2026-09-09-gap 补测通过。
- 受管替换版本：旧发布在换版过程中仍可问；新版本独立审批后切换，UAT-014 已复验关闭。

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
