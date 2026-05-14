# ============================================================
# Makefile - AI ETL Pipeline 标准化构建命令
# ============================================================

APP_NAME := ai-etl-pipeline
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u +%Y%m%dT%H%M%SZ)
LDFLAGS  := -s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME)

.PHONY: all build-worker build-api run-worker run-api clean test lint docker-worker docker-api up down help

## help: 显示帮助信息
help:
	@echo "Usage: make <target>"
	@echo ""
	@echo "Targets:"
	@echo "  build-worker  编译 ETL Worker"
	@echo "  build-api     编译 Query API"
	@echo "  run-worker    本地运行 Worker（dev 模式）"
	@echo "  run-api       本地运行 API（dev 模式）"
	@echo "  test          运行测试"
	@echo "  lint          代码检查"
	@echo "  docker-worker 构建 Worker Docker 镜像"
	@echo "  docker-api    构建 API Docker 镜像"
	@echo "  up            启动全套服务（docker-compose）"
	@echo "  down          停止全套服务"
	@echo "  clean         清理构建产物"
	@echo "  deps          下载依赖"
	@echo "  kafka-init    创建 Kafka Topic"

## build-worker: 编译 ETL Worker
build-worker:
	go build -ldflags "$(LDFLAGS)" -o bin/$(APP_NAME)-worker ./cmd/worker

## build-api: 编译 Query API
build-api:
	go build -ldflags "$(LDFLAGS)" -o bin/$(APP_NAME)-api ./cmd/api

## build: 编译所有服务
build: build-worker build-api

## run-worker: 本地 dev 模式运行 Worker
run-worker: build-worker
	ENVIRONMENT=dev ./bin/$(APP_NAME)-worker

## run-api: 本地 dev 模式运行 API
run-api: build-api
	ENVIRONMENT=dev JWT_SECRET=dev-secret ./bin/$(APP_NAME)-api

## test: 运行测试
test:
	go test -v -race -coverprofile=coverage.out ./internal/...
	go tool cover -func=coverage.out

## lint: 代码检查
lint:
	golangci-lint run ./...

## deps: 下载依赖
deps:
	go mod tidy
	go mod download

## docker-worker: 构建 Worker Docker 镜像
docker-worker:
	docker build --build-arg VERSION=$(VERSION) -t $(APP_NAME)-worker:$(VERSION) -f Dockerfile .
	docker tag $(APP_NAME)-worker:$(VERSION) $(APP_NAME)-worker:latest

## docker-api: 构建 API Docker 镜像
docker-api:
	docker build --build-arg VERSION=$(VERSION) -t $(APP_NAME)-api:$(VERSION) -f Dockerfile.api .
	docker tag $(APP_NAME)-api:$(VERSION) $(APP_NAME)-api:latest

## docker: 构建所有 Docker 镜像
docker: docker-worker docker-api

## up: 启动全套服务
up:
	docker compose up -d
	@echo "等待服务就绪..."
	@sleep 10
	@echo "Kafka UI: http://localhost:8090"
	@echo "Qdrant:   http://localhost:6333/dashboard"
	@echo "Pipeline: http://localhost:8080/healthz"

## down: 停止全套服务
down:
	docker compose down

## kafka-init: 创建 Kafka Topic（需要先 up）
kafka-init:
	docker compose exec kafka kafka-topics.sh --bootstrap-server localhost:9092 \
		--create --if-not-exists --topic doc-processing --partitions 6 --replication-factor 1
	docker compose exec kafka kafka-topics.sh --bootstrap-server localhost:9092 \
		--create --if-not-exists --topic doc-processing-dlq --partitions 3 --replication-factor 1

## kafka-produce: 发送测试消息到 Kafka
kafka-produce:
	echo '{"file_path":"/data/test.pdf","doc_id":"test-001","tenant_id":"tenant-a"}' | \
		docker compose exec -T kafka kafka-console-producer.sh \
		--bootstrap-server localhost:9092 --topic doc-processing

## clean: 清理
clean:
	rm -rf bin/ coverage.out
