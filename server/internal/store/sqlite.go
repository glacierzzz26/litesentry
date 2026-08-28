package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（无 cgo，交叉编译友好）
)

// SQLite 默认存储实现。
// 时序列统一用 INTEGER 存 unix 秒，规避 SQLite / PG 的 TIMESTAMP 方言差异。
type SQLite struct {
	db *sql.DB
}

func NewSQLite(dsn string) (*SQLite, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // modernc sqlite 单写者；1 连接避免写锁冲突
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, err
	}
	s := &SQLite{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS agents (
  agent_id   TEXT PRIMARY KEY,
  hostname   TEXT NOT NULL,
  os         TEXT NOT NULL DEFAULT '',
  arch       TEXT NOT NULL DEFAULT '',
  kernel     TEXT NOT NULL DEFAULT '',
  version    TEXT NOT NULL DEFAULT '',  -- Agent 构建版本（注册上报）
  machine_id TEXT,               -- 机器指纹（注册复用；可为空表示旧节点未注册）
  ipv4       TEXT NOT NULL DEFAULT '[]',
  ipv6       TEXT NOT NULL DEFAULT '[]',
  last_seen  INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
-- 机器指纹唯一索引由 migrate() 在列迁移完成后创建（旧库 agents 表可能缺 machine_id 列）

CREATE TABLE IF NOT EXISTS host_metrics (
  agent_id TEXT NOT NULL,
  ts       INTEGER NOT NULL,
  hostname TEXT NOT NULL,
  os TEXT, arch TEXT, kernel TEXT,
  uptime_s INTEGER,
  load_1m REAL, load_5m REAL, cpu_pct REAL,
  agent_cpu_pct REAL, agent_mem_rss INTEGER,
  mem_total INTEGER, mem_used INTEGER, swap_total INTEGER, swap_used INTEGER,
  net_rx_bps INTEGER, net_tx_bps INTEGER
);
CREATE INDEX IF NOT EXISTS idx_host_agent_ts ON host_metrics (agent_id, ts);

CREATE TABLE IF NOT EXISTS disk_metrics (
  agent_id TEXT NOT NULL,
  ts       INTEGER NOT NULL,
  mount    TEXT NOT NULL,
  fs       TEXT,
  total INTEGER, used INTEGER,
  PRIMARY KEY (agent_id, ts, mount)
);

CREATE TABLE IF NOT EXISTS container_metrics (
  agent_id TEXT NOT NULL,
  ts       INTEGER NOT NULL,
  container_id TEXT NOT NULL,
  name TEXT, image TEXT, state TEXT,
  restarts INTEGER,
  uptime_s INTEGER, cpu_pct REAL,
  mem_usage INTEGER, mem_limit INTEGER,
  net_rx_bps INTEGER, net_tx_bps INTEGER,
  PRIMARY KEY (agent_id, ts, container_id)
);

CREATE TABLE IF NOT EXISTS settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL DEFAULT '',
  updated_at INTEGER
);

CREATE TABLE IF NOT EXISTS users (
  id                   TEXT PRIMARY KEY,
  username             TEXT NOT NULL UNIQUE,
  password_hash        TEXT NOT NULL,      -- bcrypt，绝不回传
  display_name         TEXT NOT NULL DEFAULT '',
  role                 TEXT NOT NULL DEFAULT 'admin',
  must_change_password INTEGER NOT NULL DEFAULT 0,
  last_login_at        INTEGER,            -- 上次登录 unix 秒，NULL = 从未登录
  created_at           INTEGER NOT NULL,
  updated_at           INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS alert_rules (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  metric     TEXT NOT NULL,
  op         TEXT NOT NULL DEFAULT '>',
  threshold  REAL NOT NULL DEFAULT 0,
  duration_s INTEGER NOT NULL DEFAULT 0,
  severity   TEXT NOT NULL DEFAULT 'warning',
  enabled    INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
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
  started_at  INTEGER NOT NULL,
  resolved_at INTEGER,
  notified_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_events_agent_ts ON alert_events (agent_id, started_at);
CREATE INDEX IF NOT EXISTS idx_events_state  ON alert_events (state, started_at);

-- ============ 阶段二：插件 / 任务 / frp ============

CREATE TABLE IF NOT EXISTS plugins (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'binary',
  version TEXT NOT NULL, sha256 TEXT NOT NULL, size INTEGER NOT NULL DEFAULT 0,
  args_schema TEXT DEFAULT '', data BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  UNIQUE(id, version)                  -- 不可变版本，只允许新增
);
CREATE TABLE IF NOT EXISTS agent_plugins (
  agent_id TEXT NOT NULL, plugin_id TEXT NOT NULL, version TEXT NOT NULL,
  args_json TEXT DEFAULT '',
  PRIMARY KEY (agent_id, plugin_id)
);
-- 插件通用时序（阶段二）：host.* / container.* / 自定义插件统一落此表，
-- S2 再按 name 前缀翻译回 host_metrics / container_metrics / disk_metrics。
CREATE TABLE IF NOT EXISTS series (
  agent_id TEXT NOT NULL, ts INTEGER NOT NULL, name TEXT NOT NULL,
  tags TEXT NOT NULL DEFAULT '{}', fields TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_series_agent_name_ts ON series (agent_id, name, ts);
CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT DEFAULT '',
  target_agent_id TEXT DEFAULT '', cron TEXT NOT NULL, plugin_id TEXT NOT NULL,
  args_json TEXT DEFAULT '', timeout_s INTEGER NOT NULL DEFAULT 60,
  enabled INTEGER NOT NULL DEFAULT 1, last_run_at INTEGER, last_status TEXT DEFAULT '',
  last_output_tail TEXT DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS task_runs (
  id TEXT PRIMARY KEY, task_id TEXT NOT NULL, agent_id TEXT NOT NULL,
  status TEXT NOT NULL, exit_code INTEGER, output TEXT DEFAULT '',
  started_at INTEGER NOT NULL, finished_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_task_runs_task ON task_runs (task_id, started_at);
CREATE TABLE IF NOT EXISTS frp_configs (
  kind TEXT NOT NULL, agent_id TEXT NOT NULL DEFAULT '',
  server_addr TEXT DEFAULT '', server_port INTEGER DEFAULT 7000, token TEXT DEFAULT '',
  proxies TEXT NOT NULL DEFAULT '[]', state_version INTEGER NOT NULL DEFAULT 1,
  enabled INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL,
  PRIMARY KEY (kind, agent_id)         -- frps: agent_id=''；frpc: agent_id=节点 id
);
CREATE TABLE IF NOT EXISTS frp_status (
  agent_id TEXT PRIMARY KEY, running INTEGER NOT NULL DEFAULT 0,
  frp_version TEXT DEFAULT '', error TEXT DEFAULT '',
  tunnels TEXT NOT NULL DEFAULT '[]', ts INTEGER NOT NULL
);
`

func (s *SQLite) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}
	// 对已存在（旧 schema）的库做轻量列迁移：SQLite 不支持 ADD COLUMN IF NOT EXISTS
	for _, mc := range []struct{ table, col, ddl string }{
		{"host_metrics", "agent_cpu_pct", `ALTER TABLE host_metrics ADD COLUMN agent_cpu_pct REAL`},
		{"host_metrics", "agent_mem_rss", `ALTER TABLE host_metrics ADD COLUMN agent_mem_rss INTEGER`},
		{"agents", "machine_id", `ALTER TABLE agents ADD COLUMN machine_id TEXT`},
		{"agents", "version", `ALTER TABLE agents ADD COLUMN version TEXT`},
	} {
		has, err := s.hasColumn(mc.table, mc.col)
		if err != nil {
			return err
		}
		if !has {
			if _, err := s.db.Exec(mc.ddl); err != nil {
				return err
			}
		}
	}
	// 迁移完回填：老库 ALTER 加列的 version 为 NULL，归一化为空串
	if _, err := s.db.Exec(`UPDATE agents SET version = COALESCE(version, '') WHERE version IS NULL`); err != nil {
		return err
	}
	// 迁移完确保机器指纹唯一索引存在（partial：仅索引非 NULL 行）
	_, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_machine ON agents (machine_id) WHERE machine_id IS NOT NULL`)
	return err
}

func (s *SQLite) hasColumn(table, col string) (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *SQLite) UpsertAgent(ctx context.Context, a *Agent) error {
	ipv4, _ := json.Marshal(a.IPv4)
	ipv6, _ := json.Marshal(a.IPv6)
	now := a.LastSeen.Unix()
	created := a.CreatedAt.Unix()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO agents (agent_id, hostname, os, arch, kernel, ipv4, ipv6, last_seen, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(agent_id) DO UPDATE SET
  hostname=excluded.hostname, os=excluded.os, arch=excluded.arch, kernel=excluded.kernel,
  ipv4=excluded.ipv4, ipv6=excluded.ipv6, last_seen=excluded.last_seen
`,
		a.AgentID, a.Hostname, a.OS, a.Arch, a.Kernel,
		string(ipv4), string(ipv6), now, created)
	return err
}

func (s *SQLite) RegisterAgent(ctx context.Context, machineID string, a *Agent) (string, bool, error) {
	var existing string
	err := s.db.QueryRowContext(ctx,
		`SELECT agent_id FROM agents WHERE machine_id = ?`, machineID).Scan(&existing)
	switch {
	case err == nil:
		// 复用历史 agent_id：同步更新元信息与在线时间
		_, uerr := s.db.ExecContext(ctx, `
UPDATE agents SET hostname=?, os=?, arch=?, kernel=?, version=?, last_seen=?
WHERE agent_id=?`,
			a.Hostname, a.OS, a.Arch, a.Kernel, a.Version, a.LastSeen.Unix(), existing)
		return existing, false, uerr
	case err != sql.ErrNoRows:
		return "", false, err
	}
	id := newUUID()
	_, err = s.db.ExecContext(ctx, `
INSERT INTO agents (agent_id, hostname, os, arch, kernel, version, machine_id, last_seen, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, a.Hostname, a.OS, a.Arch, a.Kernel, a.Version, machineID, a.LastSeen.Unix(), a.CreatedAt.Unix())
	return id, true, err
}

func (s *SQLite) AppendBatch(ctx context.Context, h *HostSample, disks []*DiskSample, containers []*ContainerSample) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // commit 后再 rollback 是 no-op

	if h != nil {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO host_metrics
  (agent_id, ts, hostname, os, arch, kernel, uptime_s,
   load_1m, load_5m, cpu_pct,
   agent_cpu_pct, agent_mem_rss,
   mem_total, mem_used, swap_total, swap_used,
   net_rx_bps, net_tx_bps)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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
INSERT OR REPLACE INTO disk_metrics (agent_id, ts, mount, fs, total, used)
VALUES (?, ?, ?, ?, ?, ?)`,
			d.AgentID, d.Ts.Unix(), d.Mount, d.FS, d.Total, d.Used); err != nil {
			return err
		}
	}
	for _, c := range containers {
		if _, err := tx.ExecContext(ctx, `
INSERT OR REPLACE INTO container_metrics
  (agent_id, ts, container_id, name, image, state, restarts,
   uptime_s, cpu_pct, mem_usage, mem_limit, net_rx_bps, net_tx_bps)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.AgentID, c.Ts.Unix(), c.ContainerID, c.Name, c.Image, c.State, c.Restarts,
			c.UptimeS, c.CPUPct, c.MemUsage, c.MemLimit, c.NetRXBps, c.NetTXBps); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) QueryHost(ctx context.Context, agentID string, from, to time.Time) ([]*HostSample, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT agent_id, ts, hostname, os, arch, kernel, uptime_s,
       load_1m, load_5m, cpu_pct,
       COALESCE(agent_cpu_pct, 0), COALESCE(agent_mem_rss, 0),
       mem_total, mem_used, swap_total, swap_used,
       net_rx_bps, net_tx_bps
FROM host_metrics
WHERE agent_id = ? AND ts >= ? AND ts <= ?
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

func (s *SQLite) QueryContainers(ctx context.Context, agentID string, from, to time.Time) ([]*ContainerSample, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT agent_id, ts, container_id, name, image, state, restarts,
       uptime_s, cpu_pct, mem_usage, mem_limit, net_rx_bps, net_tx_bps
FROM container_metrics
WHERE agent_id = ? AND ts >= ? AND ts <= ?
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

func (s *SQLite) QueryDisks(ctx context.Context, agentID string, from, to time.Time) ([]*DiskSample, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT agent_id, ts, mount, fs, total, used
FROM disk_metrics
WHERE agent_id = ? AND ts >= ? AND ts <= ?
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

func (s *SQLite) Agents(ctx context.Context) ([]*Agent, error) {
	rows, err := s.db.QueryContext(ctx, `
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

func (s *SQLite) Overview(ctx context.Context, from, to time.Time) (*Overview, error) {
	ov := &Overview{DiskMax: map[string]float64{}}

	// 1. 每节点最近一条主机样本：JOIN 取每 agent 的最大 ts 行。
	hosts, err := s.db.QueryContext(ctx, `
SELECT h.agent_id, h.ts, h.hostname, h.os, h.arch, h.kernel, h.uptime_s,
       h.load_1m, h.load_5m, h.cpu_pct,
       COALESCE(h.agent_cpu_pct, 0), COALESCE(h.agent_mem_rss, 0),
       h.mem_total, h.mem_used, h.swap_total, h.swap_used,
       h.net_rx_bps, h.net_tx_bps
FROM host_metrics h
JOIN (SELECT agent_id, MAX(ts) mts FROM host_metrics WHERE ts >= ? AND ts <= ? GROUP BY agent_id) x
  ON h.agent_id = x.agent_id AND h.ts = x.mts
ORDER BY h.hostname`, from.Unix(), to.Unix())
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

	// 2. 每节点最高磁盘占用（取该窗口内各挂载点 used/total 的最大百分比，按 agent 聚合）。
	diskRows, err := s.db.QueryContext(ctx, `
SELECT agent_id, MAX(CAST(used AS REAL) / NULLIF(total, 0) * 100) AS pct
FROM disk_metrics WHERE ts >= ? AND ts <= ? AND total > 0
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
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM alert_events WHERE state = 'firing' AND started_at >= ? AND started_at <= ?`,
		from.Unix(), to.Unix()).Scan(&ov.Firing); err != nil {
		return nil, err
	}

	return ov, nil
}

func (s *SQLite) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *SQLite) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		key, value, time.Now().Unix())
	return err
}

func (s *SQLite) ListRules(ctx context.Context) ([]*AlertRule, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, metric, op, threshold, duration_s, severity, enabled, created_at, updated_at
FROM alert_rules ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*AlertRule, 0, 8)
	for rows.Next() {
		var r AlertRule
		var enabled, createdAt, updatedAt int64
		if err := rows.Scan(&r.ID, &r.Name, &r.Metric, &r.Op, &r.Threshold, &r.DurationS,
			&r.Severity, &enabled, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		r.CreatedAt = time.Unix(createdAt, 0).UTC()
		r.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		out = append(out, &r)
	}
	return out, rows.Err()
}

func (s *SQLite) SaveRule(ctx context.Context, r *AlertRule) error {
	now := time.Now().Unix()
	if r.ID == "" {
		// 新建：分配 ID 与创建时间
		r.ID = newUUID()
		r.CreatedAt = time.Unix(now, 0).UTC()
		r.UpdatedAt = r.CreatedAt
		_, err := s.db.ExecContext(ctx, `
INSERT INTO alert_rules (id, name, metric, op, threshold, duration_s, severity, enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.Name, r.Metric, r.Op, r.Threshold, r.DurationS, r.Severity,
			enabledInt(r.Enabled), now, now)
		return err
	}
	r.UpdatedAt = time.Unix(now, 0).UTC()
	_, err := s.db.ExecContext(ctx, `
UPDATE alert_rules SET name=?, metric=?, op=?, threshold=?, duration_s=?, severity=?, enabled=?, updated_at=?
WHERE id=?`,
		r.Name, r.Metric, r.Op, r.Threshold, r.DurationS, r.Severity,
		enabledInt(r.Enabled), now, r.ID)
	return err
}

func (s *SQLite) DeleteRule(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM alert_rules WHERE id = ?`, id)
	return err
}

// ---- 用户 ----

const userCols = `id, username, password_hash, display_name, role, must_change_password, last_login_at, created_at, updated_at`

// scanUser 扫一行用户（*sql.Row.Scan / *sql.Rows.Scan 均满足该签名）。
func scanUser(scan func(dest ...any) error) (*User, error) {
	var u User
	var lastLogin sql.NullInt64
	var mustChange, createdAt, updatedAt int64
	if err := scan(&u.ID, &u.Username, &u.PasswordHash, &u.DisplayName, &u.Role,
		&mustChange, &lastLogin, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	u.MustChangePwd = mustChange != 0
	u.CreatedAt = time.Unix(createdAt, 0).UTC()
	u.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	if lastLogin.Valid {
		t := time.Unix(lastLogin.Int64, 0).UTC()
		u.LastLoginAt = &t
	}
	return &u, nil
}

func (s *SQLite) getUser(ctx context.Context, where string, arg any) (*User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM users WHERE `+where, arg).Scan)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	return u, err
}

func (s *SQLite) CreateUser(ctx context.Context, u *User) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO users (id, username, password_hash, display_name, role, must_change_password, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, u.DisplayName, u.Role,
		enabledInt(u.MustChangePwd), u.CreatedAt.Unix(), u.UpdatedAt.Unix())
	return err
}

func (s *SQLite) UpdateUser(ctx context.Context, u *User) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE users SET display_name=?, role=?, password_hash=?, must_change_password=?, updated_at=?
WHERE id=?`,
		u.DisplayName, u.Role, u.PasswordHash, enabledInt(u.MustChangePwd), time.Now().Unix(), u.ID)
	return err
}

func (s *SQLite) GetUserByID(ctx context.Context, id string) (*User, error) {
	return s.getUser(ctx, `id = ?`, id)
}

func (s *SQLite) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	return s.getUser(ctx, `username = ?`, username)
}

func (s *SQLite) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*User, 0, 4)
	for rows.Next() {
		u, err := scanUser(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *SQLite) DeleteUser(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return err
}

func (s *SQLite) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *SQLite) SetLastLogin(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_login_at=? WHERE id=?`, at.Unix(), id)
	return err
}

func (s *SQLite) AppendEvent(ctx context.Context, e *AlertEvent) error {
	resolved, notified := nullInt64(e.ResolvedAt), nullInt64(e.NotifiedAt)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO alert_events
  (id, rule_id, rule_name, agent_id, agent_name, entity_id, entity_name,
   metric, value, threshold, severity, state, started_at, resolved_at, notified_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.RuleID, e.RuleName, e.AgentID, e.AgentName, e.EntityID, e.EntityName,
		e.Metric, e.Value, e.Threshold, e.Severity, e.State, e.StartedAt.Unix(),
		resolved, notified)
	return err
}

func (s *SQLite) QueryEvents(ctx context.Context, from, to time.Time, agentID, state string, limit int) ([]*AlertEvent, error) {
	q := `SELECT id, rule_id, rule_name, agent_id, agent_name, entity_id, entity_name,
       metric, value, threshold, severity, state, started_at,
       COALESCE(resolved_at, 0), COALESCE(notified_at, 0)
FROM alert_events WHERE started_at >= ? AND started_at <= ?`
	args := []any{from.Unix(), to.Unix()}
	if agentID != "" {
		q += ` AND agent_id = ?`
		args = append(args, agentID)
	}
	if state != "" {
		q += ` AND state = ?`
		args = append(args, state)
	}
	q += ` ORDER BY started_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
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

func (s *SQLite) ResolveEvent(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE alert_events SET state='resolved', resolved_at=? WHERE id=? AND state='firing'`, at.Unix(), id)
	return err
}

func (s *SQLite) SetEventNotified(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alert_events SET notified_at=? WHERE id=?`, at.Unix(), id)
	return err
}

// ---- 阶段二：插件仓库 / 指派 / Series ----

func (s *SQLite) SavePlugin(ctx context.Context, p *Plugin) error {
	if p.Data == nil {
		p.Data = []byte{}
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO plugins (id, name, kind, version, sha256, size, args_schema, data, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Kind, p.Version, p.SHA256, p.Size, p.ArgsSchema, p.Data, p.CreatedAt.Unix())
	return err
}

func (s *SQLite) ListPlugins(ctx context.Context) ([]*Plugin, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, kind, version, sha256, size, args_schema, created_at
FROM plugins ORDER BY id, version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*Plugin, 0, 8)
	for rows.Next() {
		var p Plugin
		var createdAt int64
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.Version, &p.SHA256, &p.Size, &p.ArgsSchema, &createdAt); err != nil {
			return nil, err
		}
		p.CreatedAt = time.Unix(createdAt, 0).UTC()
		out = append(out, &p)
	}
	return out, rows.Err()
}

func (s *SQLite) GetPlugin(ctx context.Context, id, version string) (*Plugin, error) {
	var p Plugin
	var createdAt int64
	err := s.db.QueryRowContext(ctx, `
SELECT id, name, kind, version, sha256, size, args_schema, data, created_at
FROM plugins WHERE id = ? AND version = ?`, id, version).
		Scan(&p.ID, &p.Name, &p.Kind, &p.Version, &p.SHA256, &p.Size, &p.ArgsSchema, &p.Data, &createdAt)
	if err != nil {
		return nil, err
	}
	p.CreatedAt = time.Unix(createdAt, 0).UTC()
	return &p, nil
}

func (s *SQLite) AssignPlugin(ctx context.Context, ap *AgentPlugin) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO agent_plugins (agent_id, plugin_id, version, args_json)
VALUES (?, ?, ?, ?)
ON CONFLICT(agent_id, plugin_id) DO UPDATE SET
  version=excluded.version, args_json=excluded.args_json`,
		ap.AgentID, ap.PluginID, ap.Version, ap.ArgsJSON)
	return err
}

func (s *SQLite) AgentPlugins(ctx context.Context, agentID string) ([]*AgentPlugin, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT agent_id, plugin_id, version, args_json
FROM agent_plugins WHERE agent_id = ? ORDER BY plugin_id`, agentID)
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

func (s *SQLite) AppendSeries(ctx context.Context, agentID string, items []*Series) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, it := range items {
		tags, _ := json.Marshal(it.Tags)
		fields, _ := json.Marshal(it.Fields)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO series (agent_id, ts, name, tags, fields) VALUES (?, ?, ?, ?, ?)`,
			agentID, it.TS.Unix(), it.Name, string(tags), string(fields)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- 阶段二：定时任务 ----

const taskCols = `id, name, description, target_agent_id, cron, plugin_id, args_json,
  timeout_s, enabled, last_run_at, last_status, last_output_tail, created_at, updated_at`

// scanTask 扫一行任务（*sql.Row.Scan / *sql.Rows.Scan 均满足该签名）。
func scanTask(scan func(dest ...any) error) (*Task, error) {
	var t Task
	var enabled, createdAt, updatedAt int64
	var lastRunAt sql.NullInt64
	if err := scan(&t.ID, &t.Name, &t.Description, &t.TargetAgentID, &t.Cron, &t.PluginID, &t.ArgsJSON,
		&t.TimeoutS, &enabled, &lastRunAt, &t.LastStatus, &t.LastOutputTail, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	t.Enabled = enabled != 0
	t.CreatedAt = time.Unix(createdAt, 0).UTC()
	t.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	if lastRunAt.Valid {
		v := time.Unix(lastRunAt.Int64, 0).UTC()
		t.LastRunAt = &v
	}
	return &t, nil
}

func (s *SQLite) SaveTask(ctx context.Context, t *Task) error {
	now := time.Now().Unix()
	if t.ID == "" {
		t.ID = newUUID()
		t.CreatedAt = time.Unix(now, 0).UTC()
		t.UpdatedAt = t.CreatedAt
		_, err := s.db.ExecContext(ctx, `
INSERT INTO tasks (id, name, description, target_agent_id, cron, plugin_id, args_json, timeout_s, enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.Name, t.Description, t.TargetAgentID, t.Cron, t.PluginID, t.ArgsJSON,
			t.TimeoutS, enabledInt(t.Enabled), now, now)
		return err
	}
	t.UpdatedAt = time.Unix(now, 0).UTC()
	_, err := s.db.ExecContext(ctx, `
UPDATE tasks SET name=?, description=?, target_agent_id=?, cron=?, plugin_id=?, args_json=?, timeout_s=?, enabled=?, updated_at=?
WHERE id=?`,
		t.Name, t.Description, t.TargetAgentID, t.Cron, t.PluginID, t.ArgsJSON,
		t.TimeoutS, enabledInt(t.Enabled), now, t.ID)
	return err
}

func (s *SQLite) ListTasks(ctx context.Context) ([]*Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskCols+` FROM tasks ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*Task, 0, 8)
	for rows.Next() {
		t, err := scanTask(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *SQLite) GetTask(ctx context.Context, id string) (*Task, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = ?`, id).Scan)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	return t, err
}

func (s *SQLite) DeleteTask(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id)
	return err
}

func (s *SQLite) AppendTaskRun(ctx context.Context, r *TaskRun) error {
	if r.ID == "" {
		r.ID = newUUID()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO task_runs (id, task_id, agent_id, status, exit_code, output, started_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.TaskID, r.AgentID, r.Status, r.ExitCode, r.Output, r.StartedAt.Unix(), nullInt64(r.FinishedAt))
	return err
}

func (s *SQLite) QueryTaskRuns(ctx context.Context, taskID string, limit int) ([]*TaskRun, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, task_id, agent_id, status, exit_code, output, started_at,
       COALESCE(finished_at, 0)
FROM task_runs WHERE task_id = ? ORDER BY started_at DESC LIMIT ?`, taskID, limit)
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

func (s *SQLite) UpdateTaskLastRun(ctx context.Context, taskID, status, outputTail string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE tasks SET last_run_at=?, last_status=?, last_output_tail=? WHERE id=?`,
		at.Unix(), status, outputTail, taskID)
	return err
}

// ---- 阶段二：frp 配置 / 状态 ----

func (s *SQLite) SaveFrpConfig(ctx context.Context, c *FrpConfig) error {
	proxies, _ := json.Marshal(c.Proxies)
	if c.StateVersion == 0 {
		c.StateVersion = 1
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO frp_configs (kind, agent_id, server_addr, server_port, token, proxies, state_version, enabled, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(kind, agent_id) DO UPDATE SET
  server_addr=excluded.server_addr, server_port=excluded.server_port, token=excluded.token,
  proxies=excluded.proxies, state_version=excluded.state_version, enabled=excluded.enabled,
  updated_at=excluded.updated_at`,
		c.Kind, c.AgentID, c.ServerAddr, c.ServerPort, c.Token, string(proxies),
		c.StateVersion, enabledInt(c.Enabled), time.Now().Unix())
	return err
}

func (s *SQLite) GetFrpConfig(ctx context.Context, kind, agentID string) (*FrpConfig, error) {
	var c FrpConfig
	var proxies string
	var stateVersion, enabled, updatedAt int64
	err := s.db.QueryRowContext(ctx, `
SELECT kind, agent_id, server_addr, server_port, token, proxies, state_version, enabled, updated_at
FROM frp_configs WHERE kind = ? AND agent_id = ?`, kind, agentID).
		Scan(&c.Kind, &c.AgentID, &c.ServerAddr, &c.ServerPort, &c.Token, &proxies, &stateVersion, &enabled, &updatedAt)
	if err != nil {
		return nil, err
	}
	c.Proxies = parseTunnels(proxies)
	c.StateVersion = uint64(stateVersion)
	c.Enabled = enabled != 0
	c.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return &c, nil
}

func (s *SQLite) ListFrpConfigs(ctx context.Context) ([]*FrpConfig, error) {
	rows, err := s.db.QueryContext(ctx, `
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
		var stateVersion, enabled, updatedAt int64
		if err := rows.Scan(&c.Kind, &c.AgentID, &c.ServerAddr, &c.ServerPort, &c.Token, &proxies,
			&stateVersion, &enabled, &updatedAt); err != nil {
			return nil, err
		}
		c.Proxies = parseTunnels(proxies)
		c.StateVersion = uint64(stateVersion)
		c.Enabled = enabled != 0
		c.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		out = append(out, &c)
	}
	return out, rows.Err()
}

func (s *SQLite) UpsertFrpStatus(ctx context.Context, agentID string, st *FrpStatus) error {
	tunnels, _ := json.Marshal(st.Tunnels)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO frp_status (agent_id, running, frp_version, error, tunnels, ts)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(agent_id) DO UPDATE SET
  running=excluded.running, frp_version=excluded.frp_version, error=excluded.error,
  tunnels=excluded.tunnels, ts=excluded.ts`,
		agentID, enabledInt(st.Running), st.FrpVersion, st.Error, string(tunnels), st.TS.Unix())
	return err
}

func (s *SQLite) DeleteFrpConfig(ctx context.Context, kind, agentID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM frp_configs WHERE kind = ? AND agent_id = ?`, kind, agentID)
	return err
}

func (s *SQLite) QueryFrpStatus(ctx context.Context, agentID string) (*FrpStatus, error) {
	var st FrpStatus
	var running, ts int64
	var tunnels string
	err := s.db.QueryRowContext(ctx, `
SELECT running, frp_version, error, tunnels, ts
FROM frp_status WHERE agent_id = ?`, agentID).
		Scan(&running, &st.FrpVersion, &st.Error, &tunnels, &ts)
	if err != nil {
		return nil, err
	}
	st.Running = running != 0
	_ = json.Unmarshal([]byte(tunnels), &st.Tunnels)
	st.TS = time.Unix(ts, 0).UTC()
	return &st, nil
}

// parseTunnels 反序列化 frp_configs.proxies JSON 列（容错空串/null）。
func parseTunnels(s string) []FrpTunnel {
	var out []FrpTunnel
	if s == "" || s == "null" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// enabledInt 布尔 → 0/1（SQLite 无 BOOLEAN）。
func enabledInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullInt64 可空时间 → sql.NullInt64（未设置 = NULL）。
func nullInt64(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.Unix(), Valid: true}
}

func (s *SQLite) Cleanup(ctx context.Context, retention time.Duration) (int64, error) {
	cutoff := time.Now().Add(-retention).Unix()
	var total int64
	for _, q := range []string{
		`DELETE FROM host_metrics WHERE ts < ?`,
		`DELETE FROM disk_metrics WHERE ts < ?`,
		`DELETE FROM container_metrics WHERE ts < ?`,
		`DELETE FROM alert_events WHERE started_at < ?`,
	} {
		res, err := s.db.ExecContext(ctx, q, cutoff)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

func (s *SQLite) Close() error {
	return s.db.Close()
}

var _ Store = (*SQLite)(nil)

var ErrClosed = errors.New("store: closed")
