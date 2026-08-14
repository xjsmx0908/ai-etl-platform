# ============================================================
# 多阶段构建 - Document Parser Service (Python)
# ============================================================

# --- 阶段 1: 依赖安装 ---
FROM python:3.11-slim AS builder

WORKDIR /app

# 安装系统依赖
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    && rm -rf /var/lib/apt/lists/*

# 复制依赖文件并安装
COPY requirements.txt .
RUN pip install --no-cache-dir --prefix=/install -r requirements.txt

# --- 阶段 2: 运行 ---
FROM python:3.11-slim

# OpenCV (OCR via rapidocr) runtime dependencies.
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgl1 libglib2.0-0 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# 复制安装的依赖
COPY --from=builder /install /usr/local

# 复制应用代码
COPY . .

# 安全: 非 root 用户运行
RUN useradd -r -s /bin/false -m appuser && \
    chown -R appuser:appuser /app
USER appuser

# 健康检查
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD python -c "import urllib.request; urllib.request.urlopen('http://localhost:8000/healthz')" || exit 1

EXPOSE 8000

# 启动命令
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000"]
