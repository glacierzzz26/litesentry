// Package store 统一存储抽象：SQLite（默认）与 PostgreSQL（可选）。
//
// 业务层（gRPC / API / 告警）只依赖 Store 接口，不感知后端；
// 轻量监控假设（SQLite 单文件）只留在这个包内，Pro 版可整体替换后端（如 ClickHouse）。
package store

import (
	"context"
	"time"
)

// IPAddr 地址条目（写入 agents 表快照）
type IPAddr struct {
	Family string `json:"family"` // ipv4 | ipv6
	Addr   string `json:"addr"`
	Iface  string `json:"iface"`
	Scope  string `json:"scope"` // global | link | loopback
}

// Agent 节点最新状态
type Agent struct {
	AgentID   string    `json:"agent_id"`
	Hostname  string    `json:"hostname"`
	OS        string    `json:"os"`
	Arch      string    `json:"arch"`
	Kernel    string    `json:"kernel"`
	Version   string    `json:"version"` // Agent 构建版本（注册上报）
	MachineID string    `json:"-"`       // 机器指纹，仅服务端内部使用，不外泄
	IPv4      []IPAddr  `json:"ipv4"`
	IPv6      []IPAddr  `json:"ipv6"`
	LastSeen  time.Time `json:"last_seen"`
	CreatedAt time.Time `json:"created_at"`
	// Status 由 API 层计算（服务端权威）：online | offline，预留 upgrading 等扩展状态。
	Status string `json:"status"`
}

// HostSample 主机时序样本
type HostSample struct {
	AgentID     string    `json:"agent_id"`
	Ts          time.Time `json:"ts"`
	Hostname    string    `json:"hostname"`
	OS          string    `json:"os"`
	Arch        string    `json:"arch"`
	Kernel      string    `json:"kernel"`
	UptimeS     uint64    `json:"uptime_s"`
	Load1       float64   `json:"load_1m"`
	Load5       float64   `json:"load_5m"`
	CPUPct      float64   `json:"cpu_pct"`
	AgentCPUPct float64   `json:"agent_cpu_pct"` // Agent 自身进程 CPU 占用（%）
	AgentMemRSS uint64    `json:"agent_mem_rss"` // Agent 自身 RSS（字节）
	MemTotal    uint64    `json:"mem_total"`
	MemUsed     uint64    `json:"mem_used"`
	SwapTotal   uint64    `json:"swap_total"`
	SwapUsed    uint64    `json:"swap_used"`
	NetRXBps    uint64    `json:"net_rx_bps"`
	NetTXBps    uint64    `json:"net_tx_bps"`
}

// DiskSample 磁盘时序样本
type DiskSample struct {
	AgentID string    `json:"agent_id"`
	Ts      time.Time `json:"ts"`
	Mount   string    `json:"mount"`
	FS      string    `json:"fs"`
	Total   uint64    `json:"total"`
	Used    uint64    `json:"used"`
}

// ContainerSample 容器时序样本
type ContainerSample struct {
	AgentID     string    `json:"agent_id"`
	Ts          time.Time `json:"ts"`
	ContainerID string    `json:"container_id"`
	Name        string    `json:"name"`
	Image       string    `json:"image"`
	State       string    `json:"state"`
	Restarts    uint32    `json:"restarts"`
	UptimeS     uint64    `json:"uptime_s"`
	CPUPct      float64   `json:"cpu_pct"`
	MemUsage    uint64    `json:"mem_usage"`
	MemLimit    uint64    `json:"mem_limit"`
	NetRXBps    uint64    `json:"net_rx_bps"`
	NetTXBps    uint64    `json:"net_tx_bps"`
}

// AlertRule 告警规则。
type AlertRule struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Metric    string    `json:"metric"` // cpu_pct | mem_pct | load_1m | disk_pct | container_cpu | container_mem | container_down | offline
	Op        string    `json:"op"`     // > | <
	Threshold float64   `json:"threshold"`
	DurationS int64     `json:"duration_s"` // 持续超过阈值多久才触发（秒），0 = 立即
	Severity  string    `json:"severity"`   // warning | critical
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AlertEvent 告警事件（firing 触发 / resolved 恢复）。
type AlertEvent struct {
	ID         string     `json:"id"`
	RuleID     string     `json:"rule_id"`
	RuleName   string     `json:"rule_name"`
	AgentID    string     `json:"agent_id"`
	AgentName  string     `json:"agent_name"`
	EntityID   string     `json:"entity_id"`   // 容器规则 = 容器 id；主机/离线规则为空
	EntityName string     `json:"entity_name"` // 容器规则 = 容器名称
	Metric     string     `json:"metric"`
	Value      float64    `json:"value"`
	Threshold  float64    `json:"threshold"`
	Severity   string     `json:"severity"` // warning | critical
	State      string     `json:"state"`    // firing | resolved
	StartedAt  time.Time  `json:"started_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	NotifiedAt *time.Time `json:"notified_at,omitempty"`
}

// Overview 总览聚合：一次返回前端总览页所需的全量数据，
// 消除按节点逐个扇出请求（N+1）。
//
//   - Hosts：窗口内每节点最近一条主机样本（CPU/内存/负载/网络/Agent 自耗）
//   - DiskMax：每节点最高挂载点使用率（%），agent_id → 百分比
//   - Firing：当前仍触发中（state=firing）的告警事件数
type Overview struct {
	Hosts   []*HostSample      `json:"hosts"`    // 每节点最近一条主机样本
	DiskMax map[string]float64 `json:"disk_max"` // agent_id → 最高挂载点使用率%
	Firing  int                `json:"firing"`   // 当前 firing 事件数
}

// User 面板登录用户（阶段一全 admin 角色，列预留扩展）。
// PasswordHash 存 bcrypt 哈希，绝不通过 JSON 回传（json:"-"）。
type User struct {
	ID             string     `json:"id"`
	Username       string     `json:"username"`
	DisplayName    string     `json:"display_name"`
	Role           string     `json:"role"`                // 阶段一全 admin，列预留
	PasswordHash   string     `json:"-"`                   // bcrypt，绝不回传
	MustChangePwd  bool       `json:"must_change_password"` // 首次登录强制改密
	LastLoginAt    *time.Time `json:"last_login_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// ---- 阶段二：插件 / 任务 / frp 数据模型 ----

// Series 通用时序数据（插件统一输出，落 series 表；阶段一预留字段正式启用）。
type Series struct {
	Name   string             `json:"name"`
	Tags   map[string]string  `json:"tags,omitempty"`
	Fields map[string]float64 `json:"fields"`
	TS     time.Time          `json:"ts"`
}

// Plugin 插件仓库条目：不可变发布物，只允许新增版本（UNIQUE(id, version)）。
type Plugin struct {
	ID         string    `json:"id"`    // 插件标识（稳定跨版本），如 "hello"
	Name       string    `json:"name"`  // 展示名
	Kind       string    `json:"kind"`  // 阶段二仅 binary
	Version    string    `json:"version"`
	SHA256     string    `json:"sha256"`
	Size       int64     `json:"size"`
	ArgsSchema string    `json:"args_schema,omitempty"` // 参数说明（前端表单用）
	CreatedAt  time.Time `json:"created_at"`
	Data       []byte    `json:"-"` // 二进制内容，GetPlugin 返回；List 不填充
}

// AgentPlugin 插件指派：某 agent 应运行的 manifest 条目（DesiredState.plugins 来源）。
type AgentPlugin struct {
	AgentID  string `json:"agent_id"`
	PluginID string `json:"plugin_id"`
	Version  string `json:"version"`
	ArgsJSON string `json:"args_json"`
}

// Task 定时任务定义：执行全在目标 agent 侧（本地 cron → 插件 run）。
type Task struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Description    string     `json:"description"`
	TargetAgentID  string     `json:"target_agent_id"` // 空 = 公网机（server 同机 agent）
	Cron           string     `json:"cron"`
	PluginID       string     `json:"plugin_id"`
	ArgsJSON       string     `json:"args_json"`
	TimeoutS       uint32     `json:"timeout_s"`
	Enabled        bool       `json:"enabled"`
	RunNow         bool       `json:"run_now"` // 立即运行标记：POST /run 置位，agent 下心跳执行一次后 Server 清除
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	LastStatus     string     `json:"last_status"` // ok | failed | timeout | skipped
	LastOutputTail string     `json:"last_output_tail"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// TaskRun 定时任务执行历史（审计）：agent 上报结果，append-only。
type TaskRun struct {
	ID         string     `json:"id"`
	TaskID     string     `json:"task_id"`
	AgentID    string     `json:"agent_id"`
	Status     string     `json:"status"` // ok | failed | timeout | skipped
	ExitCode   *int32     `json:"exit_code,omitempty"`
	Output     string     `json:"output"` // stdout/stderr 截断尾部
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// FrpTunnel 隧道配置项（frpc proxies 元素）。
type FrpTunnel struct {
	Name       string `json:"name"`
	Type       string `json:"type"` // tcp | udp | http | https | stcp ...
	LocalIP    string `json:"local_ip,omitempty"`
	LocalPort  int    `json:"local_port,omitempty"`
	RemotePort int    `json:"remote_port,omitempty"`
}

// FrpConfig 本机 frp 配置（frps 或某 agent 的 frpc，按 kind+agent 区分）。
type FrpConfig struct {
	Kind         string      `json:"kind"` // frps | frpc
	AgentID      string      `json:"agent_id"`
	ServerAddr   string      `json:"server_addr"`
	ServerPort   int         `json:"server_port"`
	Token        string      `json:"token"` // 仅 gRPC 已鉴权通道下发，绝不过 REST 回传明文
	Proxies      []FrpTunnel `json:"proxies"`
	StateVersion uint64      `json:"state_version"`
	Enabled      bool        `json:"enabled"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

// FrpTunnelStatus 隧道实时状态（agent 上报）。
type FrpTunnelStatus struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Status  string `json:"status"` // online | offline | error
	Err     string `json:"err,omitempty"`
	RXBytes uint64 `json:"rx_bytes"`
	TXBytes uint64 `json:"tx_bytes"`
}

// FrpStatus 本机 frp 进程 + 隧道整体状态（agent 上报，每次 Push 覆盖写）。
type FrpStatus struct {
	Running    bool              `json:"running"`
	FrpVersion string            `json:"frp_version"`
	Error      string            `json:"error"`
	Tunnels    []FrpTunnelStatus `json:"tunnels"`
	TS         time.Time         `json:"ts"`
}

// Store 统一存储接口。
type Store interface {
	// RegisterAgent 按机器指纹注册：已注册过则复用其 agent_id，否则新建 UUID。
	// 返回 agent_id 与是否新建。
	RegisterAgent(ctx context.Context, machineID string, a *Agent) (agentID string, isNew bool, err error)
	// UpsertAgent 注册/更新节点最新状态（含 IPv4/IPv6 地址快照）。
	UpsertAgent(ctx context.Context, a *Agent) error
	// TouchAgentLastSeen 仅刷新节点最近心跳时间（不覆盖元信息）。
	// 用于采集与心跳解耦后：无 series 的空心跳也须刷新在线态，避免节点误判离线。
	TouchAgentLastSeen(ctx context.Context, agentID string, ts time.Time) error
	// AppendBatch 将一批（主机 + 磁盘 + 容器）时序样本写入，单事务。
	AppendBatch(ctx context.Context, h *HostSample, disks []*DiskSample, containers []*ContainerSample) error
	// QueryHost 查询某节点一段窗口内的主机样本。
	QueryHost(ctx context.Context, agentID string, from, to time.Time) ([]*HostSample, error)
	// QueryContainers 查询某节点一段窗口内的容器样本。
	QueryContainers(ctx context.Context, agentID string, from, to time.Time) ([]*ContainerSample, error)
	// QueryDisks 查询某节点一段窗口内的磁盘样本（每条 = 一个挂载点的最近占用）。
	QueryDisks(ctx context.Context, agentID string, from, to time.Time) ([]*DiskSample, error)
	// Agents 返回全部节点最新状态（面板节点列表）。
	Agents(ctx context.Context) ([]*Agent, error)
	// Overview 一次返回总览聚合数据（每节点最新主机样本 + 每节点最高磁盘占用 + firing 事件数），
	// 消除前端总览页的 N+1 扇出。
	Overview(ctx context.Context, from, to time.Time) (*Overview, error)

	// GetSetting 读通用设置（不存在返回空串）。SetSetting 写。
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error

	// ---- 用户（面板登录，密码已 bcrypt 哈希）----
	// CreateUser 新建用户。UpdateUser 更新 display_name/role/password_hash/must_change_password。
	CreateUser(ctx context.Context, u *User) error
	UpdateUser(ctx context.Context, u *User) error
	// GetUserByID / GetUserByUsername 按主键查询。
	GetUserByID(ctx context.Context, id string) (*User, error)
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	// ListUsers 全部用户（JSON 不含密码哈希）。DeleteUser 按 id 删除。
	ListUsers(ctx context.Context) ([]*User, error)
	DeleteUser(ctx context.Context, id string) error
	// CountUsers 用户总数（bootstrap 种子判断用）。
	CountUsers(ctx context.Context) (int, error)
	// SetLastLogin 回写上次登录时间。
	SetLastLogin(ctx context.Context, id string, at time.Time) error

	// ListRules 返回全部告警规则。SaveRule 新建（ID 为空则分配）或更新。
	ListRules(ctx context.Context) ([]*AlertRule, error)
	SaveRule(ctx context.Context, r *AlertRule) error
	DeleteRule(ctx context.Context, id string) error

	// AppendEvent 写入告警事件。QueryEvents 查询（可按节点/状态过滤，limit 取最近 N 条）。
	AppendEvent(ctx context.Context, e *AlertEvent) error
	QueryEvents(ctx context.Context, from, to time.Time, agentID, state string, limit int) ([]*AlertEvent, error)
	// ResolveEvent 将 firing 事件标记为 resolved（幂等：仅更新仍 firing 的）。
	ResolveEvent(ctx context.Context, id string, at time.Time) error
	// SetEventNotified 回写通知时间（通知成功/重发时调用）。
	SetEventNotified(ctx context.Context, id string, at time.Time) error

	// ---- 插件仓库（阶段二）----
	// SavePlugin 登记一个不可变插件版本（同 id+version 已存在则报错）。
	SavePlugin(ctx context.Context, p *Plugin) error
	// ListPlugins 返回全部插件（不含二进制内容，Data 为空）。GetPlugin 返回元信息 + 二进制。
	ListPlugins(ctx context.Context) ([]*Plugin, error)
	GetPlugin(ctx context.Context, id, version string) (*Plugin, error)
	// DeletePluginVersion 删除某插件版本；若仍被任一 agent 指派则报错（须先取消指派）。
	DeletePluginVersion(ctx context.Context, id, version string) error

	// ---- 插件指派（manifest 来源）----
	// AssignPlugin 指派插件到某 agent（覆盖同插件旧指派）。AgentPlugins 列出该 agent 的 manifest。
	AssignPlugin(ctx context.Context, ap *AgentPlugin) error
	AgentPlugins(ctx context.Context, agentID string) ([]*AgentPlugin, error)
	// UnassignPlugin 取消某 agent 上某插件的指派。
	UnassignPlugin(ctx context.Context, agentID, pluginID string) error

	// ---- 插件 Series（阶段二）----
	// AppendSeries 将一批插件产出的通用时序写入 series 表（单事务）。
	AppendSeries(ctx context.Context, agentID string, items []*Series) error

	// ---- 定时任务（阶段二，执行在 agent 侧）----
	SaveTask(ctx context.Context, t *Task) error
	ListTasks(ctx context.Context) ([]*Task, error)
	GetTask(ctx context.Context, id string) (*Task, error)
	DeleteTask(ctx context.Context, id string) error
	// AppendTaskRun 记录一次执行历史（审计）。QueryTaskRuns 查最近 limit 条。
	AppendTaskRun(ctx context.Context, r *TaskRun) error
	QueryTaskRuns(ctx context.Context, taskID string, limit int) ([]*TaskRun, error)
	// UpdateTaskLastRun 回写任务最近一次执行的摘要（面板列表用，避免 join）。
	UpdateTaskLastRun(ctx context.Context, taskID, status, outputTail string, at time.Time) error
	// SetTaskRunNow 置位/清除任务「立即运行」标记（POST /run 置位，收到执行报告后清除）。
	SetTaskRunNow(ctx context.Context, taskID string, runNow bool) error

	// ---- frp 配置 / 状态（阶段二）----
	SaveFrpConfig(ctx context.Context, c *FrpConfig) error
	GetFrpConfig(ctx context.Context, kind, agentID string) (*FrpConfig, error)
	ListFrpConfigs(ctx context.Context) ([]*FrpConfig, error)
	// UpsertFrpStatus 覆盖写某 agent 最近一次 frp 状态。QueryFrpStatus 读最近一次。
	UpsertFrpStatus(ctx context.Context, agentID string, s *FrpStatus) error
	QueryFrpStatus(ctx context.Context, agentID string) (*FrpStatus, error)
	// DeleteFrpConfig 删除一条 frp 配置（frps 行的 agent_id=''）。删除后下次 DesiredState 不带 frp → agent 停进程。
	DeleteFrpConfig(ctx context.Context, kind, agentID string) error

	// Cleanup 删除超过保留期（默认 30 天）的时序数据，返回清理行数。
	Cleanup(ctx context.Context, retention time.Duration) (int64, error)
	Close() error
}
