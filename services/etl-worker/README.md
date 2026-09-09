# ETL Worker & Query API

Go 模块，包含 Kafka ETL Worker（`cmd/worker`）和 Query API（`cmd/api`）。可复用代码在 `internal/`。

当前架构、环境变量、部署和验收以仓库根目录为准：

- [`README.md`](../../README.md)
- [`docs/architecture-design-document.md`](../../docs/architecture-design-document.md)
- [`docs/technical-design-document.md`](../../docs/technical-design-document.md)
- [`.env.example`](../../.env.example)

不要使用本模块内的独立 Compose；全栈编排在仓库根目录 `docker-compose.yml`。

## 常用命令

```bash
make build
make test
make lint
```

Docker 镜像：

- `Dockerfile`：ETL Worker
- `Dockerfile.api`：Query API
