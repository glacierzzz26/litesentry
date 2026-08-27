# syntax=docker/dockerfile:1
# litesentry 单镜像 —— 自包含多阶段构建：一条 docker build 出完整镜像，无需宿主机 Go/Node 工具链。
#
#   阶段 1（node）   构建 React 前端 → web/dist
#   阶段 2（golang） 编译 server 二进制（go:embed 内嵌前端 + OpenAPI 文档）
#   阶段 3（nginx）  运行时：nginx 终止 HTTPS + server 进程（web :8080 / gRPC :9000）
#
# 用法：
#   make docker                     # = docker build -f deploy/Dockerfile -t litesentry:0.1.0 -t litesentry:latest .
#   docker compose up -d --build    # 或走 compose（context=仓库根）
#
# 证书/密钥一律不进镜像：证书用 volume 挂载到 /etc/litesentry/certs/，
# 密钥经环境变量注入（见 docker-compose.yml 与 .env）。server/docs（swag 产物）已入库，
# 构建期无需 swag；前端 dist 未入库，由阶段 1 生成后拷入 webui 内嵌目录。

# --- 阶段 1：前端（node） ---
FROM node:22-alpine AS web
WORKDIR /build/web
# 先装依赖（利用 Docker 层缓存，package*.json 未变则跳过）
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build          # tsc -b && vite build → /build/web/dist

# --- 阶段 2：Go server（golang，静态链接 → 可跑在 musl nginx:alpine） ---
FROM golang:1.26-alpine AS build
# 国内网络 proxy.golang.org 常超时：构建时 --build-arg GOPROXY=https://goproxy.cn,direct 可换镜像源
ARG GOPROXY=
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
# go:embed 的前端产物由阶段 1 提供，忽略本地 webui/dist（见 .dockerignore）
COPY --from=web /build/web/dist internal/webui/dist/
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/litesentry-server ./cmd/server

# --- 阶段 3：运行时（nginx 终止 HTTPS + server 进程） ---
FROM nginx:alpine
RUN mkdir -p /etc/litesentry/certs /var/lib/litesentry \
    && rm -f /etc/nginx/conf.d/default.conf
COPY --from=build /out/litesentry-server /usr/local/bin/litesentry-server
COPY deploy/nginx.conf /etc/nginx/conf.d/litesentry.conf
COPY deploy/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 443 9000
ENTRYPOINT ["/entrypoint.sh"]
