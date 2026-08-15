# ============================================================
# 多阶段构建 - AI ETL Pipeline Worker
# ============================================================

# --- 阶段 1: 编译 ---
FROM golang:1.25-alpine AS builder

WORKDIR /app

# 先复制依赖文件（利用 Docker 层缓存）
COPY go.mod go.sum ./
RUN go mod download

# 复制源码并编译 Worker
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w -X main.version=${VERSION:-dev} -X main.buildTime=$(date -u +%Y%m%dT%H%M%SZ)" \
    -o /ai-etl-worker ./cmd/worker

# --- 阶段 2: 运行 ---
FROM debian:bookworm-slim

# 安装运行时依赖
RUN apt-get update && apt-get install -y --no-install-recommends \
    poppler-utils \
    pandoc \
    wget \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# 安全: 非 root 用户运行
RUN useradd -r -s /bin/false -m appuser
RUN mkdir -p /data/uploads && chown -R appuser:appuser /data
USER appuser

# 复制二进制
COPY --from=builder /ai-etl-worker /usr/local/bin/ai-etl-worker

EXPOSE 8080

ENTRYPOINT ["ai-etl-worker"]
