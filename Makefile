# litesentry 构建入口
# 依赖：go ≥1.21、cargo ≥1.75、node ≥20、protoc + protoc-gen-go(-grpc)、
#       swag（go install github.com/swaggo/swag/cmd/swag@latest）、protoc-gen-doc
# PATH 需要包含 ~/.local/bin、~/go/bin、~/.cargo/bin

BIN_DIR := bin
PROTO   := proto/litesentry.proto

.PHONY: all deps proto proto-doc docs web server agent build test run-server run-agent docker clean

all: build

## 安装文档/生成工具（一次性）
deps:
	go install github.com/swaggo/swag/cmd/swag@latest
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

## 重新生成 Go proto 代码（Rust 侧由 build.rs 在编译期生成）
proto:
	cd server && protoc -I ../proto --go_out=gen --go_opt=paths=source_relative \
		--go-grpc_out=gen --go-grpc_opt=paths=source_relative ../proto/litesentry.proto

## gRPC 契约文档（protoc-gen-doc）→ docs/proto.html + docs/proto.md
proto-doc:
	mkdir -p docs
	protoc -I proto --doc_out=docs --doc_opt=html,proto.html proto/litesentry.proto
	protoc -I proto --doc_out=docs --doc_opt=markdown,proto.md proto/litesentry.proto

## REST OpenAPI 文档（swaggo）→ server/docs（Swagger UI 由 Server 在 /docs 提供）
docs:
	cd server && swag init -g cmd/server/main.go -o docs

## 前端构建 + 复制进 server 内嵌目录
web:
	cd web && npm run build
	mkdir -p server/internal/webui/dist
	cp -r web/dist/* server/internal/webui/dist/

## 编译 Rust Agent（静态二进制）
agent:
	cd agent && cargo build --release

## 编译 Go Server（先保证前端与 OpenAPI 就位）
## CGO_ENABLED=0：静态链接，产物可放进 nginx:alpine（musl）运行
server: docs web
	mkdir -p $(BIN_DIR)
	cd server && CGO_ENABLED=0 go build -o ../$(BIN_DIR)/litesentry-server ./cmd/server

build: server agent proto-doc
	@echo "构建完成:"
	@ls -la $(BIN_DIR)/litesentry-server agent/target/release/litesentry-agent
	@echo "API 文档: docs/proto.html（gRPC 契约） ·  http://<server>:8080/docs/（REST Swagger UI）"

## 本地联调：Server（SQLite + 明文 gRPC + dev token + 种子管理员 admin/admin）
run-server:
	cd server && go run ./cmd/server --db=sqlite:///var/lib/litesentry/litesentry.db \
		--token=dev --web=:8080 --listen=:9000 \
		--bootstrap-user=admin --bootstrap-pass=admin

## 本地联调：Agent 上报到 127.0.0.1:9000
run-agent:
	cd agent && LS_SERVER=127.0.0.1:9000 LS_TOKEN=dev \
		LS_ID_FILE=/tmp/litesentry-agent-id cargo run --release

## 单元测试（Server：告警引擎 + 认证）
test:
	cd server && go test ./...

## 打包 Docker 镜像：本地构建 server 二进制（内嵌前端）→ 基于 nginx 的基础镜像只做 COPY
## CGO_ENABLED=0：静态链接，保证能跑在 nginx:alpine（musl）
docker: web
	mkdir -p $(BIN_DIR)
	cd server && CGO_ENABLED=0 go build -o ../$(BIN_DIR)/litesentry-server ./cmd/server
	docker build -f deploy/Dockerfile -t litesentry:latest .

clean:
	rm -rf $(BIN_DIR) agent/target server/docs server/internal/webui/dist
