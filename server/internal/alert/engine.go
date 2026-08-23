// Package alert 阈值评估引擎。
//
// 周期性（默认 60s）扫描各节点最新样本，对照已启用规则判定是否告警：
// 持续超限 duration_s 才触发（firing），恢复后自动 resolve；
// 同一事件通知有 30min 冷却，避免刷屏。
//
// 状态机仅存内存：Server 重启后重新累积 duration（可接受，配合冷却窗口）。
package alert

import (
	"context"
	"log"
	"sync"
	"time"

	"litesentry/server/internal/store"
)

// 支持指标（与前端「告警规则」页下拉保持一致）。
const (
	MetricCPU      = "cpu_pct"        // 主机 CPU 使用率 %
	MetricMem      = "mem_pct"        // 主机内存使用率 %
	MetricLoad1    = "load_1m"        // 主机负载 1m
	MetricDiskPct  = "disk_pct"       // 磁盘使用率 %（任一挂载点）
	MetricContCPU  = "container_cpu"  // 容器 CPU 使用率 %
	MetricContMem  = "container_mem"  // 容器内存使用率 %
	MetricContDown = "container_down" // 容器停止（state ≠ running）
	MetricOffline  = "offline"        // 节点离线（最近心跳 > 5min）
)

// offlineAfter 与 api 包一致：连续 5 分钟无心跳判定离线。
const offlineAfter = 5 * time.Minute

// notifyCooldown 同一事件重复通知的最小间隔。
const notifyCooldown = 30 * time.Minute

// breach 单规则 × 单对象（主机/容器）的评估状态。
type breach struct {
	breachStart time.Time
	firing      bool
	eventID     string // 当前 firing 事件的 ID（恢复时 resolve）
	lastNotify  time.Time
}

// Engine 评估引擎。零成本构造；用 Run 启动。
type Engine struct {
	st    store.Store
	mu    sync.Mutex
	state map[string]*breach // key = ruleID|agentID|entityID

	// Notify 告警通知注入点（默认飞书发送）。返回发送成功的时间；
	// 单元测试可替换为记录型 stub，避免真实 HTTP 调用。
	Notify func(ctx context.Context, ev *store.AlertEvent) (time.Time, error)
}

func New(st store.Store) *Engine {
	e := &Engine{st: st, state: make(map[string]*breach)}
	e.Notify = e.notify
	return e
}

// Run 阻塞执行评估循环（interval ≤ 0 时用 60s）。启动即评估一次。
func (e *Engine) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	e.evaluate(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.evaluate(ctx)
		}
	}
}

func (e *Engine) evaluate(ctx context.Context) {
	rules, err := e.st.ListRules(ctx)
	if err != nil {
		log.Printf("alert: list rules: %v", err)
		return
	}
	var enabled []*store.AlertRule
	for _, r := range rules {
		if r.Enabled {
			enabled = append(enabled, r)
		}
	}
	if len(enabled) == 0 {
		return
	}
	agents, err := e.st.Agents(ctx)
	if err != nil {
		log.Printf("alert: list agents: %v", err)
		return
	}
	now := time.Now().UTC()
	for _, a := range agents {
		e.evaluateAgent(ctx, now, a, enabled)
	}
}

// agentSnap 某节点本评估周期内的最新样本快照（各规则复用，避免 N 次查询）。
type agentSnap struct {
	host       *store.HostSample
	diskMaxPct float64
	diskHas    bool
	containers map[string]*store.ContainerSample // 每容器最新一条
}

func (e *Engine) evaluateAgent(ctx context.Context, now time.Time, a *store.Agent, rules []*store.AlertRule) {
	snap := &agentSnap{containers: make(map[string]*store.ContainerSample)}
	from := now.Add(-5 * time.Minute)

	if hs, err := e.st.QueryHost(ctx, a.AgentID, from, now); err == nil && len(hs) > 0 {
		snap.host = hs[len(hs)-1]
	}
	if ds, err := e.st.QueryDisks(ctx, a.AgentID, from, now); err == nil {
		for _, d := range ds {
			if d.Total == 0 {
				continue
			}
			if pct := float64(d.Used) / float64(d.Total) * 100; !snap.diskHas || pct > snap.diskMaxPct {
				snap.diskMaxPct, snap.diskHas = pct, true
			}
		}
	}
	if cs, err := e.st.QueryContainers(ctx, a.AgentID, from, now); err == nil {
		for _, c := range cs {
			if cur, ok := snap.containers[c.ContainerID]; !ok || c.Ts.After(cur.Ts) {
				snap.containers[c.ContainerID] = c
			}
		}
	}

	for _, r := range rules {
		switch r.Metric {
		case MetricCPU, MetricMem, MetricLoad1, MetricDiskPct:
			if v, ok := e.hostValue(r.Metric, snap); ok {
				e.check(ctx, now, r, a, "", "", v)
			}
		case MetricOffline:
			v := 0.0
			if time.Since(a.LastSeen) > offlineAfter {
				v = 1
			}
			e.check(ctx, now, r, a, "", "", v)
		case MetricContCPU, MetricContMem, MetricContDown:
			for id, c := range snap.containers {
				var v float64
				switch r.Metric {
				case MetricContCPU:
					v = c.CPUPct
				case MetricContMem:
					if c.MemLimit > 0 {
						v = float64(c.MemUsage) / float64(c.MemLimit) * 100
					}
				case MetricContDown:
					if c.State != "running" {
						v = 1
					}
				}
				e.check(ctx, now, r, a, id, c.Name, v)
			}
		}
	}
}

// hostValue 取主机类指标当前值。
func (e *Engine) hostValue(metric string, snap *agentSnap) (float64, bool) {
	h := snap.host
	if h == nil {
		return 0, false
	}
	switch metric {
	case MetricCPU:
		return h.CPUPct, true
	case MetricMem:
		if h.MemTotal == 0 {
			return 0, false
		}
		return float64(h.MemUsed) / float64(h.MemTotal) * 100, true
	case MetricLoad1:
		return h.Load1, true
	case MetricDiskPct:
		return snap.diskMaxPct, snap.diskHas
	}
	return 0, false
}

// check 评估单条规则在单个对象上的状态：驱动内存状态机，把需要落库/通知的动作
// 在锁外执行（避免 HTTP 通知阻塞后续评估）。
func (e *Engine) check(ctx context.Context, now time.Time, r *store.AlertRule, a *store.Agent, entityID, entityName string, value float64) {
	key := r.ID + "|" + a.AgentID + "|" + entityID
	breaching := value > r.Threshold
	if r.Op == "<" {
		breaching = value < r.Threshold
	}
	dur := time.Duration(r.DurationS) * time.Second

	var toFire *store.AlertEvent
	var renotifyID string
	var resolveID string
	var deleteKey bool

	e.mu.Lock()
	st := e.state[key]
	if st == nil {
		st = &breach{}
		e.state[key] = st
	}
	if breaching {
		if st.breachStart.IsZero() {
			st.breachStart = now
		}
		if !st.firing && now.Sub(st.breachStart) >= dur {
			st.firing = true
			toFire = &store.AlertEvent{
				ID: store.NewID(), RuleID: r.ID, RuleName: r.Name,
				AgentID: a.AgentID, AgentName: a.Hostname,
				EntityID: entityID, EntityName: entityName,
				Metric: r.Metric, Value: value, Threshold: r.Threshold,
				Severity: r.Severity, State: "firing", StartedAt: now,
			}
			st.eventID = toFire.ID
			st.lastNotify = now
		} else if st.firing && now.Sub(st.lastNotify) >= notifyCooldown {
			st.lastNotify = now
			renotifyID = st.eventID
		}
	} else {
		if st.firing {
			resolveID = st.eventID
		}
		deleteKey = true
	}
	e.mu.Unlock()

	if toFire != nil {
		if err := e.st.AppendEvent(ctx, toFire); err != nil {
			log.Printf("alert: append event: %v", err)
			return
		}
		if nt, err := e.Notify(ctx, toFire); err != nil {
			// 通知失败不重试重发，留给冷却窗口后的下一次（或手动修复配置）
			log.Printf("alert: notify rule=%q agent=%q: %v", toFire.RuleName, toFire.AgentName, err)
		} else if err := e.st.SetEventNotified(ctx, toFire.ID, nt); err != nil {
			log.Printf("alert: set notified_at: %v", err)
		}
	}
	if renotifyID != "" {
		// 冷却到期仍超限：重发一次通知（不新建事件）
		ev := &store.AlertEvent{RuleName: r.Name, AgentID: a.AgentID, AgentName: a.Hostname,
			EntityID: entityID, EntityName: entityName, Metric: r.Metric,
			Value: value, Threshold: r.Threshold, Severity: r.Severity, StartedAt: now}
		if nt, err := e.Notify(ctx, ev); err != nil {
			log.Printf("alert: renotify rule=%q agent=%q: %v", ev.RuleName, ev.AgentName, err)
		} else if err := e.st.SetEventNotified(ctx, renotifyID, nt); err != nil {
			log.Printf("alert: set notified_at: %v", err)
		}
	}
	if resolveID != "" {
		if err := e.st.ResolveEvent(ctx, resolveID, now); err != nil {
			log.Printf("alert: resolve event %s: %v", resolveID, err)
		}
	}
	if deleteKey {
		e.mu.Lock()
		delete(e.state, key)
		e.mu.Unlock()
	}
}
