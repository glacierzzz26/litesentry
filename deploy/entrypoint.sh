#!/bin/sh
# 容器入口：先起 nginx（HTTPS 终止），再以生产参数（-prod）启动 litesentry-server。
# 所有密钥经环境变量注入：LITESENTRY_TOKEN / LITESENTRY_JWT_SECRET / LITESENTRY_BOOTSTRAP_*。
set -e

mkdir -p /var/lib/litesentry

echo "==> 校验 nginx 配置"
nginx -t

echo "==> 启动 nginx（:443 HTTPS → 127.0.0.1:8080）"
nginx -g 'daemon off;' &
NGINX_PID=$!
trap 'kill "$NGINX_PID" 2>/dev/null || true' EXIT

echo "==> 启动 litesentry-server（:8080 web/API，:9000 gRPC mTLS，-prod 强制安全基线）"
exec /usr/local/bin/litesentry-server \
  --web=127.0.0.1:8080 \
  --listen=:9000 \
  --db="sqlite:///var/lib/litesentry/litesentry.db" \
  --token="${LITESENTRY_TOKEN:-}" \
  --jwt-secret="${LITESENTRY_JWT_SECRET:-}" \
  --bootstrap-user="${LITESENTRY_BOOTSTRAP_USER:-admin}" \
  --bootstrap-pass="${LITESENTRY_BOOTSTRAP_PASS:-}" \
  --allow-agents="${LITESENTRY_ALLOW_AGENTS:-}" \
  --grpc-tls-cert=/etc/litesentry/certs/server.crt \
  --grpc-tls-key=/etc/litesentry/certs/server.key \
  --grpc-tls-ca=/etc/litesentry/certs/ca.crt \
  --prod
