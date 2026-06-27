# ============================================================
# CPU Reranker Service
# ============================================================

FROM python:3.11-slim

WORKDIR /app

RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    && rm -rf /var/lib/apt/lists/*

COPY requirements.txt .
ARG TORCH_VERSION=2.3.1+cpu
RUN pip install --no-cache-dir \
    --extra-index-url https://download.pytorch.org/whl/cpu \
    "torch==${TORCH_VERSION}"
RUN pip install --no-cache-dir -r requirements.txt

COPY . .

RUN useradd -r -s /bin/false -m appuser && \
    mkdir -p /models/huggingface && \
    chown -R appuser:appuser /app /models
ENV PYTHONUNBUFFERED=1 \
    HF_HOME=/models/huggingface \
    TOKENIZERS_PARALLELISM=false
USER appuser

HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=5 \
    CMD python -c "import urllib.request; urllib.request.urlopen('http://localhost:8091/healthz')" || exit 1

EXPOSE 8091

CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8091"]
