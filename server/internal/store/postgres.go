package store

import (
	"context"
	"database/sql"
	"encoding/json"
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
	return err
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

func (p *Postgres) Cleanup(ctx context.Context, retention time.Duration) (int64, error) {
	cutoff := time.Now().Add(-retention).Unix()
	var total int64
	for _, q := range []string{
		`DELETE FROM host_metrics WHERE ts < $1`,
		`DELETE FROM disk_metrics WHERE ts < $1`,
		`DELETE FROM container_metrics WHERE ts < $1`,
		`DELETE FROM alert_events WHERE started_at < $1`,
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
