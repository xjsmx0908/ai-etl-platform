#!/bin/bash
# ==========================================
# AI-ETL Platform - 启动脚本
# ==========================================

set -e

echo "🚀 Starting AI-ETL Platform..."

# 检查 docker-compose.yml
if [ ! -f "docker-compose.yml" ]; then
    echo "❌ Error: docker-compose.yml not found"
    exit 1
fi

# 检查 .env 文件
if [ ! -f ".env" ]; then
    echo "⚠️  Warning: .env not found, copying from .env.example"
    cp .env.example .env
    echo "📝 Please edit .env file with your configuration"
fi

# 启动服务
echo "📦 Building and starting services..."
docker compose up -d --build

# 等待服务就绪
echo "⏳ Waiting for services to be ready..."
sleep 10

# 显示服务状态
echo ""
echo "✅ Service Status:"
docker compose ps

echo ""
echo "🌐 Access Points:"
echo "  - Query API:      http://localhost:8080"
echo "  - Parser Service: http://localhost:8000"
echo "  - Prometheus:     http://localhost:9090"
echo "  - Jaeger UI:      http://localhost:16686"
echo "  - Kafka UI:       http://localhost:8090"
echo "  - MinIO Console:  http://localhost:9001"
echo ""
echo "📖 View logs: docker compose logs -f"
echo "🛑 Stop:        docker compose down"
echo ""
echo "✨ AI-ETL Platform is running!"
