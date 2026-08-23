#!/usr/bin/env bash
# 生成 mTLS 证书体系（内部 CA + Server 证书 + Agent 证书）。
#
# 证书已真正接线：Server 配 -grpc-tls-* 启用双向 TLS，Agent 配 LS_TLS_* 接入；
# 同一份 server.crt/key 也用于 nginx 的 HTTPS（:443）终止。证书只放 0600 权限路径。
#
# 用法：
#   ./deploy/gen-certs.sh [输出目录] [Server SAN]
#
#   - 输出目录默认仓库根/certs（与 deploy/docker-compose.yml 的挂载一致）
#   - Server SAN 默认 DNS:localhost,IP:127.0.0.1；
#     生产请传真实主机名/公网 IP，例如：
#       ./deploy/gen-certs.sh ./certs 'DNS:monitor.example.com,IP:1.2.3.4'
#     （nginx 浏览器校验 + Agent 连接 https://<host>:9000 时都要能匹配）
set -euo pipefail

OUT="${1:-$(dirname "$0")/../certs}"
OUT="$(cd "$(dirname "$OUT")" && pwd)/$(basename "$OUT")"
SAN="${2:-DNS:localhost,IP:127.0.0.1}"
mkdir -p "$OUT"

CA_KEY="$OUT/ca.key"
CA_CRT="$OUT/ca.crt"
SERVER_KEY="$OUT/server.key"
SERVER_CRT="$OUT/server.crt"
AGENT_KEY="$OUT/agent.key"
AGENT_CRT="$OUT/agent.crt"
DAYS=3650

echo "==> 生成内部 CA"
openssl genrsa -out "$CA_KEY" 4096
openssl req -x509 -new -nodes -key "$CA_KEY" -sha256 -days "$DAYS" \
  -subj "/CN=litesentry-internal-ca" -out "$CA_CRT"

echo "==> 生成 Server 证书（签发自内部 CA，SAN=$SAN）"
openssl genrsa -out "$SERVER_KEY" 4096
openssl req -new -key "$SERVER_KEY" -subj "/CN=litesentry-server" \
  -addext "subjectAltName=$SAN" -out "$OUT/server.csr"
openssl x509 -req -in "$OUT/server.csr" -CA "$CA_CRT" -CAkey "$CA_KEY" -CAcreateserial \
  -out "$SERVER_CRT" -days "$DAYS" -sha256 \
  -extfile <(printf "subjectAltName=%s\nextendedKeyUsage=serverAuth\n" "$SAN")

echo "==> 生成 Agent 证书（mTLS 客户端身份）"
openssl genrsa -out "$AGENT_KEY" 4096
openssl req -new -key "$AGENT_KEY" -subj "/CN=litesentry-agent" -out "$OUT/agent.csr"
openssl x509 -req -in "$OUT/agent.csr" -CA "$CA_CRT" -CAkey "$CA_KEY" -CAcreateserial \
  -out "$AGENT_CRT" -days "$DAYS" -sha256 \
  -extfile <(echo "extendedKeyUsage=clientAuth")

chmod 600 "$CA_KEY" "$SERVER_KEY" "$AGENT_KEY"
rm -f "$OUT/server.csr" "$OUT/agent.csr"

echo "==> 完成。证书在 $OUT"
echo "    CA:   $CA_CRT        （分发给 Server/Agent 做双向验证，0644 即可）"
echo "    SVR:  $SERVER_CRT    （Server + nginx 用，配 -grpc-tls-cert 与 ssl_certificate）"
echo "    AGT:  $AGENT_CRT     （Agent 用，配 LS_TLS_CERT/LS_TLS_KEY）"
echo "    分发到每台被监控主机（agent 侧）：ca.crt + agent.crt + agent.key，放 0600 路径。"
