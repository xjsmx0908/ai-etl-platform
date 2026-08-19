# Document Parser Service

企业级文档解析与语义切块服务，基于 Python + FastAPI 实现。

## 架构定位

从 Go ETL Worker 中拆分出的独立微服务，专门负责：
- **文档解析**：PDF、DOC、DOCX、TXT、MD 等格式
- **语义切块**：标题感知分块、段落合并、重叠切分
- **计算隔离**：计算密集型任务不影响 Go Worker 的并发性能

## 技术栈

- **FastAPI** - 高性能异步 HTTP 框架
- **PyMuPDF** - PDF 解析（支持文本、图像、元数据）
- **LibreOffice Writer** - 旧版二进制 DOC 转换
- **python-docx** - DOCX 文档解析
- **chardet** - 文本编码自动检测
- **Pydantic** - 数据校验与序列化

## 快速开始

### 1. 本地开发

```bash
# 创建虚拟环境
python -m venv venv
source venv/bin/activate  # Linux/Mac
# 或
venv\Scripts\activate  # Windows

# 安装依赖
pip install -r requirements.txt

# 解析旧版 .doc 时还需 LibreOffice Writer
sudo apt-get install libreoffice-writer-nogui

# 启动服务
python -m app.main

# 访问 API 文档
# http://localhost:8000/docs
```

### 2. Docker 部署

```bash
# 构建镜像
docker build -t doc-parser-service:latest .

# 启动服务
docker run -p 8000:8000 doc-parser-service:latest

# 或使用 docker-compose
docker compose up -d
```

## API 接口

### 1. 上传文件解析

```bash
curl -X POST http://localhost:8000/api/v1/parse \
  -H "X-Internal-Token: <parser-internal-token>" \
  -F "doc_id=test-001" \
  -F "tenant_id=tenant-a" \
  -F "file=@/path/to/document.pdf" \
  -F "permission=read"
```

**响应示例：**

```json
{
  "doc_id": "test-001",
  "tenant_id": "tenant-a",
  "chunks": [
    {
      "chunk_id": "test-001_0000",
      "doc_id": "test-001",
      "tenant_id": "tenant-a",
      "content": "# 第一章\n\n这是文档内容...",
      "index": 0,
      "token_count": 256,
      "permission": "read",
      "file_hash": "sha256..."
    }
  ],
  "total_chunks": 15,
  "parse_time_ms": 1234.56,
  "file_size_bytes": 1048576,
  "status": "success"
}
```

### 2. 健康检查

```bash
curl http://localhost:8000/healthz
```

## 配置

通过环境变量或 `.env` 文件配置：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `HOST` | `0.0.0.0` | 监听地址 |
| `PORT` | `8000` | 监听端口 |
| `MIN_CHUNK_SIZE` | `128` | 最小切块大小（字符） |
| `MAX_CHUNK_SIZE` | `4000` | 最大切块大小（字符） |
| `CHUNK_OVERLAP` | `200` | 切块重叠大小（字符） |
| `MAX_FILE_SIZE_MB` | `100` | 最大文件大小（MB） |
| `LOG_LEVEL` | `INFO` | 日志级别 |
| `CORS_ALLOWED_ORIGINS` | `*` | 逗号分隔的 CORS allowlist（非 dev 不允许 `*`） |
| `INTERNAL_API_TOKEN` | _(空)_ | 服务间鉴权 token（非 dev 环境必填） |
| `INTERNAL_API_TOKEN_FILE` | _(空)_ | token 文件路径（`INTERNAL_API_TOKEN` 为空时生效） |

> 安全说明：`/api/v1/parse-file-path` 已下线，不再支持客户端直接传入服务器文件路径。
> 安全说明：`/api/v1/*` 默认要求 `X-Internal-Token`（非 dev 环境强制）。
> 密钥优先级：`INTERNAL_API_TOKEN` > `INTERNAL_API_TOKEN_FILE`。
> CORS 说明：非 dev 环境启动时会拒绝 `CORS_ALLOWED_ORIGINS=*`。

## 支持的文档格式

| 格式 | 解析器 | 说明 |
|------|--------|------|
| PDF | PyMuPDF | 支持文本提取、分页标记 |
| DOC | LibreOffice + python-docx | 隔离转换为 DOCX，最长 60 秒 |
| DOCX | python-docx | 保留标题层级结构 |
| TXT | chardet | 自动编码检测 |
| MD | chardet | Markdown 标题感知 |
| CSV | chardet | CSV 文本解析 |
| LOG | chardet | 日志文件解析 |

## 切块策略

1. **标题感知**：Markdown H1-H6 标题强制开始新块
2. **段落边界**：空行作为段落分隔
3. **短段落合并**：< MIN_CHUNK_SIZE 的段落下一个合并
4. **超大块切分**：> MAX_CHUNK_SIZE 强制切分 + 重叠

## 性能

- **PDF 解析**：~100页/秒（纯文本）
- **语义切块**：~10MB/秒
- **并发处理**：异步 I/O，支持 100+ 并发请求

## 与 Go ETL Worker 集成

Go Worker 通过 HTTP 调用此服务：

```go
import "ai-etl-pipeline/internal/parser"

// 创建客户端
parserClient := parser.NewClient("http://parser-service:8000")

// 解析文档
chunks, err := parserClient.ParseFile(ctx, task)
```

## 未来扩展

- [ ] OCR 支持（Tesseract）
- [ ] 表格识别（tabula-py）
- [ ] 文档布局分析（LayoutLM）
- [ ] 图像提取与描述
- [ ] 批量处理端点

## 许可证

MIT
