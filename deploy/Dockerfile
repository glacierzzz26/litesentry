# syntax=docker/dockerfile:1
# litesentry 单镜像 —— 以 nginx 为基础镜像，只打包宿主机本地构建产物。
# 前端在 web/dist 构建并 go:embed 进 server 二进制；本文件不负责编译，避免容器内
# 拉取 node/golang 构建镜像，产物与镜像构建分离、构建快且可复现。
#
# 前提（在宿主机执行）：
#   make web          # npm build + 复制 dist 进 server/internal/webui/dist
#   make server       # go build → bin/litesentry-server（内嵌前端 + OpenAPI 文档）
# 然后打包：
#   make docker       # = docker build -f deploy/Dockerfile -t litesentry .
# 或 docker compose up -d --build
#
# 证书/密钥一律不进镜像：证书用 volume 挂载到 /etc/litesentry/certs/，
# 密钥经环境变量注入（见 docker-compose.yml 与 .env）。

FROM nginx:alpine
RUN mkdir -p /etc/litesentry/certs /var/lib/litesentry \
    && rm -f /etc/nginx/conf.d/default.conf
COPY bin/litesentry-server /usr/local/bin/litesentry-server
COPY deploy/nginx.conf /etc/nginx/conf.d/litesentry.conf
COPY deploy/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
EXPOSE 443 9000
ENTRYPOINT ["/entrypoint.sh"]
