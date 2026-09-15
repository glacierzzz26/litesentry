// 数据模型 —— 与 server/internal/store 的 JSON 结构一一对应（Go 结构体序列化）。
// 重写阶段用 mock 数据；结构保持对齐，后续接真实接口零迁移。

export interface IPAddr {
  family: string; // ipv4 | ipv6
  addr: string;
  iface: string;
  scope: string; // global | link | loopback
}

export interface Agent {
  agent_id: string;
  hostname: string;
  os: string;
  arch: string;
  kernel: string;
  version: string; // Agent 构建版本（注册上报）
  ipv4: IPAddr[];
  ipv6: IPAddr[];
  last_seen: string; // RFC3339
  created_at: string;
  status: string; // 服务端权威判定：online | offline，预留 upgrading 等
}

export interface HostSample {
  agent_id: string;
  ts: string;
  hostname: string;
  os: string;
  arch: string;
  kernel: string;
  uptime_s: number;
  load_1m: number;
  load_5m: number;
  cpu_pct: number;
  agent_cpu_pct: number; // Agent 自身进程 CPU 占用（%）
  agent_mem_rss: number; // Agent 自身 RSS（字节）
  mem_total: number;
  mem_used: number;
  swap_total: number;
  swap_used: number;
  net_rx_bps: number;
  net_tx_bps: number;
}

export interface DiskSample {
  agent_id: string;
  ts: string;
  mount: string;
  fs: string;
  total: number;
  used: number;
}

export interface ContainerSample {
  agent_id: string;
  ts: string;
  container_id: string;
  name: string;
  image: string;
  state: string;
  restarts: number;
  uptime_s: number;
  cpu_pct: number;
  mem_usage: number;
  mem_limit: number;
  net_rx_bps: number;
  net_tx_bps: number;
}

/** 取某窗口内每个实体的最近一条（按 ts 归并）。 */
export function latestByKey<T extends { ts: string }>(rows: T[], key: (r: T) => string): Map<string, T> {
  const out = new Map<string, T>();
  for (const r of rows) {
    const k = key(r);
    const cur = out.get(k);
    if (!cur || new Date(r.ts).getTime() > new Date(cur.ts).getTime()) out.set(k, r);
  }
  return out;
}

// ---- 总览聚合 ----

export interface Overview {
  hosts: HostSample[]; // 每节点最近一条主机样本
  disk_max: Record<string, number>; // agent_id → 最高挂载点使用率%
  firing: number; // 当前 firing 事件数
}

// ---- 告警 ----

export interface AlertRule {
  id: string;
  name: string;
  metric: string; // cpu_pct | mem_pct | load_1m | disk_pct | container_cpu | container_mem | container_down | offline
  op: string; // > | <
  threshold: number;
  duration_s: number; // 持续超过阈值多久才触发（秒），0 = 立即
  severity: string; // warning | critical
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface AlertEvent {
  id: string;
  rule_id: string;
  rule_name: string;
  agent_id: string;
  agent_name: string;
  entity_id: string; // 容器规则 = 容器 id；主机/离线规则为空
  entity_name: string; // 容器规则 = 容器名称
  metric: string;
  value: number;
  threshold: number;
  severity: string; // warning | critical
  state: string; // firing | resolved
  started_at: string;
  resolved_at?: string;
  notified_at?: string;
}

// ---- 用户 / 认证 ----

export interface User {
  id: string;
  username: string;
  display_name: string;
  role: string; // 阶段一全 admin，列预留
  must_change_password: boolean; // 首登强制改密
  last_login_at?: string; // RFC3339；从未登录 = 无
  created_at: string;
  updated_at: string;
}

export interface LoginResult {
  token: string;
  expires_at: string;
  must_change_password: boolean;
  user: User;
}

// ---- 设置 ----

export interface SettingsView {
  feishu_webhook: string;
  feishu_secret_set: boolean;
}

export interface SettingsBody {
  feishu_webhook: string;
  feishu_secret?: string;
  feishu_secret_clear?: boolean;
}

// ---- FRP 隧道 ----

/** frpc 隧道配置项（渲染进 [[proxies]]）。 */
export interface FrpTunnel {
  name: string;
  type: string; // tcp | udp | http | https | stcp ...
  local_ip?: string;
  local_port?: number;
  remote_port?: number;
}

/** 单隧道实时状态（agent 经 admin API 轮询上报）。 */
export interface FrpTunnelStatus {
  name: string;
  type: string;
  status: string; // online | offline | error
  err?: string;
  rx_bytes: number;
  tx_bytes: number;
}

/** 本机 frp 进程 + 隧道整体状态（每次 Push 覆盖写）。 */
export interface FrpStatus {
  running: boolean;
  frp_version: string;
  error: string;
  tunnels: FrpTunnelStatus[];
  ts: string;
}

/** FRP 配置视图（REST 返回，token 不回传）。agent_id 前端不展示，仅用于 PUT/DELETE 路径。 */
export interface FrpConfigView {
  kind: string; // frps | frpc
  agent_id: string; // frps 行恒为 "server"（哨兵）
  agent_name: string;
  server_addr: string;
  server_port: number;
  proxies: FrpTunnel[];
  enabled: boolean;
  token_set: boolean;
  updated_at: string;
  status?: FrpStatus;
}

/** FRP 配置写入体。token 为空 = 保留原值。 */
export interface SaveFrpBody {
  server_addr: string;
  server_port: number;
  token?: string;
  proxies: FrpTunnel[];
  enabled: boolean;
}

// ---- 定时任务（阶段二 S4）----

/** 定时任务定义（REST 返回）。target_agent_id 前端不展示，仅用于编辑回填（空 = 公网机）。 */
export interface Task {
  id: string;
  name: string;
  description: string;
  target_agent_id: string; // 空 = 公网机（server 同机 agent）
  target_agent_name: string;
  cron: string;
  plugin_id: string;
  args_json: string;
  timeout_s: number;
  enabled: boolean;
  run_now?: boolean; // 立即运行标记：已触发（等 agent 执行报告回清）时为 true
  last_run_at?: string;
  last_status: string; // ok | failed | timeout | skipped
  last_output_tail: string;
  created_at: string;
  updated_at: string;
}

/** 任务运行历史（agent 上报，append-only 审计）。 */
export interface TaskRun {
  id: string;
  task_id: string;
  agent_id: string;
  status: string; // ok | failed | timeout | skipped
  exit_code?: number;
  output: string; // stdout/stderr 截断尾部
  started_at: string;
  finished_at?: string;
}

/** 任务写入体（target_agent_id 空 = 公网机）。 */
export interface SaveTaskBody {
  name: string;
  description?: string;
  target_agent_id: string;
  cron: string;
  plugin_id: string;
  args_json?: string;
  timeout_s?: number;
  enabled?: boolean;
}

/** 目标 agent 已指派的插件（任务表单插件下拉数据源）。 */
export interface AgentPlugin {
  agent_id: string;
  plugin_id: string;
  version: string;
  args_json: string;
}

// ---- 插件仓库（阶段二）----

/** 插件仓库条目（不可变版本；同 id 可并存多版本）。 */
export interface Plugin {
  id: string; // 插件标识（稳定跨版本）
  name: string; // 展示名
  kind: string; // 阶段二仅 binary
  version: string;
  sha256: string;
  size: number; // 字节
  args_schema?: string; // 参数说明（JSON）
  created_at: string;
}

/** 插件上传体（multipart）。 */
export interface SavePluginBody {
  id: string;
  name: string;
  version: string;
  args_schema?: string;
}

/** 插件指派体（agent_id 为内部标识，前端展示主机名）。 */
export interface AssignPluginBody {
  agent_id: string;
  version: string;
  args_json?: string;
}
