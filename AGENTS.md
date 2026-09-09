# 仓库指南

## 回答风格

默认使用中文并保持简洁：

- 先给结论，再给必要依据。
- 简单问题用 1～3 句话回答；复杂问题最多列出 3～5 个要点。
- 不重复用户问题，不展开无关背景。
- 不输出内部思考过程，只提供结论、关键依据和下一步。
- 代码任务只汇报改动、验证结果和剩余风险；用户明确要求时再展开细节。
- 存在多个方案时直接推荐一个，并用一句话说明原因。

## 项目结构与模块组织
本仓库是由 Go 和 Python 服务组成的 AI ETL/RAG 平台。`services/etl-worker/` 是 Kafka ETL Worker 和 Query API 的 Go 模块；入口位于 `cmd/worker` 和 `cmd/api`，可复用代码位于 `internal/`。`services/doc-parser-service/` 是 Python FastAPI Parser Service，应用代码位于 `app/`，测试位于 `tests/`。`services/reranker-service/` 是可选的 FastAPI Cross-Encoder Reranker，通过 Docker Compose 的 `rerank` profile 启用。共享编排和运维资源位于根目录：`docker-compose.yml`、`infrastructure/`、`deploy/`、`scripts/`、`docs/` 和 `secrets/examples/`。

## 构建、测试与开发命令
- `docker compose up -d`：启动完整本地服务栈。
- `docker compose ps` / `docker compose logs -f`：检查运行中的服务。
- `bash scripts/e2e-smoke.sh`：运行上传、解析、向量化、存储和查询的端到端冒烟流程。
- `python3 scripts/run-evals.py`：运行确定性 RAG 回归检查。
- `uv run --with-requirements scripts/requirements-test.txt python -m unittest discover -s scripts/tests -p 'test_*.py' -q`：在隔离 Python 环境中运行评测、治理和证据结构契约测试。
- `cd services/etl-worker && make build`：构建两个 Go 二进制程序。
- `cd services/etl-worker && make test`：运行 `internal/...` 的 Go 竞态测试和覆盖率测试。
- `cd services/doc-parser-service && pip install -r requirements.txt && pytest -q`：安装 Parser 依赖并运行 Python 测试。
- `cd services/reranker-service && pip install -r requirements-test.txt && RERANKER_BACKEND=lexical RERANKER_LOAD_ON_STARTUP=false pytest -q`：不下载模型，运行轻量级 Reranker 测试。

## 编码风格与命名约定
Go 代码使用 `gofmt`，包名保持简短、小写并聚焦领域。Go 测试放在被测包旁边，文件名使用 `_test.go`。Python 代码遵循 PEP 8：四空格缩进、函数和模块使用 `snake_case`；涉及请求或响应结构时使用带类型标注的 Pydantic 模型。配置通过环境变量驱动，新增设置同步写入 `.env.example` 文件。

## 测试约定
行为发生变化时必须新增或更新测试。Go 覆盖率由 `make test` 收集；CI 还会运行 `go vet` 和 `go test ./... -count=1`。Python 测试在 `services/doc-parser-service` 和 `services/reranker-service` 中使用 `pytest`。外部系统优先使用确定性 fixture 和 mock；`scripts/e2e-smoke.sh` 仅用于全栈验证。

Knowledge Release Center 变更必须通过功能验收，不能仅以集成演练替代。每次修改发布中心策略、Agent 预审、发布流程或 Web 界面后，都要运行 `docs/release-center-functional-acceptance.md` 中的矩阵。矩阵必须覆盖普通单管理员审批、机密/高风险双管理员审批、Agent 成功、Agent 错误和显式失败状态、确定性阻断、过期 exact-candidate 拒绝，以及幂等/冲突决策。仅构建成功或 happy path 测试通过不代表验收完成。

产品体验巡检或 Web 工作台交互变更必须执行 `docs/product-experience-acceptance.md`。美观、使用逻辑和缺陷只记入 `issues/findings-register.md`，不能用隔离栈绿报代替真实页面结论；验收中途不改代码。

## 提交与 Pull Request 约定
Git 历史使用 `feat:`、`fix:`、`chore:` 等 Conventional Commit 前缀。每个提交保持单一主题，并说明用户可见或运维层面的影响。Pull Request 应包含简短摘要、适用时关联 issue、执行过的验证命令，以及 API/运维行为变更所需的截图或日志。分支保护要求至少一次审批，并在合并前通过 `Required Checks` CI 门禁。

## 自动提交与推送
- 默认在任务完整结束后创建 Git 提交并推送到配置的远程仓库；只有用户明确要求不提交或不推送时才跳过。
- 提交前运行受影响的测试、构建、Lint 和 `git diff --check`；验证失败时不得提交。
- 任务开始时用 `git status --short` 记录基线，保留已有修改，只暂存本任务变更的文件。
- 禁止使用 `git add -A` 或 `git add .`；不得暂存密钥、生成物或无关修改。
- 使用 Conventional Commit 消息，例如 `feat: add agent pre-review validation`。
- 将新提交推送到其上游远程分支；禁止强制推送、amend、reset 或删除已有提交。
- 如果分支没有上游，在确认远程仓库和分支名后使用 `git push -u origin <branch>`。
- 如果冲突、认证失败或变更归属不明确导致无法安全推送，立即停止并报告确切原因。
- 交付时报告提交哈希、远程分支和执行过的验证命令。

## 安全与配置提示
禁止提交真实凭据。使用 `.env.example` 和 `secrets/examples/` 作为模板，将本地密钥保存在被忽略的文件中。开发环境之外的 Parser endpoint 要求 `X-Internal-Token`；修改请求流程时必须保留租户、权限、JWT 和 CORS 检查。
