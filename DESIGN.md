# litesentry — 轻量主机 + 容器监控系统设计

> 阶段：第一阶段（**已实现**，本设计文档已随实现对齐）+ 第二阶段前瞻（插件化，待定稿）
> 日期：2026-08-21（定稿）→ 2026-08-23（随实现更新）
> 技术栈：Agent = **Rust** · Server = **Go + Gin** · 前端 = **React 19 + Ant Design v5** · 通信 = **gRPC**（token + 可选 mTLS）

## 背景与目标

个人量化研究系统的轻量、简约、大方的监控系统。

| 约束 | 结论 |
|---|---|
| **轻量** | Agent 单二进制 ≤10MB、常驻内存 5~10MB，无运行时依赖；Server 单二进制，无重型框架 |
| **CS 架构** | Agent（被监控端采集器）→ Server（中心端 + 面板），**主动上报** |
| **内网但可访问外网** | 被监控机在内网/NAT 后，Agent 主动 push，Server 不反向拉取 |
| **IPv6** | **通信走 IPv4**；Agent 采集并上报本机全部 IPv4 + IPv6 全局地址，面板展示，便于 IPv6 直连/ssh |
| **安全防护** | 一级需求；gRPC 双向 **mTLS 已真正接线**（`-prod` 强制），未配证书时明文回落仅限联调；token / 白名单 / 时间戳防重放测试即启用 |
| **监控对象** | 主机（CPU/内存/磁盘/网络/负载）+ 容器（CPU/内存/网络/状态/重启数）+ **Agent 自身进程开销**（CPU/RSS） |
| **告警** | 内置阈值告警引擎（8 类指标，持续超限防抖），推送**飞书自定义机器人**（HMAC-SHA256 签名） |
| **Web 鉴权** | 面板 **JWT** 登录 + bcrypt 密码 + 用户管理（首登强制改密） |

## 总体架构

```
┌─────────────────┐  gRPC (HTTP/2 · IPv4)   ┌──────────────────────────────┐
│  Agent (Rust)   │ ──────────────────────▶ │  Server (Go + Gin)           │
│  · 主机采集 sysinfo │  :9000 Register+Push    │  · gRPC 接收 / token/白名单      │
│  · 容器采集 bollard │   token（可选 mTLS）      │  · Store: SQLite 默认 / PG 可选  │
│  · IPv4/IPv6 探测 │                         │  · 告警引擎 60s ──▶ 飞书机器人    │
│  · 自身 CPU/RSS   │                         │  · REST + JWT 供前端           │
└─────────────────┘                          │  · 内嵌 React SPA + Swagger    │
                    浏览器 (React SPA)          └──────────────┬───────────────┘
                      HTTPS :443 (nginx) ◀──────────────────────┘
                      → 127.0.0.1:8080 (JWT 登录)
```

- **机器 ↔ 机器**：Rust Agent → Go Server，**gRPC over IPv4**。启动时先 `Register`（按机器指纹 `/etc/machine-id` 换取/复用 `agent_id`），此后按周期 `Push`（默认 **60s**，上报即心跳）
- **浏览器 ↔ Server**：React 走 **REST + JWT**（Gin，`/api/*`），取数方式为**前端定时轮询（15s）**；SSE 设计期曾考虑，未采用，保持简单
- **生产**：单 Docker 镜像，nginx 终止 HTTPS（:443）→ 127.0.0.1:8080；gRPC :9000 对外走 mTLS
- gRPC 只对内网暴露（`:9000`），Web 面板走 `:8080`/`:443`，两侧端口严格分离
- Server 监听用 IPv4（可选双栈），**Agent 一律通过 IPv4 连接 Server**；IPv6 仅作为采集上报的地址数据，不参与监控传输

## 技术选型

### Agent（Rust）

| 模块 | 选型 | 理由 |
|---|---|---|
| gRPC | `tonic` + `prost` | Rust 生态标准 gRPC，HTTP/2，与 Go grpc-go 互操作成熟 |
| 主机指标 | `sysinfo` crate | 跨平台、零配置拿 CPU/内存/磁盘/负载/网络 |
| 容器指标 | `bollard`（Docker API） | 异步非阻塞，`containers/stats` 流式拿实时 CPU/内存/网络 |
| IPv6 | `get_if_addrs` 等 | 遍历网卡过滤全局 IPv6（排除 `fe80::`、`::1`） |
| 自监控 | `/proc/self/stat` + `/proc/self/statm` | 读 Agent 自身进程 CPU ticks 与 RSS，上报 `agent_cpu_pct` / `agent_mem_rss` |
| TLS | `tokio-rustls` | `LS_TLS_*` 三件套配齐即启用双向 TLS，否则明文回落（仅限联调） |
| 运行时 | `tokio` | 常驻内存压到 5~10MB |

> 编译目标：Rust 单静态二进制 ≈ 5~8MB，静态链接无 glibc 依赖，一键拷到任何 Linux 机器。
> 上报间隔：`LS_INTERVAL` 默认 **60s**（即心跳周期）；网络速率用前后两采样差分得到 bps。

### Server（Go + Gin）

| 模块 | 选型 | 理由 |
|---|---|---|
| Web 框架 | `gin-gonic/gin` | 轻量、路由/中间件齐全 |
| gRPC | `google.golang.org/grpc` | 接收 Agent 上报，含 `Register`/`Push`/`Stream`（预留） |
| 存储 | **SQLite（默认）** + **PostgreSQL（可选）** | SQLite 用 `modernc.org/sqlite`（纯 Go、无 cgo、WAL）；PG 用 `pgx`；统一 `Store` 接口，`--db=sqlite:///path` 或 `--db=postgres://...` 切换 |
| 面板鉴权 | JWT（HS256）+ bcrypt | Web 面板独立于 gRPC 的静态 token；登录失败计数锁防爆破 |
| 实时数据 | 前端轮询（无 SSE） | 设计期曾规划 SSE，最终以 15s 轮询简化实现 |
| 前端静态 | `embed` 打包进二进制 | 发布 = 一个二进制；生产由 nginx 反代 HTTPS |
| API 文档 | swaggo OpenAPI | `swag init` → `/docs` Swagger UI |

### 前端（React）

- **React 19 + TypeScript + Vite** + **Ant Design v5**（组件/布局主题）+ **Tailwind v4**（仅布局辅助类）+ **zustand**（全局状态）+ **dayjs**（时间）
- **图表：手写 SVG 组件**（`MetricChart` 折线+面积+十字线 tooltip / `Sparkline` / `MiniMeter`），不引图表库——体积小、视觉完全可控，契合"简约大气"
- 单浅色主题（对齐 chprobe 风格）：白色侧栏 240px 可折叠到 64px、白色顶栏、浅色卡片网格
- 路由用**极简 hash 路由**（自研 `useHashRoute`，不引 react-router）

## 数据存储设计

### Store 抽象接口

统一 `Store` 接口，业务层（gRPC/API/告警）不感知后端，通过 `--db` 切换实现：

```go
type Store interface {
  RegisterAgent(ctx, machineID string, a *Agent) (agentID string, isNew bool, err error) // 按机器指纹注册/复用
  UpsertAgent(ctx, a *Agent) error                        // 更新节点最新状态（含 IPv4/IPv6 地址快照）
  AppendBatch(ctx, h *HostSample, disks, containers) error // 主机+磁盘+容器一批单事务
  QueryHost(ctx, agentID string, from, to time.Time) ([]*HostSample, error)
  QueryContainers(ctx, ...) ([]*ContainerSample, error)
  QueryDisks(ctx, ...) ([]*DiskSample, error)
  Agents(ctx) ([]*Agent, error)
  Overview(ctx, from, to) (*Overview, error)             // 每节点最新主机样本 + 最高磁盘占用 + firing 事件数（消除 N+1）

  GetSetting / SetSetting                                  // 通用设置（飞书 webhook/secret 等）

  // ---- 用户（面板登录，密码 bcrypt）----
  CreateUser / UpdateUser / GetUserByID / GetUserByUsername / ListUsers / DeleteUser / CountUsers / SetLastLogin

  // ---- 告警规则 / 事件 ----
  ListRules / SaveRule / DeleteRule
  AppendEvent / QueryEvents / ResolveEvent / SetEventNotified

  Cleanup(ctx, retention) (int64, error)                  // 清理超保留期时序数据
  Close() error
}
```

- **默认**：`--db=sqlite:///var/lib/litesentry/litesentry.db`（`modernc.org/sqlite` 纯 Go、WAL、busy_timeout）
- **可选**：`--db=postgres://user:pass@host:5432/litesentry`（`pgx` 驱动）
- **时序时间统一用 INTEGER 存 unix 秒**，规避 SQLite / PG 的 TIMESTAMP 方言差异
- `agent_id` 由 Server 分发：首次 `Register` 分配 UUID 并回写 `machine_id`；同机器重装/重启复用历史 id

### Schema

```sql
-- 节点最新状态（地址快照：IPv4/IPv6 全量 JSON，含 iface/scope）
CREATE TABLE agents (
  agent_id   TEXT PRIMARY KEY,
  hostname   TEXT NOT NULL,
  os TEXT, arch TEXT, kernel TEXT,
  version    TEXT NOT NULL DEFAULT '',   -- Agent 构建版本（注册上报）
  machine_id TEXT,                       -- 机器指纹，注册复用；唯一索引（partial）
  ipv4 TEXT NOT NULL DEFAULT '[]',
  ipv6 TEXT NOT NULL DEFAULT '[]',
  last_seen  INTEGER NOT NULL,           -- unix 秒
  created_at INTEGER NOT NULL
);

-- 主机时序（append-only，批写入）
CREATE TABLE host_metrics (
  agent_id TEXT NOT NULL, ts INTEGER NOT NULL,
  hostname TEXT, os TEXT, arch TEXT, kernel TEXT, uptime_s INTEGER,
  load_1m REAL, load_5m REAL, cpu_pct REAL,
  agent_cpu_pct REAL, agent_mem_rss INTEGER,   -- Agent 自身进程开销
  mem_total INTEGER, mem_used INTEGER, swap_total INTEGER, swap_used INTEGER,
  net_rx_bps INTEGER, net_tx_bps INTEGER
);
CREATE INDEX idx_host_agent_ts ON host_metrics (agent_id, ts);

-- 磁盘 / 容器时序（每批多行，INSERT OR REPLACE）
CREATE TABLE disk_metrics (
  agent_id TEXT NOT NULL, ts INTEGER NOT NULL,
  mount TEXT NOT NULL, fs TEXT, total INTEGER, used INTEGER,
  PRIMARY KEY (agent_id, ts, mount)
);
CREATE TABLE container_metrics (
  agent_id TEXT NOT NULL, ts INTEGER NOT NULL, container_id TEXT NOT NULL,
  name TEXT, image TEXT, state TEXT, restarts INTEGER,
  uptime_s INTEGER, cpu_pct REAL,
  mem_usage INTEGER, mem_limit INTEGER, net_rx_bps INTEGER, net_tx_bps INTEGER,
  PRIMARY KEY (agent_id, ts, container_id)
);

-- 通用设置（飞书 webhook / secret 等）
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '', updated_at INTEGER);

-- 面板登录用户（密码 bcrypt，绝不回传）
CREATE TABLE users (
  id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL,
  display_name TEXT NOT NULL DEFAULT '', role TEXT NOT NULL DEFAULT 'admin',
  must_change_password INTEGER NOT NULL DEFAULT 0,
  last_login_at INTEGER, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);

-- 告警规则
CREATE TABLE alert_rules (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, metric TEXT NOT NULL,
  op TEXT NOT NULL DEFAULT '>', threshold REAL NOT NULL DEFAULT 0,
  duration_s INTEGER NOT NULL DEFAULT 0, severity TEXT NOT NULL DEFAULT 'warning',
  enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);

-- 告警事件（firing / resolved）
CREATE TABLE alert_events (
  id TEXT PRIMARY KEY, rule_id TEXT, rule_name TEXT NOT NULL,
  agent_id TEXT NOT NULL, agent_name TEXT NOT NULL DEFAULT '',
  entity_id TEXT NOT NULL DEFAULT '', entity_name TEXT NOT NULL DEFAULT '',
  metric TEXT NOT NULL, value REAL, threshold REAL,
  severity TEXT NOT NULL DEFAULT 'warning', state TEXT NOT NULL DEFAULT 'firing',
  started_at INTEGER NOT NULL, resolved_at INTEGER, notified_at INTEGER
);
CREATE INDEX idx_events_agent_ts ON alert_events (agent_id, started_at);
CREATE INDEX idx_events_state  ON alert_events (state, started_at);

-- 阶段二插件数据（预留，阶段一未建表）
-- CREATE TABLE series (agent_id TEXT, ts INTEGER, name TEXT, tags JSON, fields JSON);
```

### 写入与保留策略

- **批量事务写入**：`AppendBatch` 单事务写入主机 + 磁盘 + 容器；Agent 默认 60s 一批，每节点 ~60 行/分，写入量很小
- **保留策略**：默认保留 30 天（`--retention`），Server 每小时周期 `Cleanup` 删除超期时序 + 告警事件
- **PostgreSQL 进阶（可选）**：数据量大时 `PARTITION BY RANGE(ts)` 按月分区，或加 TimescaleDB 扩展；个人场景默认不需要

## gRPC 协议（proto）

`proto/litesentry.proto` 是唯一数据契约，Go/Rust 两端共用一份生成代码（Rust 由 `build.rs` 编译期生成）。完整契约见 `docs/proto.html`，要点：

```proto
service Agent {
  rpc Register(RegisterRequest) returns (RegisterReply);  // 启动时按机器指纹换取/复用 agent_id
  rpc Push(MetricsBatch) returns (PushAck);               // 常规定时上报（默认 60s，即心跳）
  rpc Stream(stream MetricsBatch) returns (stream PushAck); // 预留：长连接流式（阶段二插件分发复用此通道）
}

message RegisterRequest { hostname; machine_id; os; arch; kernel; version }  // 机器指纹 /etc/machine-id
message RegisterReply   { agent_id; server_time }

message HostMetrics {
  hostname; os; arch; kernel; uptime_s; load_1m; load_5m; cpu_pct;
  Mem mem; Mem swap; repeated Disk disks; Net net; repeated IPAddr ips;
  float  agent_cpu_pct = 14;  // Agent 自身进程周期 CPU 占用（%）
  uint64 agent_mem_rss = 15;  // Agent 自身 RSS（字节）
}
message ContainerMetrics { id; name; image; state; restarts; uptime_s; cpu_pct; ContainerMem mem; Net net }
message Series { ... }        // 阶段二插件通用字段，阶段一预留不填充
```

> 前端不直接碰 gRPC。Server 把采集结果写入 SQLite（或 PostgreSQL），前端走 REST 拿 JSON。
> 鉴权：metadata `authorization: Bearer <token>`；可选 mTLS（`--grpc-tls-*` 三件套齐全即启用）。
> **阶段一已实现 `Register`（机器指纹注册），并预留 `Series` / `Stream` 供阶段二直接启用。**

## REST API 一览（前端取数）

所有 `/api/*`（除 `POST /api/auth/login`）均需 `Authorization: Bearer <JWT>`。Swagger：`http://<server>:8080/docs/`。

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/auth/login` | 登录换 JWT（bcrypt 校验 + 失败锁定 5 次/15min） |
| POST | `/api/auth/change-password` | 改密（首登/重置后强制） |
| GET | `/api/auth/me` | 当前用户 |
| GET | `/api/health` | 健康检查 |
| GET | `/api/overview` | 总览聚合：每节点最新主机样本 + 最高磁盘占用% + firing 事件数 |
| GET | `/api/agents` | 节点列表（含 IPv4/IPv6 快照、online/offline 状态） |
| GET | `/api/agents/:id/host` `…/containers` `…/disks` | 时序样本（`?from=&to=` unix 秒） |
| GET/POST | `/api/alerts/rules` · PUT/DELETE `/api/alerts/rules/:id` | 告警规则 CRUD |
| GET | `/api/alerts/events` | 告警事件（按节点/状态/时间窗口过滤） |
| GET/PUT | `/api/settings` · POST `/api/settings/feishu-test` | 飞书机器人配置 / 发送测试 |
| GET/POST | `/api/users` · PUT/DELETE `/api/users/:id` | 用户管理（重置密码、删除守卫） |

## 告警引擎

- **指标集**（8 类）：`cpu_pct` / `mem_pct` / `load_1m` / `disk_pct`（任一挂载点） / `container_cpu` / `container_mem` / `container_down`（state≠running） / `offline`（心跳 > 5min）
- **规则**：比较符 `>` `<`、阈值、`duration_s` 持续防抖（0=立即）、级别 `warning/critical`、`enabled`
- **评估**：Server 内 `alert.Engine` 每 **60s** 扫描各节点最新样本（5min 窗口），按 `ruleID|agentID|entityID` 维护**内存状态机**（breach 起始时间 → firing → resolve）
- **通知**：firing 时发送**飞书自定义机器人**（HMAC-SHA256 签名），同一事件 **30min 冷却**防刷屏；恢复自动 resolve 并落库
- **事件**：`firing` / `resolved` 落 `alert_events` 表，面板「告警事件」页可查；总览页统计未恢复数
- 通知失败不阻塞评估；`Notify` 注入点可替换（单元测试用 stub）

## 用户与认证

- **Web 面板**：JWT（HS256，`sub`=用户 id，TTL 默认 24h）；密码 **bcrypt** 落库、`json:"-"` 绝不回传；首登/重置后 `must_change_password` 强制改密
- **防爆破**：登录失败计数锁（同一 IP+用户名 5 次 / 15min 锁定）；用户不存在也做真实 bcrypt 比对，消除用户名枚举时序差
- **种子管理员**：用户表为空时由 `--bootstrap-user/-bootstrap-pass`（或环境变量）创建，首登强制改密；禁止默认密码 `admin`
- **用户管理**：设置页可建/删用户、重置密码（守卫：不能删自己、不能删最后一个用户）；阶段一全 `admin` 角色，角色列预留
- **密钥优先级**：命令行参数 > 环境变量（`LITESENTRY_TOKEN` / `LITESENTRY_JWT_SECRET` / `LITESENTRY_BOOTSTRAP_*`），避免 `ps` 可见

## 安全防护

| 层 | 措施 |
|---|---|
| **传输加密** | gRPC 全链路 **双向 TLS（mTLS，已接线）**：`--grpc-tls-cert/--grpc-tls-key/--grpc-tls-ca` 三件套齐全即启用，Agent 必须持 CA 签发客户端证书；未配证书时明文回落（仅限联调）；nginx :443 终止 HTTPS |
| **身份认证** | ① mTLS 证书即身份（可选）；② 共享 token 走 gRPC metadata（`authorization`），测试即启用 |
| **防伪造/重放** | `agent_id` 白名单（`--allow-agents`）；时间戳偏离 >5min 拒收 |
| **Web 面板鉴权** | JWT（HS256）+ bcrypt 密码 + 登录失败锁定 + 首登强制改密 |
| **端口收敛** | 只开 `:9000`（gRPC/mTLS）和 `:8080`/`:443`（Web）；Agent 零入站端口 |
| **密钥管理** | token/CA 密钥 0600 权限，绝不写入代码和日志；日志脱敏 |
| **生产基线** | `-prod` 强制：显式 JWT 密钥 + mTLS 证书齐全 + token 非空 + 禁止默认种子密码，任一不满足拒绝启动 |
| **Docker 面** | Agent 只读 Docker socket，不具管理权限；飞书 secret 只参与 HMAC 计算，接口只回传"是否已配置" |

## 前端设计语言（简约大气 · antd 浅色）

- **布局**：白色侧栏 240px（折叠到 64px 图标）+ 白色顶栏 56px（页面标题 + 服务状态点 + 用户名），浅色卡片网格，一屏总览
- **视觉**：大面积留白、细边框（1px）、柔和阴影；指标数字等宽字体、大字号主色高亮；主题色 `#2a78d6` 蓝灰中性
- **图表**：**手写 SVG** —— `MetricChart` 多序列折线 + 面积填充 + 十字线 tooltip，`Sparkline` 主机卡片趋势小图，`MiniMeter` 阈值配色进度条；不引图表库
- **数据获取**：总览/详情页独立轮询（15s），`/api/overview` 一次聚合消除 N+1
- **页面**：
  - `#/` 总览：统计卡片（主机/在线/容器/运行中/未恢复告警，可点击跳转）+ 主机卡片网格（含 CPU sparkline）+ 最近告警
  - `#/hosts` 主机列表（在线优先排序）
  - `#/agent/:id` 节点详情：当前值 + 1h/6h/24h/7d 曲线 + 磁盘 + 容器 + **地址表（IPv4/IPv6 一键复制）** + Agent 自身开销
  - `#/containers` 容器列表（跨节点，含所属主机）
  - `#/container/:agent/:cid` 容器详情：CPU/内存/网络曲线 + 基础信息
  - `#/alerts` 告警事件 · `#/alerts/rules` 告警规则配置
  - `#/settings` 设置：**用户管理** + 飞书机器人
  - 未登录 → 登录页；首登/重置密码 → 强制改密页
- 单浅色主题（设计期规划的明暗双主题未启用；暗色主题列为后续可选）

## 项目结构

```
litesentry/
├── proto/litesentry.proto      # 唯一数据契约
├── agent/                      # Rust
│   └── src/
│       ├── main.rs             # 启动注册 + 周期上报（core 传输核）
│       ├── collector.rs        # 主机采集（sysinfo + 网络差分 + 自身 CPU/RSS）
│       ├── docker.rs           # 容器采集（bollard）
│       ├── ip.rs               # IPv4/IPv6 地址探测
│       ├── client.rs           # tonic gRPC + TLS
│       └── config.rs           # 配置/密钥/机器指纹加载（0600）
├── server/                     # Go + Gin
│   ├── cmd/server/main.go      # 入口（flag/env、-prod 校验、优雅退出）
│   ├── cmd/smoke/              # 端到端 mTLS 冒烟客户端
│   └── internal/
│       ├── grpc/               # gRPC 接收 + mTLS + token/白名单/时间戳
│       ├── store/              # Store 接口 + SQLite/PG 实现 + schema 迁移
│       ├── api/                # gin REST + JWT 中间件 + 用户/设置/告警 handlers
│       ├── alert/              # 阈值评估引擎 + 飞书通知
│       └── webui/              # 内嵌 React 构建产物 (embed)
├── web/                        # React 19 + Ant Design v5 + Tailwind v4
├── deploy/                     # 生产部署：gen-certs.sh / Dockerfile / nginx.conf /
│                               #   entrypoint.sh / docker-compose.yml / .env.example / agent systemd
└── docs/                       # gRPC 契约文档（protoc-gen-doc 生成）
```

## 部署

- **单 Docker 镜像**：nginx（:443 HTTPS 终止）→ 127.0.0.1:8080（Go server，前端已内嵌）+ gRPC :9000（mTLS）；证书 volume 只读挂载**不进镜像**，DB 用命名卷持久化
- **证书**：`./deploy/gen-certs.sh ./certs 'DNS:...,IP:...'` 生成内部 CA + Server/Agent 证书（SAN 可配）；Server 用 `ca+server.crt/key`，每台 Agent 用 `ca+agent.crt/key`（0600）
- **Agent**：systemd 守护（`deploy/litesentry-agent.service`），环境文件 `/etc/litesentry/agent.env`（0600）
- **入口安全**：`entrypoint.sh` 以 `-prod` 启动，缺 `LITESENTRY_JWT_SECRET` / 证书 / token 或种子密码为 `admin` 立即退出并打印中文原因

## 实现状态（阶段一：已实现）

| 模块 | 状态 |
|---|---|
| proto（Register + Push + 预留 Stream/Series） | ✅ |
| Rust Agent（主机/容器/IPv6/自监控/注册/60s 心跳） | ✅ |
| Go Server（gRPC 鉴权、SQLite/PG、REST、JWT 用户、设置） | ✅ |
| 告警引擎 + 飞书通知（8 类指标、防抖、冷却、事件落库） | ✅ |
| React 前端（8 页面 + 登录/强制改密 + 手写 SVG 图表） | ✅ |
| 生产部署（单镜像、nginx HTTPS、mTLS、systemd、-prod 基线） | ✅ |
| 单元测试（告警引擎、认证） | ✅ 基础 |
| 暗色主题 / 阶段二插件化 | ⏸ 预留，未启用 |

---

# 第二阶段 — Agent 插件化演进（待定稿）

> 状态：**待定稿**。方向已与第一阶段架构确认兼容，信任边界需独立评审。
> 核心变化：Agent 只做 **基本心跳上报 + 插件管理 + 数据传输**；采集逻辑下沉为插件（Lua 脚本或二进制），**插件由 Server 提供并分发**。

## 兼容性结论

- **传输层 / Server 骨架 / 前端均不改**：现有 push 模型（NAT 友好）+ gRPC + TLS/mTLS 完全承接；插件分发复用同一通道（`Stream` 已预留）
- **Agent 内部重构**：从"采集器"变"传输核 + 插件宿主"（阶段一已按 core/collector 分层，降重构成本）
- **proto 扩展**：阶段一已预留 `Series` 通用字段与 `Stream` 通道，直接启用
- **安全模型升级**：新增最高风险层 —— Server 向 Agent 分发可执行代码，信任边界与阶段一不同

## 插件化后 Agent 职责

```
agent/
├── core/            # 传输核（不变）：Register + Push + TLS
├── plugin/
│   ├── manager/     # 插件生命周期：manifest 拉取→下载→校验→启停/升级
│   ├── lua/         # Lua 插件运行环境（mlua，进程内 VM）
│   ├── binary/      # 二进制插件（子进程 + JSON 行协议）
│   └── api/         # 插件 SDK：统一输出 schema
```

## 插件执行模型

| 类型 | 执行方式 | 适用 | 隔离 |
|---|---|---|---|
| **Lua 脚本** | 进程内 `mlua` VM | 轻量采集、快改快发 | 语言级受限（禁 IO/os，仅白名单 API） |
| **二进制** | 子进程 + stdin/stdout JSON 行 | 重活、自定义语言 | OS 级隔离（进程 + 可选 seccomp/Landlock） |

两种插件产出统一 JSON 契约 → Agent 解析成 `Series`。

## 协议扩展（同一 gRPC 通道）

```proto
service Control {
  rpc Heartbeat(HeartbeatReq) returns (HeartbeatResp);   // 响应含插件 manifest 期望版本
  rpc FetchPlugin(PluginReq) returns (stream Chunk);     // Agent 拉插件（含校验和）
  rpc ReportPluginStatus(PluginStatusReq) returns (Ack); // 插件运行状态回传
}
```

> 分发走 **Agent 主动拉取**（心跳响应带 manifest 期望版本，Agent 发现缺/旧即发起下载）——Agent 在 NAT 后 Server 不能反推，靠心跳驱动契合现有 push 模型。

## Server 新增：插件仓库

- `internal/plugin/`：插件注册（上传、版本、manifest）、指派（哪个插件发给哪台 Agent）、签名
- 插件为**不可变发布物**：只允许新增版本，不可在线修改

## 安全模型（最关键，独立评审）

**Server 向 Agent 分发可执行代码 = 变相远程执行能力。Server 被攻破 → 所有 Agent 沦陷。** 缓解措施：

1. **插件签名**：Server 私钥签名，Agent 内置公钥验签（防仓库污染 / 中间人）
2. **双校验**：签名 + 校验和；Agent 只认 manifest 白名单内 `plugin_id + version`，未知插件拒绝执行
3. **仅管理员上传**：上传入口二次鉴权
4. **沙箱**：二进制插件默认无网络出站；Lua 禁全系统调用；后续可上 seccomp
5. **失败隔离**：插件崩溃/死循环不影响 Agent 心跳；超时强杀、输出截断

## 阶段二实现路线（待定稿）

1. Server 插件仓库 + 签名/校验和 + manifest 指派
2. proto 扩展 `Control` 服务 + `Series` 启用
3. Agent 插件管理器（拉取/校验/生命周期）
4. Lua 运行时（mlua + 白名单 API）
5. 二进制插件子进程协议 + 沙箱
6. 内置插件化：host / docker 迁移为 Server 下发的内置插件
