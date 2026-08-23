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
