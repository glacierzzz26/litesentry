# litesentry · 轻量主机 + 容器监控

面向个人量化研究系统的主机/容器监控。**Agent 推送 → gRPC → Server 落库 → React 面板**，
单一二进制发布，SQLite 开箱即用；内置阈值告警引擎，告警经 **nexus 事件中心** 统一路由发送飞书。

> 技术栈：Agent = **Rust** · Server = **Go + Gin** · 前端 = **React 19 + Ant Design v5 + Tailwind CSS v4**（手写 SVG 图表，深/浅双主题）· 通信 = **gRPC**（双向 TLS + token 鉴权；未配证书时明文回落，仅限联调）

## 架构

```
┌─ Agent (Rust) ──────────┐        ┌─ Server (Go + Gin) ──────────────────────┐
│ 主机采集 (sysinfo)       │  gRPC   │  :9000 接收 + token/白名单鉴权            │
│ 容器采集 (bollard)       │ ─────→  │  Store: SQLite 默认 / PG 可选             │
│ IPv4/IPv6 地址探测       │  push   │  告警引擎 (60s 评估) ──→ nexus 事件中心    │
│ 自身 CPU/RSS 上报        │        │  :8080 REST + 内嵌 React 面板            │
└─────────────────────────┘        │  :8080 /docs  Swagger UI                 │
                                   └───────────────────────────────────────────┘
```

- 通信走 **IPv4**；IPv6 仅采集上报，供面板展示 / 直连 / SSH（主机详情「地址」区查看）。
- Agent 主动推送（NAT 友好），Server 永不主动拉取；**上报即心跳**（默认 60s）。
- `proto/litesentry.proto` 是唯一数据契约，Rust / Go 两端共用生成代码；`Series` 字段已预留，供阶段二插件上报。

## 快速开始

### A. 一键部署（Docker，推荐）

前后端已打进**同一个镜像**：nginx 终止 HTTPS（:443）→ Go Server（内嵌前端 + REST :8080）+ gRPC（:9000 mTLS）。

```bash
# 1) 构建镜像：本机先编译前端（go:embed 进 Server 二进制）+ Server，再以 nginx 为基础镜像打包（只拉 nginx:alpine）
make docker

# 2) 生成 mTLS 证书（SAN 写浏览器访问与 Agent 连接的地址）
./deploy/gen-certs.sh ./certs 'DNS:monitor.example.com,IP:1.2.3.4'

# 3) 配置密钥（勿提交版本库）
cp deploy/.env.example deploy/.env   # 填入 LITESENTRY_TOKEN / LITESENTRY_JWT_SECRET / LITESENTRY_BOOTSTRAP_PASS（强密码，禁止 admin）

# 4) 启动
docker compose -f deploy/docker-compose.yml up -d --build
```

- 浏览器打开 **https://<主机>/**，用 `.env` 里的 `LITESENTRY_BOOTSTRAP_USER/PASS` 登录（首次登录强制改密）。
- 端口：**443**（面板/API HTTPS）· **9000**（Agent gRPC mTLS）。
- 完整验证（含 mTLS 端到端、证书生成细节）见下方「生产部署」。

### B. 部署 Agent（每台被监控主机）

```bash
# 证书生成时已产出 certs/{ca.crt,agent.crt,agent.key}，分发到 /etc/litesentry/（0600）
# /etc/litesentry/agent.env（0600）
LS_SERVER=monitor.example.com:9000
LS_TOKEN=<与 Server 一致的 token>
LS_TLS_CA=/etc/litesentry/ca.crt
LS_TLS_CERT=/etc/litesentry/agent.crt
LS_TLS_KEY=/etc/litesentry/agent.key

install -m 0755 agent/target/release/litesentry-agent /usr/local/bin/litesentry-agent
cp deploy/litesentry-agent.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now litesentry-agent
journalctl -u litesentry-agent -f   # 采集日志
```

> Agent 二进制需在目标主机架构上编译（`make agent`）；首次注册按机器指纹自动分发 `agent_id`。常用环境变量：

| 变量 | 默认 | 说明 |
|---|---|---|
| `LS_SERVER` | `127.0.0.1:9000` | Server gRPC 地址（配了证书自动走 `https://`） |
| `LS_TOKEN` | 空 | Bearer token |
| `LS_AGENT_ID` | 自动生成 | 手动指定节点标识 |
| `LS_ID_FILE` | `/var/lib/litesentry/agent_id` | agent_id 持久化文件 |
| `LS_INTERVAL` | `60` | 心跳周期（秒），即上报/下发节拍 |
| `LS_COLLECT_INTERVAL` | 同 `LS_INTERVAL` | 采集间隔（秒），下发给采集插件；与心跳解耦（可心跳 60s、采集 300s） |
| `LS_BUILTIN_DIR` | `<agent_exe>/plugins` | 内置采集插件（host/docker/disk）所在目录；**升级 agent 时须同步更新该目录**（SHA-256 按构建期清单复核） |
| `LS_TLS_CA` | 空 | mTLS：CA 证书 PEM（Agent 校验 Server 身份） |
| `LS_TLS_CERT` | 空 | mTLS：Agent 客户端证书 PEM |
| `LS_TLS_KEY` | 空 | mTLS：Agent 客户端私钥 PEM（600 权限） |

### C. 源码构建 + 本地运行（开发）

```bash
make deps      # 一次性：安装 swag / protoc 插件
make server    # 编译 Server（自动构建前端并 go:embed 内嵌）→ bin/litesentry-server
make agent     # 编译 Agent → agent/target/release/litesentry-agent

# 本地 Server（SQLite + dev token + 明文 gRPC + 种子 admin/admin）
make run-server
# 本地 Agent 上报到 127.0.0.1:9000
make run-agent
```

面板 <http://localhost:8080/>。Server 常用参数：

- `--token`：Agent 上报鉴权共享 token；不设则测试模式（免鉴权）。
- `--allow-agents=a,b`：agent_id 白名单（空 = 不校验）。
- `--db=postgres://user:pass@host/db` 切换 PostgreSQL。
- `-prod`：生产强制安全基线 —— 显式 JWT 密钥 + gRPC mTLS 证书齐全 + token 非空 + 禁止默认种子密码，任一不满足即拒绝启动。
- mTLS 证书参数：`--grpc-tls-cert/--grpc-tls-key/--grpc-tls-ca`（三者齐全即启用双向 TLS，否则明文）。
- 密钥可从环境变量读：`LITESENTRY_TOKEN` / `LITESENTRY_JWT_SECRET` / `LITESENTRY_BOOTSTRAP_USER` / `LITESENTRY_BOOTSTRAP_PASS`，避免进 `ps`。

### D. 前端开发（热更新）

```bash
cd web && npm install && npm run dev   # :5173，/api 与 /docs 代理到 :8080
```

后端地址默认 `http://127.0.0.1:8080`，可用 `LS_API_PROXY` 覆盖（如 `LS_API_PROXY=http://localhost:8088 npm run dev`）。

## 面板

v2 界面（按 `litesentry-prototype-v2.html` 重建）：左侧 **232px** 侧栏（品牌 + 图标导航，可折叠），顶部细顶栏（页面标题 + 在线状态点 `x/y 在线` + **深/浅主题切换** + 当前用户 + 退出）。主题选择记忆在 `localStorage`，首屏不闪烁。指标进度条统一配色：**≤60 绿 · 60–85 琥珀 · >85 红**。

| 路由 | 页面 |
|---|---|
| `#/` | 总览：4 张统计卡（主机/在线/运行中容器/未恢复告警，可点击跳转）+ 主机卡片网格（CPU/内存 sparkline、磁盘/负载）+ 最近告警表 |
| `#/hosts` | 主机列表：状态 LED + 主机名 + 系统/版本 + CPU/内存/磁盘进度条 + 负载 + Agent 开销，点行进详情 |
| `#/agent/:id` | 节点详情：返回 + 名称 + LED + 1h/6h/24h/7d 时间窗，CPU/内存/网络/负载曲线 + 磁盘占用 + Agent 自身开销 + 容器表 + IPv4/IPv6 地址（可复制） |
| `#/containers` | 容器列表（含所属主机/镜像/状态/CPU/内存/网络，点名称进详情） |
| `#/container/:agent/:cid` | 容器详情：CPU/内存/网络曲线 + 基础信息 |
| `#/alerts` | 告警事件（级别/规则/节点/指标/值阈值/开始时间） |
| `#/alerts/rules` | 告警规则配置（新建/编辑/删除/启用） |
| `#/settings` | 设置：用户管理 + 告警通知（Nexus：地址 / source / token + 发送测试） |

> 节点一律以**主机名**展示（`agent_id` 不直接显示）。

## 告警

- **规则**：指标 = CPU/内存/负载/磁盘/容器 CPU/容器内存/容器停止/节点离线，比较 `>` `<`，可设持续秒数（防抖）与级别（warning/critical）。
- **评估**：Server 每 60s 扫描一次；持续超阈值达到 `duration_s` 才触发（firing），恢复后自动 resolve；同一事件通知冷却 30 分钟防刷屏。
- **通知**：firing / resolved 各作为一条事件上报 **nexus 事件中心**（`POST {nexus_url}/api/v1/events`，`Authorization: Bearer <ingest token>`），由 nexus 按 severity 路由并发送飞书。配置存 `settings` 表（设置页可改、即时生效），未配置则跳过并记日志。**本服务不再直连飞书。**
- 事件存 `alert_events` 表（随 30 天保留期清理），面板「告警事件」页可查。

## 安全

- **gRPC 双向 TLS（mTLS）**：配齐 `--grpc-tls-cert/--grpc-tls-key/--grpc-tls-ca` 即启用，Agent 必须持有本 CA 签发的客户端证书；未配证书时明文回落（仅限开发联调）。
- **应用层鉴权**：Bearer token（`authorization: Bearer <token>`）+ agent_id 白名单（`--allow-agents`）+ 时间戳防重放（±5 分钟），mTLS 之上再兜一层。
- **Web 面板**：JWT（HS256，`sub`=用户 id）；密码 bcrypt 落库、**绝不通过 JSON 回传**；登录连续失败 5 次锁定 15 分钟。
- **密钥不进命令行**：token / JWT 密钥 / 种子账号支持环境变量（`LITESENTRY_*`）注入，避免被 `ps` 读到；`-prod` 强制安全基线。
- **nexus ingest token**：只出现在上报请求的 `Authorization` 头，**绝不写入日志**；设置接口只回传"是否已配置"。
- 证书私钥 / token 文件权限 **0600**。

## 生产部署

生产采用**单 Docker 镜像**：nginx 终止 HTTPS（:443）→ 127.0.0.1:8080（Go web/API，前端已内嵌）；gRPC :9000 对外走 mTLS。Agent 每台主机用 systemd 守护（需访问宿主 docker socket）。

### 1. 生成证书（含真实主机名 SAN）

```bash
# SAN 必须匹配浏览器访问的地址与 Agent 连接的 Server 地址（域名或 IP）
./deploy/gen-certs.sh ./certs 'DNS:monitor.example.com,IP:1.2.3.4'
# 产物：certs/ca.crt · server.crt · server.key · agent.crt · agent.key
```

分发：**Server** 用 `ca.crt + server.crt + server.key`；**每台 Agent** 用 `ca.crt + agent.crt + agent.key`（放 0600 路径）。注意 `ca.crt` 对所有机器都发（双向验证信任根）。

### 2. 构建并启动镜像

> 构建走**本地产物**：`make docker` 先在宿主机编译 Server 二进制（前端已 go:embed 内嵌），再以 **nginx 为基础镜像**只做打包 —— 不在容器内跑 node/golang 构建。

```bash
# 本地构建产物 + nginx 基础镜像打包
make docker
# 准备 .env（复制 deploy/.env.example，填入随机长密钥；勿提交版本库）
cp deploy/.env.example deploy/.env
# 启动
docker compose -f deploy/docker-compose.yml up -d --build
# 验证
curl -k https://<主机>/              # 应返回前端 HTML
# 登录拿 token（除 /api/auth/login 外，其余 /api/* 均需 JWT）
TOKEN=$(curl -sk -X POST https://<主机>/api/auth/login -H 'Content-Type: application/json' \
  -d "{\"username\":\"<管理员>\",\"password\":\"<密码>\"}" | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
curl -k https://<主机>/api/health -H "Authorization: Bearer $TOKEN"   # {"status":"ok","time":"..."}
```

- 端口：**443**（浏览器 HTTPS）· **9000**（Agent gRPC mTLS）。
- 证书以只读 volume 挂载进容器（`certs/`），**不进镜像**；DB 用命名卷 `litesentry-db` 持久化。
- `-prod` 在 entrypoint 内强制：缺 `LITESENTRY_JWT_SECRET` / 证书 / token，或种子密码为 `admin`，容器立即退出并打印中文原因。

### 3. 部署 Agent（每台被监控主机）

```bash
# /etc/litesentry/agent.env（0600）
LS_SERVER=monitor.example.com:9000
LS_TOKEN=<与 Server 一致的 token>
LS_TLS_CA=/etc/litesentry/ca.crt
LS_TLS_CERT=/etc/litesentry/agent.crt
LS_TLS_KEY=/etc/litesentry/agent.key

# 安装二进制 + 开机自启
install -m 0755 agent/target/release/litesentry-agent /usr/local/bin/litesentry-agent
# 内置采集插件（host/docker/disk）随 agent 旁路发布，须放到 <agent_exe>/plugins/
mkdir -p /usr/local/bin/plugins
install -m 0755 agent/plugins/host/target/release/host     /usr/local/bin/plugins/host
install -m 0755 agent/plugins/docker/target/release/docker /usr/local/bin/plugins/docker
install -m 0755 agent/plugins/disk/target/release/disk     /usr/local/bin/plugins/disk
cp deploy/litesentry-agent.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now litesentry-agent
journalctl -u litesentry-agent -f   # 查看采集日志
```

> ⚠️ **Server 与 Agent 须成对升级**：内置插件体系依赖 `RegisterRequest.builtins` 上报 +
> Server 侧落 `agents.builtin_json`。旧 Server + 新 agent 会退化为「本地空清单兜底」（采集照跑，
> 但插件页看不到）；新 Server + 旧 agent 因无内置清单上报**不会播种指派**，插件页同样看不到 ——
> 两侧版本一致才完整。升级 agent 时**务必同步更新 `/usr/local/bin/plugins/`**（见上），
> 否则构建期写入的 SHA-256 与文件不符，内置插件会被拒绝启动。

### 4. 验证 mTLS 端到端

```bash
# 合法 Agent 证书 → 推送成功
go -C server run ./cmd/smoke -addr <主机>:9000 -token <token> \
  -tls-ca ../certs/ca.crt -tls-cert ../certs/agent.crt -tls-key ../certs/agent.key

# 缺客户端证书 → 握手失败被拒（transport: authentication handshake failed）
go -C server run ./cmd/smoke -addr <主机>:9000 -token <token> -tls-ca ../certs/ca.crt
```

### 5. 开发态对照

| 场景 | Server | Agent |
|---|---|---|
| 本地联调（明文） | `make run-server` | `make run-agent` |
| 生产（mTLS） | 镜像 + `-prod`（entrypoint 注入） | systemd + `LS_TLS_*` |

## 数据保留

默认保留 30 天（`--retention`），Server 每小时自动清理过期时序数据（含告警事件）。

## 目录结构

```
proto/                    # 唯一数据契约（gRPC）
server/                   # Go + Gin
  internal/grpc/          #   gRPC 接收 + 鉴权
  internal/store/         #   Store 抽象：SQLite 默认 + PostgreSQL 可选
  internal/alert/         #   告警评估引擎 + nexus 事件上报
  internal/api/           #   REST（/api）+ Swagger 注解
  internal/webui/         #   内嵌 React 构建产物
  cmd/server/             #   入口
  cmd/smoke/              #   端到端冒烟客户端
agent/                    # Rust（sysinfo + bollard + tonic）
web/                      # React 19 + antd v5 + Tailwind CSS v4（v2 单入口，手写 SVG 图表，深/浅双主题）
deploy/                   # 生产部署
  gen-certs.sh            #   mTLS 证书生成（CA/Server/Agent，SAN 可配）
  Dockerfile              #   单镜像：nginx 基础镜像 + server（内嵌 web，本地产物打包）
  nginx.conf              #   :443 HTTPS → 127.0.0.1:8080
  entrypoint.sh           #   容器入口（nginx + -prod 启动 server）
  docker-compose.yml      #   certs 挂载 + db 卷 + .env 注入
  .env.example            #   生产密钥模板
  litesentry-agent.service#   Agent systemd 守护
docs/                     # gRPC 契约文档（protoc-gen-doc 生成）
```

## 路线图

- 阶段一（当前）：主机 + 容器 + 地址面板，SQLite 落库，REST 供前端，阈值告警经 nexus 事件中心通知。
- 阶段二（已落地）：Agent 插件化 —— 二进制插件由 Server 指派（`DesiredState` 下发 + `FetchPlugin` 分块拉取），`Series` 字段承载插件指标；FRP 隧道管理与定时任务均在 agent 侧执行。**内置采集插件（host/docker/disk）随 agent 旁路发布，并并入同一套插件体系** —— 注册时上报内置清单，Server 做一次性默认兜底指派，此后可像外部插件一样取消指派 / 升级。后续 Pro 版可在此基础扩展安全 / AI 能力。
