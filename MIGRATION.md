# AI-ETL Platform Monorepo 重构指南

## 📋 当前状态

已创建顶层目录结构和统一配置文件：
```
D:\projects\ai-etl-platform\
├── docker-compose.yml          ✅ 已创建
├── README.md                   ✅ 已创建
├── .env.example                ✅ 已创建
└── scripts/                    ✅ 已创建
    ├── start.sh
    └── stop.sh
```

## 🔄 需要手动执行的步骤

### 步骤 1：移动现有项目到 services/ 目录

```powershell
# 在 PowerShell 中执行
cd D:\projects

# 创建 services 目录
mkdir ai-etl-platform\services

# 移动 ai-etl-pipeline 为 etl-worker
Move-Item ai-etl-pipeline ai-etl-platform\services\etl-worker

# 移动 doc-parser-service
Move-Item doc-parser-service ai-etl-platform\services\doc-parser-service
```

### 步骤 2：删除旧的独立 docker-compose.yml

```powershell
# 删除 etl-worker 中的旧 docker-compose.yml（已被顶层的统一配置替代）
Remove-Item ai-etl-platform\services\etl-worker\docker-compose.yml
```

### 步骤 3：验证目录结构

```
D:\projects\ai-etl-platform\
├── docker-compose.yml
├── README.md
├── .env.example
├── scripts/
│   ├── start.sh
│   └── stop.sh
└── services/
    ├── etl-worker/              # 原 ai-etl-pipeline
    │   ├── cmd/
    │   ├── internal/
    │   ├── Dockerfile
    │   ├── Dockerfile.api
    │   ├── Makefile
    │   └── go.mod
    └── doc-parser-service/      # Python Parser Service
        ├── app/
        ├── Dockerfile
        ├── docker-compose.yml   # 保留（用于独立测试）
        └── requirements.txt
```

### 步骤 4：初始化 Git 仓库

```powershell
cd D:\projects\ai-etl-platform

# 初始化 Git
git init

# 创建 .gitignore
@"
__pycache__/
*.pyc
*.pyo
.env
.venv/
venv/
*.egg-info/
dist/
build/
.pytest_cache/
*.egg
.DS_Store

# Go
bin/
*.exe

# IDE
.vscode/
.idea/
"@ | Out-File -FilePath .gitignore -Encoding utf8

# 添加所有文件
git add .

# 提交
git commit -m "refactor: restructure as monorepo

- Move etl-worker and parser-service under services/
- Add unified docker-compose.yml
- Add start/stop scripts
- Update documentation"

# 添加远程仓库（如果需要）
git remote add origin git@github.com:xjsmx0908/ai-etl-platform.git
git branch -M main
git push -u origin main
```

## 🚀 启动验证

```powershell
# 进入项目目录
cd D:\projects\ai-etl-platform

# 复制环境变量
cp .env.example .env

# 启动所有服务
docker compose up -d

# 查看状态
docker compose ps

# 查看日志
docker compose logs -f
```

## 📊 架构优势

### 之前（独立项目）
```
D:\projects\
├── ai-etl-pipeline\          ❌ 独立的 docker-compose.yml
│   └── 引用 ../doc-parser-service
└── doc-parser-service\       ❌ 独立的 Git 仓库
```

**问题：**
- 跨目录引用脆弱
- 版本管理分散
- 部署复杂

### 现在（Monorepo）
```
D:\projects\ai-etl-platform\  ✅ 统一的 Git 仓库
├── docker-compose.yml        ✅ 统一编排
└── services/
    ├── etl-worker/           ✅ 清晰的边界
    └── doc-parser-service/   ✅ 独立可测试
```

**优势：**
- ✅ 统一版本管理
- ✅ 一键启动所有服务
- ✅ 清晰的职责边界
- ✅ 便于 CI/CD

## ⚠️ 注意事项

1. **doc-parser-service 的 docker-compose.yml 保留**
   - 用于独立开发和测试
   - 顶层的 docker-compose.yml 用于完整部署

2. **Go 服务的 docker-compose.yml 已删除**
   - 已被顶层的统一配置替代
   - 如需独立测试 Go 服务，可以单独创建

3. **环境变量统一管理**
   - 使用顶层的 `.env` 文件
   - 各服务通过 docker-compose.yml 注入

## 📝 下一步

1. ✅ 创建 Monorepo 结构（本指南）
2. ⏳ 移动文件到新结构
3. ⏳ 测试启动所有服务
4. ⏳ 推送代码到 GitHub
5. ⏳ 配置 CI/CD（GitHub Actions）
6. ⏳ 添加集成测试
