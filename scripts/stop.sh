#!/bin/bash
# ==========================================
# AI-ETL Platform - 停止脚本
# ==========================================

set -e

echo "🛑 Stopping AI-ETL Platform..."

# 停止服务
docker compose down

echo ""
echo "✅ All services stopped."
echo ""
echo "💡 To remove volumes (delete all data):"
echo "   docker compose down -v"
echo ""
echo "💡 To remove images:"
echo "   docker compose down --rmi all"
