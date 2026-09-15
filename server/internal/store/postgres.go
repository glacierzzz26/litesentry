package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx 标准库驱动
)

// Postgres 可选存储实现（--db=postgres://...）。
// 与 SQLite 同构，仅占位符（$N）与幂等写入（ON CONFLICT DO NOTHING）不同。
type Postgres struct {
	db *sql.DB
}

func NewPostgres(dsn string) (*Postgres, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	p := &Postgres{db: db}
	if err := p.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return p, nil
}

const pgSchema = `
CREATE TABLE IF NOT EXISTS agents (
  agent_id   TEXT PRIMARY KEY,
  hostname   TEXT NOT NULL,
  os         TEXT NOT NULL DEFAULT '',
  arch       TEXT NOT NULL DEFAULT '',
  kernel     TEXT NOT NULL DEFAULT '',
  version    TEXT NOT NULL DEFAULT '',  -- Agent 构建版本（注册上报）
  machine_id TEXT,               -- 机器指纹（注册复用）
  ipv4       TEXT NOT NULL DEFAULT '[]',
  ipv6       TEXT NOT NULL DEFAULT '[]',
  last_seen  BIGINT NOT NULL,
  created_at BIGINT NOT NULL
);
-- 机器指纹唯一索引由 migrate() 在列迁移完成后创建（旧库 agents 表可能缺 machine_id 列）

CREATE TABLE IF NOT EXISTS host_metrics (
  agent_id TEXT NOT NULL,
  ts       BIGINT NOT NULL,
  hostname TEXT NOT NULL,
  os TEXT, arch TEXT, kernel TEXT,
  uptime_s BIGINT,
  load_1m REAL, load_5m REAL, cpu_pct REAL,
  agent_cpu_pct REAL, agent_mem_rss BIGINT,
  mem_total BIGINT, mem_used BIGINT, swap_total BIGINT, swap_used BIGINT,
  net_rx_bps BIGINT, net_tx_bps BIGINT
);
CREATE INDEX IF NOT EXISTS idx_host_agent_ts ON host_metrics (agent_id, ts);

CREATE TABLE IF NOT EXISTS disk_metrics (
  agent_id TEXT NOT NULL,
  ts       BIGINT NOT NULL,
  mount    TEXT NOT NULL,
  fs       TEXT,
  total BIGINT, used BIGINT,
  PRIMARY KEY (agent_id, ts, mount)
);

CREATE TABLE IF NOT EXISTS container_metrics (
  agent_id TEXT NOT NULL,
  ts       BIGINT NOT NULL,
  container_id TEXT NOT NULL,
  name TEXT, image TEXT, state TEXT,
  restarts INTEGER,
  uptime_s BIGINT, cpu_pct REAL,
  mem_usage BIGINT, mem_limit BIGINT,
  net_rx_bps BIGINT, net_tx_bps BIGINT,
  PRIMARY KEY (agent_id, ts, container_id)
);

CREATE TABLE IF NOT EXISTS settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL DEFAULT '',
  updated_at BIGINT
);

CREATE TABLE IF NOT EXISTS users (
  id                   TEXT PRIMARY KEY,
  username             TEXT NOT NULL UNIQUE,
  password_hash        TEXT NOT NULL,           -- bcrypt，绝不回传
  display_name         TEXT NOT NULL DEFAULT '',
  role                 TEXT NOT NULL DEFAULT 'admin',
  must_change_password BOOLEAN NOT NULL DEFAULT FALSE,
  last_login_at        BIGINT,                  -- 上次登录 unix 秒，NULL = 从未登录
  created_at           BIGINT NOT NULL,
  updated_at           BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS alert_rules (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  metric     TEXT NOT NULL,
  op         TEXT NOT NULL DEFAULT '>',
  threshold  REAL NOT NULL DEFAULT 0,
  duration_s BIGINT NOT NULL DEFAULT 0,
  severity   TEXT NOT NULL DEFAULT 'warning',
  enabled    BOOLEAN NOT NULL DEFAULT TRUE,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS alert_events (
  id          TEXT PRIMARY KEY,
  rule_id     TEXT,
  rule_name   TEXT NOT NULL,
  agent_id    TEXT NOT NULL,
  agent_name  TEXT NOT NULL DEFAULT '',
  entity_id   TEXT NOT NULL DEFAULT '',
  entity_name TEXT NOT NULL DEFAULT '',
  metric      TEXT NOT NULL,
  value       REAL,
  threshold   REAL,
  severity    TEXT NOT NULL DEFAULT 'warning',
  state       TEXT NOT NULL DEFAULT 'firing',
  started_at  BIGINT NOT NULL,
  resolved_at BIGINT,
  notified_at BIGINT
);
CREATE INDEX IF NOT EXISTS idx_events_agent_ts ON alert_events (agent_id, started_at);
CREATE INDEX IF NOT EXISTS idx_events_state  ON alert_events (state, started_at);

-- ============ 阶段二：插件 / 任务 / frp ============

CREATE TABLE IF NOT EXISTS plugins (
  id TEXT NOT NULL, name TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'binary',
  version TEXT NOT NULL, sha256 TEXT NOT NULL, size BIGINT NOT NULL DEFAULT 0,
  args_schema TEXT DEFAULT '', data BYTEA NOT NULL,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (id, version)            -- 不可变版本：同 id 可并存多版本，只允许新增
);
CREATE TABLE IF NOT EXISTS agent_plugins (
  agent_id TEXT NOT NULL, plugin_id TEXT NOT NULL, version TEXT NOT NULL,
  args_json TEXT DEFAULT '',
  PRIMARY KEY (agent_id, plugin_id)
);
CREATE TABLE IF NOT EXISTS series (
  agent_id TEXT NOT NULL, ts BIGINT NOT NULL, name TEXT NOT NULL,
  tags TEXT NOT NULL DEFAULT '{}', fields TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_series_agent_name_ts ON series (agent_id, name, ts);
CREATE INDEX IF NOT EXISTS idx_series_ts ON series (ts);
CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT DEFAULT '',
  target_agent_id TEXT DEFAULT '', cron TEXT NOT NULL, plugin_id TEXT NOT NULL,
  args_json TEXT DEFAULT '', timeout_s BIGINT NOT NULL DEFAULT 60,
  enabled BOOLEAN NOT NULL DEFAULT TRUE, run_now BOOLEAN NOT NULL DEFAULT FALSE,
  last_run_at BIGINT, last_status TEXT DEFAULT '',
  last_output_tail TEXT DEFAULT '', created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS task_runs (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL, agent_id TEXT NOT NULL,
  status TEXT NOT NULL, exit_code INTEGER, output TEXT DEFAULT '',
  started_at BIGINT NOT NULL, finished_at BIGINT
);
CREATE INDEX IF NOT EXISTS idx_task_runs_task ON task_runs (task_id, started_at);
CREATE INDEX IF NOT EXISTS idx_task_runs_started ON task_runs (started_at);
CREATE TABLE IF NOT EXISTS frp_configs (
  kind TEXT NOT NULL, agent_id TEXT NOT NULL DEFAULT '',
  server_addr TEXT DEFAULT '', server_port INTEGER DEFAULT 7000, token TEXT DEFAULT '',
  proxies TEXT NOT NULL DEFAULT '[]', state_version BIGINT NOT NULL DEFAULT 1,
  enabled BOOLEAN NOT NULL DEFAULT FALSE, updated_at BIGINT NOT NULL,
  PRIMARY KEY (kind, agent_id)
);
CREATE TABLE IF NOT EXISTS frp_status (
  agent_id TEXT PRIMARY KEY, running BOOLEAN NOT NULL DEFAULT FALSE,
  frp_version TEXT DEFAULT '', error TEXT DEFAULT '',
  tunnels TEXT NOT NULL DEFAULT '[]', ts BIGINT NOT NULL
);
`

func (p *Postgres) migrate() error {
	if _, err := p.db.Exec(pgSchema); err != nil {
		return err
	}
	// 对已存在（旧 schema）的库做列迁移（PG 原生支持 ADD COLUMN IF NOT EXISTS）
	for _, q := range []string{
		`ALTER TABLE host_metrics ADD COLUMN IF NOT EXISTS agent_cpu_pct REAL`,
		`ALTER TABLE host_metrics ADD COLUMN IF NOT EXISTS agent_mem_rss BIGINT`,
		`ALTER TABLE agents ADD COLUMN IF NOT EXISTS machine_id TEXT`,
		`ALTER TABLE agents ADD COLUMN IF NOT EXISTS version TEXT`,
		`ALTER TABLE tasks ADD COLUMN IF NOT EXISTS run_now BOOLEAN NOT NULL DEFAULT FALSE`,
	} {
		if _, err := p.db.Exec(q); err != nil {
			return err
		}
	}
	// 回填：老库 ALTER 加列的 version 为 NULL，归一化为空串
	if _, err := p.db.Exec(`UPDATE agents SET version = COALESCE(version, '') WHERE version IS NULL`); err != nil {
		return err
	}
	_, err := p.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_machine ON agents (machine_id)`)
	if err != nil {
		return err
	}
	return p.migratePluginsPK()
}

// migratePluginsPK 把 plugins 主键从 id 单列迁移到 (id, version) 复合（让同 id 可存多版本）。
// 幂等：仅当现主键列数为 1 时重建约束（旧库升级路径）。
func (p *Postgres) migratePluginsPK() error {
	var ncols int
	err := p.db.QueryRow(`
SELECT array_length(conkey, 1)
FROM pg_constraint
WHERE conrelid = 'plugins'::regclass AND contype = 'p'`).Scan(&ncols)
	if err != nil {
		return nil // 无主键记录时跳过（新库已由 pgSchema 建好复合主键）
	}
	if ncols > 1 {
		return nil // 已是复合主键
	}
	stmts := []string{
		`ALTER TABLE plugins DROP CONSTRAINT plugins_pkey`,
		`ALTER TABLE plugins ADD PRIMARY KEY (id, version)`,
	}
	for _, q := range stmts {
		if _, err := p.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func (p *Postgres) UpsertAgent(ctx context.Context, a *Agent) error {
	ipv4, _ := json.Marshal(a.IPv4)
	ipv6, _ := json.Marshal(a.IPv6)
	_, err := p.db.ExecContext(ctx, `
INSERT INTO agents (agent_id, hostname, os, arch, kernel, ipv4, ipv6, last_seen, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (agent_id) DO UPDATE SET
  hostname=EXCLUDED.hostname, os=EXCLUDED.os, arch=EXCLUDED.arch, kernel=EXCLUDED.kernel,
  ipv4=EXCLUDED.ipv4, ipv6=EXCLUDED.ipv6, last_seen=EXCLUDED.last_seen`,
		a.AgentID, a.Hostname, a.OS, a.Arch, a.Kernel,
		string(ipv4), string(ipv6), a.LastSeen.Unix(), a.CreatedAt.Unix())
	return err
}

func (p *Postgres) TouchAgentLastSeen(ctx context.Context, agentID string, ts time.Time) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE agents SET last_seen = $1 WHERE agent_id = $2`, ts.Unix(), agentID)
	return err
}

func (p *Postgres) RegisterAgent(ctx context.Context, machineID string, a *Agent) (string, bool, error) {
	var existing string
	err := p.db.QueryRowContext(ctx,
		`SELECT agent_id FROM agents WHERE machine_id = $1`, machineID).Scan(&existing)
	switch {
	case err == nil:
		// 复用历史 agent_id：同步更新元信息与在线时间
		_, uerr := p.db.ExecContext(ctx, `
UPDATE agents SET hostname=$1, os=$2, arch=$3, kernel=$4, version=$5, last_seen=$6
WHERE agent_id=$7`,
			a.Hostname, a.OS, a.Arch, a.Kernel, a.Version, a.LastSeen.Unix(), existing)
		return existing, false, uerr
	case err != sql.ErrNoRows:
		return "", false, err
	}
	id := newUUID()
	_, err = p.db.ExecContext(ctx, `
INSERT INTO agents (agent_id, hostname, os, arch, kernel, version, machine_id, last_seen, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		id, a.Hostname, a.OS, a.Arch, a.Kernel, a.Version, machineID, a.LastSeen.Unix(), a.CreatedAt.Unix())
	return id, true, err
}

func (p *Postgres) AppendBatch(ctx context.Context, h *HostSample, disks []*DiskSample, containers []*ContainerSample) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if h != nil {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO host_metrics
  (agent_id, ts, hostname, os, arch, kernel, uptime_s,
   load_1m, load_5m, cpu_pct,
   agent_cpu_pct, agent_mem_rss,
   mem_total, mem_used, swap_total, swap_used,
   net_rx_bps, net_tx_bps)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
			h.AgentID, h.Ts.Unix(), h.Hostname, h.OS, h.Arch, h.Kernel, h.UptimeS,
			h.Load1, h.Load5, h.CPUPct,
			h.AgentCPUPct, h.AgentMemRSS,
			h.MemTotal, h.MemUsed, h.SwapTotal, h.SwapUsed,
			h.NetRXBps, h.NetTXBps); err != nil {
			return err
		}
	}
	for _, d := range disks {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO disk_metrics (agent_id, ts, mount, fs, total, used)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (agent_id, ts, mount) DO NOTHING`,
			d.AgentID, d.Ts.Unix(), d.Mount, d.FS, d.Total, d.Used); err != nil {
			return err
		}
	}
	for _, c := range containers {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO container_metrics
  (agent_id, ts, container_id, name, image, state, restarts,
   uptime_s, cpu_pct, mem_usage, mem_limit, net_rx_bps, net_tx_bps)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (agent_id, ts, container_id) DO NOTHING`,
			c.AgentID, c.Ts.Unix(), c.ContainerID, c.Name, c.Image, c.State, c.Restarts,
			c.UptimeS, c.CPUPct, c.MemUsage, c.MemLimit, c.NetRXBps, c.NetTXBps); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (p *Postgres) QueryHost(ctx context.Context, agentID string, from, to time.Time) ([]*HostSample, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT agent_id, ts, hostname, os, arch, kernel, uptime_s,
       load_1m, load_5m, cpu_pct,
       COALESCE(agent_cpu_pct, 0), COALESCE(agent_mem_rss, 0),
       mem_total, mem_used, swap_total, swap_used,
       net_rx_bps, net_tx_bps
FROM host_metrics
WHERE agent_id = $1 AND ts >= $2 AND ts <= $3
ORDER BY ts`,
		agentID, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*HostSample, 0, 256)
	for rows.Next() {
		var h HostSample
		var ts int64
		if err := rows.Scan(&h.AgentID, &ts, &h.Hostname, &h.OS, &h.Arch, &h.Kernel, &h.UptimeS,
			&h.Load1, &h.Load5, &h.CPUPct,
			&h.AgentCPUPct, &h.AgentMemRSS,
			&h.MemTotal, &h.MemUsed, &h.SwapTotal, &h.SwapUsed,
			&h.NetRXBps, &h.NetTXBps); err != nil {
			return nil, err
		}
		h.Ts = time.Unix(ts, 0).UTC()
		out = append(out, &h)
	}
	return out, rows.Err()
}

func (p *Postgres) QueryContainers(ctx context.Context, agentID string, from, to time.Time) ([]*ContainerSample, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT agent_id, ts, container_id, name, image, state, restarts,
       uptime_s, cpu_pct, mem_usage, mem_limit, net_rx_bps, net_tx_bps
FROM container_metrics
WHERE agent_id = $1 AND ts >= $2 AND ts <= $3
ORDER BY ts, container_id`,
		agentID, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*ContainerSample, 0, 64)
	for rows.Next() {
		var c ContainerSample
		var ts int64
		if err := rows.Scan(&c.AgentID, &ts, &c.ContainerID, &c.Name, &c.Image, &c.State, &c.Restarts,
			&c.UptimeS, &c.CPUPct, &c.MemUsage, &c.MemLimit, &c.NetRXBps, &c.NetTXBps); err != nil {
			return nil, err
		}
		c.Ts = time.Unix(ts, 0).UTC()
		out = append(out, &c)
	}
	return out, rows.Err()
}

func (p *Postgres) QueryDisks(ctx context.Context, agentID string, from, to time.Time) ([]*DiskSample, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT agent_id, ts, mount, fs, total, used
FROM disk_metrics
WHERE agent_id = $1 AND ts >= $2 AND ts <= $3
ORDER BY ts, mount`,
		agentID, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*DiskSample, 0, 32)
	for rows.Next() {
		var d DiskSample
		var ts int64
		if err := rows.Scan(&d.AgentID, &ts, &d.Mount, &d.FS, &d.Total, &d.Used); err != nil {
			return nil, err
		}
		d.Ts = time.Unix(ts, 0).UTC()
		out = append(out, &d)
	}
	return out, rows.Err()
}

func (p *Postgres) Agents(ctx context.Context) ([]*Agent, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT agent_id, hostname, os, arch, kernel, version, ipv4, ipv6, last_seen, created_at
FROM agents ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*Agent, 0, 8)
	for rows.Next() {
		var a Agent
		var ipv4, ipv6 string
		var lastSeen, createdAt int64
		if err := rows.Scan(&a.AgentID, &a.Hostname, &a.OS, &a.Arch, &a.Kernel, &a.Version,
			&ipv4, &ipv6, &lastSeen, &createdAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(ipv4), &a.IPv4)
		_ = json.Unmarshal([]byte(ipv6), &a.IPv6)
		a.LastSeen = time.Unix(lastSeen, 0).UTC()
		a.CreatedAt = time.Unix(createdAt, 0).UTC()
		out = append(out, &a)
	}
	return out, rows.Err()
}

func (p *Postgres) Overview(ctx context.Context, from, to time.Time) (*Overview, error) {
	ov := &Overview{DiskMax: map[string]float64{}}

	// 1. 每节点最近一条主机样本：DISTINCT ON 取每 agent 最大 ts 行。
	hosts, err := p.db.QueryContext(ctx, `
SELECT DISTINCT ON (h.agent_id)
       h.agent_id, h.ts, h.hostname, h.os, h.arch, h.kernel, h.uptime_s,
       h.load_1m, h.load_5m, h.cpu_pct,
       COALESCE(h.agent_cpu_pct, 0), COALESCE(h.agent_mem_rss, 0),
       h.mem_total, h.mem_used, h.swap_total, h.swap_used,
       h.net_rx_bps, h.net_tx_bps
FROM host_metrics h
WHERE h.ts >= $1 AND h.ts <= $2
ORDER BY h.agent_id, h.ts DESC`, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer hosts.Close()
	for hosts.Next() {
		var h HostSample
		var ts int64
		if err := hosts.Scan(&h.AgentID, &ts, &h.Hostname, &h.OS, &h.Arch, &h.Kernel, &h.UptimeS,
			&h.Load1, &h.Load5, &h.CPUPct,
			&h.AgentCPUPct, &h.AgentMemRSS,
			&h.MemTotal, &h.MemUsed, &h.SwapTotal, &h.SwapUsed,
			&h.NetRXBps, &h.NetTXBps); err != nil {
			return nil, err
		}
		h.Ts = time.Unix(ts, 0).UTC()
		ov.Hosts = append(ov.Hosts, &h)
	}
	if err := hosts.Err(); err != nil {
		return nil, err
	}

	// 2. 每节点最高磁盘占用（窗口内各挂载点 used/total 最大百分比，按 agent 聚合）。
	diskRows, err := p.db.QueryContext(ctx, `
SELECT agent_id, MAX(CAST(used AS FLOAT) / NULLIF(total, 0) * 100) AS pct
FROM disk_metrics WHERE ts >= $1 AND ts <= $2 AND total > 0
GROUP BY agent_id`, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer diskRows.Close()
	for diskRows.Next() {
		var id string
		var pct float64
		if err := diskRows.Scan(&id, &pct); err != nil {
			return nil, err
		}
		if pct > 0 {
			ov.DiskMax[id] = pct
		}
	}
	if err := diskRows.Err(); err != nil {
		return nil, err
	}

	// 3. 当前 firing 事件数。
	if err := p.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM alert_events WHERE state = 'firing' AND started_at >= $1 AND started_at <= $2`,
		from.Unix(), to.Unix()).Scan(&ov.Firing); err != nil {
		return nil, err
	}

	return ov, nil
}

func (p *Postgres) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := p.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = $1`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (p *Postgres) SetSetting(ctx context.Context, key, value string) error {
	_, err := p.db.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, $3)
ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=EXCLUDED.updated_at`,
		key, value, time.Now().Unix())
	return err
}

func (p *Postgres) ListRules(ctx context.Context) ([]*AlertRule, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT id, name, metric, op, threshold, duration_s, severity, enabled, created_at, updated_at
FROM alert_rules ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*AlertRule, 0, 8)
	for rows.Next() {
		var r AlertRule
		var createdAt, updatedAt int64
		if err := rows.Scan(&r.ID, &r.Name, &r.Metric, &r.Op, &r.Threshold, &r.DurationS,
			&r.Severity, &r.Enabled, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		r.CreatedAt = time.Unix(createdAt, 0).UTC()
		r.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		out = append(out, &r)
	}
	return out, rows.Err()
}

func (p *Postgres) SaveRule(ctx context.Context, r *AlertRule) error {
	now := time.Now().Unix()
	if r.ID == "" {
		// 新建：分配 ID 与创建时间
		r.ID = newUUID()
		r.CreatedAt = time.Unix(now, 0).UTC()
		r.UpdatedAt = r.CreatedAt
		_, err := p.db.ExecContext(ctx, `
INSERT INTO alert_rules (id, name, metric, op, threshold, duration_s, severity, enabled, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			r.ID, r.Name, r.Metric, r.Op, r.Threshold, r.DurationS, r.Severity,
			r.Enabled, now, now)
		return err
	}
	r.UpdatedAt = time.Unix(now, 0).UTC()
	_, err := p.db.ExecContext(ctx, `
UPDATE alert_rules SET name=$1, metric=$2, op=$3, threshold=$4, duration_s=$5, severity=$6, enabled=$7, updated_at=$8
WHERE id=$9`,
		r.Name, r.Metric, r.Op, r.Threshold, r.DurationS, r.Severity,
		r.Enabled, now, r.ID)
	return err
}

func (p *Postgres) DeleteRule(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM alert_rules WHERE id = $1`, id)
	return err
}

// ---- 用户 ----

const pgUserCols = `id, username, password_hash, display_name, role, must_change_password, last_login_at, created_at, updated_at`

// scanUserPG 扫一行用户（PG 的 BOOLEAN 直接扫入 bool）。
func scanUserPG(scan func(dest ...any) error) (*User, error) {
	var u User
	var lastLogin sql.NullInt64
	var mustChange bool
	var createdAt, updatedAt int64
	if err := scan(&u.ID, &u.Username, &u.PasswordHash, &u.DisplayName, &u.Role,
		&mustChange, &lastLogin, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	u.MustChangePwd = mustChange
	u.CreatedAt = time.Unix(createdAt, 0).UTC()
	u.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	if lastLogin.Valid {
		t := time.Unix(lastLogin.Int64, 0).UTC()
		u.LastLoginAt = &t
	}
	return &u, nil
}

func (p *Postgres) getUser(ctx context.Context, where string, arg any) (*User, error) {
	u, err := scanUserPG(p.db.QueryRowContext(ctx,
		`SELECT `+pgUserCols+` FROM users WHERE `+where, arg).Scan)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	return u, err
}

func (p *Postgres) CreateUser(ctx context.Context, u *User) error {
	_, err := p.db.ExecContext(ctx, `
INSERT INTO users (id, username, password_hash, display_name, role, must_change_password, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		u.ID, u.Username, u.PasswordHash, u.DisplayName, u.Role,
		u.MustChangePwd, u.CreatedAt.Unix(), u.UpdatedAt.Unix())
	return err
}

func (p *Postgres) UpdateUser(ctx context.Context, u *User) error {
	_, err := p.db.ExecContext(ctx, `
UPDATE users SET display_name=$1, role=$2, password_hash=$3, must_change_password=$4, updated_at=$5
WHERE id=$6`,
		u.DisplayName, u.Role, u.PasswordHash, u.MustChangePwd, time.Now().Unix(), u.ID)
	return err
}

func (p *Postgres) GetUserByID(ctx context.Context, id string) (*User, error) {
	return p.getUser(ctx, `id = $1`, id)
}

func (p *Postgres) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	return p.getUser(ctx, `username = $1`, username)
}

func (p *Postgres) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT `+pgUserCols+` FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*User, 0, 4)
	for rows.Next() {
		u, err := scanUserPG(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (p *Postgres) DeleteUser(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, id)
	return err
}

func (p *Postgres) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := p.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (p *Postgres) SetLastLogin(ctx context.Context, id string, at time.Time) error {
	_, err := p.db.ExecContext(ctx, `UPDATE users SET last_login_at=$1 WHERE id=$2`, at.Unix(), id)
	return err
}

func (p *Postgres) AppendEvent(ctx context.Context, e *AlertEvent) error {
	resolved, notified := nullInt64(e.ResolvedAt), nullInt64(e.NotifiedAt)
	_, err := p.db.ExecContext(ctx, `
INSERT INTO alert_events
  (id, rule_id, rule_name, agent_id, agent_name, entity_id, entity_name,
   metric, value, threshold, severity, state, started_at, resolved_at, notified_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		e.ID, e.RuleID, e.RuleName, e.AgentID, e.AgentName, e.EntityID, e.EntityName,
		e.Metric, e.Value, e.Threshold, e.Severity, e.State, e.StartedAt.Unix(),
		resolved, notified)
	return err
}

func (p *Postgres) QueryEvents(ctx context.Context, from, to time.Time, agentID, state string, limit int) ([]*AlertEvent, error) {
	q := `SELECT id, rule_id, rule_name, agent_id, agent_name, entity_id, entity_name,
       metric, value, threshold, severity, state, started_at,
       COALESCE(resolved_at, 0), COALESCE(notified_at, 0)
FROM alert_events WHERE started_at >= $1 AND started_at <= $2`
	args := []any{from.Unix(), to.Unix()}
	argn := 3
	if agentID != "" {
		q += ` AND agent_id = $` + strconv.Itoa(argn)
		args = append(args, agentID)
		argn++
	}
	if state != "" {
		q += ` AND state = $` + strconv.Itoa(argn)
		args = append(args, state)
		argn++
	}
	q += ` ORDER BY started_at DESC LIMIT $` + strconv.Itoa(argn)
	args = append(args, limit)

	rows, err := p.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*AlertEvent, 0, 64)
	for rows.Next() {
		var e AlertEvent
		var startedAt, resolvedAt, notifiedAt int64
		if err := rows.Scan(&e.ID, &e.RuleID, &e.RuleName, &e.AgentID, &e.AgentName,
			&e.EntityID, &e.EntityName, &e.Metric, &e.Value, &e.Threshold, &e.Severity,
			&e.State, &startedAt, &resolvedAt, &notifiedAt); err != nil {
			return nil, err
		}
		e.StartedAt = time.Unix(startedAt, 0).UTC()
		if resolvedAt > 0 {
			t := time.Unix(resolvedAt, 0).UTC()
			e.ResolvedAt = &t
		}
		if notifiedAt > 0 {
			t := time.Unix(notifiedAt, 0).UTC()
			e.NotifiedAt = &t
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

func (p *Postgres) ResolveEvent(ctx context.Context, id string, at time.Time) error {
	_, err := p.db.ExecContext(ctx,
		`UPDATE alert_events SET state='resolved', resolved_at=$1 WHERE id=$2 AND state='firing'`, at.Unix(), id)
	return err
}

func (p *Postgres) SetEventNotified(ctx context.Context, id string, at time.Time) error {
	_, err := p.db.ExecContext(ctx, `UPDATE alert_events SET notified_at=$1 WHERE id=$2`, at.Unix(), id)
	return err
}

// ---- 阶段二：插件仓库 / 指派 / Series ----

func (p *Postgres) SavePlugin(ctx context.Context, pl *Plugin) error {
	if pl.Data == nil {
		pl.Data = []byte{}
	}
	if pl.CreatedAt.IsZero() {
		pl.CreatedAt = time.Now().UTC()
	}
	_, err := p.db.ExecContext(ctx, `
INSERT INTO plugins (id, name, kind, version, sha256, size, args_schema, data, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		pl.ID, pl.Name, pl.Kind, pl.Version, pl.SHA256, pl.Size, pl.ArgsSchema, pl.Data, pl.CreatedAt.Unix())
	return err
}

func (p *Postgres) ListPlugins(ctx context.Context) ([]*Plugin, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT id, name, kind, version, sha256, size, args_schema, created_at
FROM plugins ORDER BY id, version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*Plugin, 0, 8)
	for rows.Next() {
		var pl Plugin
		var createdAt int64
		if err := rows.Scan(&pl.ID, &pl.Name, &pl.Kind, &pl.Version, &pl.SHA256, &pl.Size, &pl.ArgsSchema, &createdAt); err != nil {
			return nil, err
		}
		pl.CreatedAt = time.Unix(createdAt, 0).UTC()
		out = append(out, &pl)
	}
	return out, rows.Err()
}

func (p *Postgres) GetPlugin(ctx context.Context, id, version string) (*Plugin, error) {
	var pl Plugin
	var createdAt int64
	err := p.db.QueryRowContext(ctx, `
SELECT id, name, kind, version, sha256, size, args_schema, data, created_at
FROM plugins WHERE id = $1 AND version = $2`, id, version).
		Scan(&pl.ID, &pl.Name, &pl.Kind, &pl.Version, &pl.SHA256, &pl.Size, &pl.ArgsSchema, &pl.Data, &createdAt)
	if err != nil {
		return nil, err
	}
	pl.CreatedAt = time.Unix(createdAt, 0).UTC()
	return &pl, nil
}

func (p *Postgres) AssignPlugin(ctx context.Context, ap *AgentPlugin) error {
	_, err := p.db.ExecContext(ctx, `
INSERT INTO agent_plugins (agent_id, plugin_id, version, args_json)
VALUES ($1, $2, $3, $4)
ON CONFLICT (agent_id, plugin_id) DO UPDATE SET
  version=EXCLUDED.version, args_json=EXCLUDED.args_json`,
		ap.AgentID, ap.PluginID, ap.Version, ap.ArgsJSON)
	return err
}

func (p *Postgres) DeletePluginVersion(ctx context.Context, id, version string) error {
	var n int
	if err := p.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_plugins WHERE plugin_id = $1 AND version = $2`, id, version).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("该版本仍被 %d 个节点指派，请先取消指派", n)
	}
	_, err := p.db.ExecContext(ctx, `DELETE FROM plugins WHERE id = $1 AND version = $2`, id, version)
	return err
}

func (p *Postgres) UnassignPlugin(ctx context.Context, agentID, pluginID string) error {
	_, err := p.db.ExecContext(ctx,
		`DELETE FROM agent_plugins WHERE agent_id = $1 AND plugin_id = $2`, agentID, pluginID)
	return err
}

func (p *Postgres) AgentPlugins(ctx context.Context, agentID string) ([]*AgentPlugin, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT agent_id, plugin_id, version, args_json
FROM agent_plugins WHERE agent_id = $1 ORDER BY plugin_id`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*AgentPlugin, 0, 8)
	for rows.Next() {
		var ap AgentPlugin
		if err := rows.Scan(&ap.AgentID, &ap.PluginID, &ap.Version, &ap.ArgsJSON); err != nil {
			return nil, err
		}
		out = append(out, &ap)
	}
	return out, rows.Err()
}

func (p *Postgres) AppendSeries(ctx context.Context, agentID string, items []*Series) error {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, it := range items {
		tags, _ := json.Marshal(it.Tags)
		fields, _ := json.Marshal(it.Fields)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO series (agent_id, ts, name, tags, fields) VALUES ($1, $2, $3, $4, $5)`,
			agentID, it.TS.Unix(), it.Name, string(tags), string(fields)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- 阶段二：定时任务 ----

const pgTaskCols = `id, name, description, target_agent_id, cron, plugin_id, args_json,
  timeout_s, enabled, run_now, last_run_at, last_status, last_output_tail, created_at, updated_at`

// scanTaskPG 扫一行任务（PG 的 BOOLEAN 直接扫入 bool）。
func scanTaskPG(scan func(dest ...any) error) (*Task, error) {
	var t Task
	var createdAt, updatedAt int64
	var lastRunAt sql.NullInt64
	if err := scan(&t.ID, &t.Name, &t.Description, &t.TargetAgentID, &t.Cron, &t.PluginID, &t.ArgsJSON,
		&t.TimeoutS, &t.Enabled, &t.RunNow, &lastRunAt, &t.LastStatus, &t.LastOutputTail, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	t.CreatedAt = time.Unix(createdAt, 0).UTC()
	t.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	if lastRunAt.Valid {
		v := time.Unix(lastRunAt.Int64, 0).UTC()
		t.LastRunAt = &v
	}
	return &t, nil
}

func (p *Postgres) SaveTask(ctx context.Context, t *Task) error {
	now := time.Now().Unix()
	if t.ID == "" {
		t.ID = newUUID()
		t.CreatedAt = time.Unix(now, 0).UTC()
		t.UpdatedAt = t.CreatedAt
		_, err := p.db.ExecContext(ctx, `
INSERT INTO tasks (id, name, description, target_agent_id, cron, plugin_id, args_json, timeout_s, enabled, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			t.ID, t.Name, t.Description, t.TargetAgentID, t.Cron, t.PluginID, t.ArgsJSON,
			t.TimeoutS, t.Enabled, now, now)
		return err
	}
	t.UpdatedAt = time.Unix(now, 0).UTC()
	_, err := p.db.ExecContext(ctx, `
UPDATE tasks SET name=$1, description=$2, target_agent_id=$3, cron=$4, plugin_id=$5, args_json=$6, timeout_s=$7, enabled=$8, updated_at=$9
WHERE id=$10`,
		t.Name, t.Description, t.TargetAgentID, t.Cron, t.PluginID, t.ArgsJSON,
		t.TimeoutS, t.Enabled, now, t.ID)
	return err
}

func (p *Postgres) ListTasks(ctx context.Context) ([]*Task, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT `+pgTaskCols+` FROM tasks ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*Task, 0, 8)
	for rows.Next() {
		t, err := scanTaskPG(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (p *Postgres) GetTask(ctx context.Context, id string) (*Task, error) {
	t, err := scanTaskPG(p.db.QueryRowContext(ctx, `SELECT `+pgTaskCols+` FROM tasks WHERE id = $1`, id).Scan)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	return t, err
}

func (p *Postgres) DeleteTask(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = $1`, id)
	return err
}

func (p *Postgres) AppendTaskRun(ctx context.Context, r *TaskRun) error {
	if r.ID == "" {
		r.ID = newUUID()
	}
	_, err := p.db.ExecContext(ctx, `
INSERT INTO task_runs (id, task_id, agent_id, status, exit_code, output, started_at, finished_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		r.ID, r.TaskID, r.AgentID, r.Status, r.ExitCode, r.Output, r.StartedAt.Unix(), nullInt64(r.FinishedAt))
	return err
}

func (p *Postgres) QueryTaskRuns(ctx context.Context, taskID string, limit int) ([]*TaskRun, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT id, task_id, agent_id, status, exit_code, output, started_at,
       COALESCE(finished_at, 0)
FROM task_runs WHERE task_id = $1 ORDER BY started_at DESC LIMIT $2`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*TaskRun, 0, 32)
	for rows.Next() {
		var r TaskRun
		var startedAt, finishedAt int64
		var exitCode sql.NullInt64
		if err := rows.Scan(&r.ID, &r.TaskID, &r.AgentID, &r.Status, &exitCode, &r.Output, &startedAt, &finishedAt); err != nil {
			return nil, err
		}
		if exitCode.Valid {
			v := int32(exitCode.Int64)
			r.ExitCode = &v
		}
		r.StartedAt = time.Unix(startedAt, 0).UTC()
		if finishedAt > 0 {
			v := time.Unix(finishedAt, 0).UTC()
			r.FinishedAt = &v
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

func (p *Postgres) UpdateTaskLastRun(ctx context.Context, taskID, status, outputTail string, at time.Time) error {
	_, err := p.db.ExecContext(ctx, `
UPDATE tasks SET last_run_at=$1, last_status=$2, last_output_tail=$3 WHERE id=$4`,
		at.Unix(), status, outputTail, taskID)
	return err
}

// SetTaskRunNow 置位/清除任务「立即运行」标记（POST /run 置位，收到执行报告后清除）。
func (p *Postgres) SetTaskRunNow(ctx context.Context, taskID string, runNow bool) error {
	_, err := p.db.ExecContext(ctx, `UPDATE tasks SET run_now=$1, updated_at=$2 WHERE id=$3`,
		runNow, time.Now().Unix(), taskID)
	return err
}

// ---- 阶段二：frp 配置 / 状态 ----

func (p *Postgres) SaveFrpConfig(ctx context.Context, c *FrpConfig) error {
	proxies, _ := json.Marshal(c.Proxies)
	if c.StateVersion == 0 {
		c.StateVersion = 1
	}
	_, err := p.db.ExecContext(ctx, `
INSERT INTO frp_configs (kind, agent_id, server_addr, server_port, token, proxies, state_version, enabled, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (kind, agent_id) DO UPDATE SET
  server_addr=EXCLUDED.server_addr, server_port=EXCLUDED.server_port, token=EXCLUDED.token,
  proxies=EXCLUDED.proxies, state_version=EXCLUDED.state_version, enabled=EXCLUDED.enabled,
  updated_at=EXCLUDED.updated_at`,
		c.Kind, c.AgentID, c.ServerAddr, c.ServerPort, c.Token, string(proxies),
		c.StateVersion, c.Enabled, time.Now().Unix())
	return err
}

func (p *Postgres) GetFrpConfig(ctx context.Context, kind, agentID string) (*FrpConfig, error) {
	var c FrpConfig
	var proxies string
	var stateVersion, updatedAt int64
	err := p.db.QueryRowContext(ctx, `
SELECT kind, agent_id, server_addr, server_port, token, proxies, state_version, enabled, updated_at
FROM frp_configs WHERE kind = $1 AND agent_id = $2`, kind, agentID).
		Scan(&c.Kind, &c.AgentID, &c.ServerAddr, &c.ServerPort, &c.Token, &proxies, &stateVersion, &c.Enabled, &updatedAt)
	if err != nil {
		return nil, err
	}
	c.Proxies = parseTunnels(proxies)
	c.StateVersion = uint64(stateVersion)
	c.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return &c, nil
}

func (p *Postgres) ListFrpConfigs(ctx context.Context) ([]*FrpConfig, error) {
	rows, err := p.db.QueryContext(ctx, `
SELECT kind, agent_id, server_addr, server_port, token, proxies, state_version, enabled, updated_at
FROM frp_configs ORDER BY kind, agent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*FrpConfig, 0, 4)
	for rows.Next() {
		var c FrpConfig
		var proxies string
		var stateVersion, updatedAt int64
		if err := rows.Scan(&c.Kind, &c.AgentID, &c.ServerAddr, &c.ServerPort, &c.Token, &proxies,
			&stateVersion, &c.Enabled, &updatedAt); err != nil {
			return nil, err
		}
		c.Proxies = parseTunnels(proxies)
		c.StateVersion = uint64(stateVersion)
		c.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		out = append(out, &c)
	}
	return out, rows.Err()
}

func (p *Postgres) UpsertFrpStatus(ctx context.Context, agentID string, st *FrpStatus) error {
	tunnels, _ := json.Marshal(st.Tunnels)
	_, err := p.db.ExecContext(ctx, `
INSERT INTO frp_status (agent_id, running, frp_version, error, tunnels, ts)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (agent_id) DO UPDATE SET
  running=EXCLUDED.running, frp_version=EXCLUDED.frp_version, error=EXCLUDED.error,
  tunnels=EXCLUDED.tunnels, ts=EXCLUDED.ts`,
		agentID, st.Running, st.FrpVersion, st.Error, string(tunnels), st.TS.Unix())
	return err
}

func (p *Postgres) DeleteFrpConfig(ctx context.Context, kind, agentID string) error {
	_, err := p.db.ExecContext(ctx,
		`DELETE FROM frp_configs WHERE kind = $1 AND agent_id = $2`, kind, agentID)
	return err
}

func (p *Postgres) QueryFrpStatus(ctx context.Context, agentID string) (*FrpStatus, error) {
	var st FrpStatus
	var ts int64
	var tunnels string
	err := p.db.QueryRowContext(ctx, `
SELECT running, frp_version, error, tunnels, ts
FROM frp_status WHERE agent_id = $1`, agentID).
		Scan(&st.Running, &st.FrpVersion, &st.Error, &tunnels, &ts)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(tunnels), &st.Tunnels)
	st.TS = time.Unix(ts, 0).UTC()
	return &st, nil
}

func (p *Postgres) Cleanup(ctx context.Context, retention time.Duration) (int64, error) {
	cutoff := time.Now().Add(-retention).Unix()
	var total int64
	for _, q := range []string{
		`DELETE FROM host_metrics WHERE ts < $1`,
		`DELETE FROM disk_metrics WHERE ts < $1`,
		`DELETE FROM container_metrics WHERE ts < $1`,
		`DELETE FROM series WHERE ts < $1`,
		`DELETE FROM alert_events WHERE started_at < $1`,
		`DELETE FROM task_runs WHERE started_at < $1`,
	} {
		res, err := p.db.ExecContext(ctx, q, cutoff)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

func (p *Postgres) Close() error {
	return p.db.Close()
}

var _ Store = (*Postgres)(nil)
