// 告警引擎状态机单元测试：用罐头数据的 fakeStore + 记录型 Notify stub 驱动 check()，
// 覆盖 duration 累积、立即触发、恢复 resolve、通知冷却、offline / container_down 布尔指标、< 运算符。
package alert

import (
	"context"
	"testing"
	"time"

	"litesentry/server/internal/store"
)

// fakeStore 实现 store.Store：仅实现引擎用到的查询/事件方法，其余走内嵌空接口（调用即 panic）。
type fakeStore struct {
	store.Store
	rules  []*store.AlertRule
	agents []*store.Agent
	hosts  map[string][]*store.HostSample
	disks  map[string][]*store.DiskSample
	conts  map[string][]*store.ContainerSample
	events []*store.AlertEvent
}

func (f *fakeStore) ListRules(context.Context) ([]*store.AlertRule, error) { return f.rules, nil }
func (f *fakeStore) Agents(context.Context) ([]*store.Agent, error)        { return f.agents, nil }
func (f *fakeStore) QueryHost(_ context.Context, id string, _, _ time.Time) ([]*store.HostSample, error) {
	return f.hosts[id], nil
}
func (f *fakeStore) QueryDisks(_ context.Context, id string, _, _ time.Time) ([]*store.DiskSample, error) {
	return f.disks[id], nil
}
func (f *fakeStore) QueryContainers(_ context.Context, id string, _, _ time.Time) ([]*store.ContainerSample, error) {
	return f.conts[id], nil
}
func (f *fakeStore) AppendEvent(_ context.Context, e *store.AlertEvent) error {
	f.events = append(f.events, e)
	return nil
}
func (f *fakeStore) SetEventNotified(_ context.Context, _ string, _ time.Time) error { return nil }
func (f *fakeStore) ResolveEvent(_ context.Context, id string, at time.Time) error {
	for _, e := range f.events {
		if e.ID == id {
			e.State = "resolved"
			t := at
			e.ResolvedAt = &t
		}
	}
	return nil
}

// notifyRec 记录型 Notify stub：留存每次通知（含 status/alert_key），供断言。
type notifyRec struct {
	notices []Notification
}

func (r *notifyRec) calls() int { return len(r.notices) }

// newTestEngine 构造引擎并把 Notify 换成记录 stub。
func newTestEngine(f *fakeStore) (*Engine, *notifyRec) {
	rec := &notifyRec{}
	e := New(f)
	e.Notify = func(_ context.Context, n Notification) (time.Time, error) {
		rec.notices = append(rec.notices, n)
		return time.Now(), nil
	}
	return e, rec
}

func cpuRule(durationS int64) *store.AlertRule {
	return &store.AlertRule{
		ID: "r1", Name: "cpu high", Metric: MetricCPU, Op: ">", Threshold: 80,
		DurationS: durationS, Enabled: true,
	}
}

func agent() *store.Agent { return &store.Agent{AgentID: "a1", Hostname: "node1"} }

func TestDurationBasedFiring(t *testing.T) {
	f := &fakeStore{
		rules: []*store.AlertRule{cpuRule(180)},
		hosts: map[string][]*store.HostSample{"a1": {{AgentID: "a1", CPUPct: 90}}},
	}
	e, rec := newTestEngine(f)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()
	a := agent()

	// 前两次评估未达 180s：不触发
	e.check(ctx, base, cpuRule(180), a, "", "", 90)
	e.check(ctx, base.Add(60*time.Second), cpuRule(180), a, "", "", 90)
	if len(f.events) != 0 || rec.calls() != 0 {
		t.Fatalf("提前触发: events=%d notified=%d", len(f.events), rec.calls())
	}
	// 恰好 180s：触发
	e.check(ctx, base.Add(180*time.Second), cpuRule(180), a, "", "", 90)
	if len(f.events) != 1 {
		t.Fatalf("events = %d, want 1", len(f.events))
	}
	if f.events[0].State != "firing" {
		t.Errorf("state = %s, want firing", f.events[0].State)
	}
	if rec.calls() != 1 {
		t.Errorf("notify calls = %d, want 1", rec.calls())
	}
	if rec.notices[0].Status != "firing" || rec.notices[0].AlertKey != "r1|a1|" {
		t.Errorf("firing 通知身份错误: %+v", rec.notices[0])
	}
}

func TestImmediateFiring(t *testing.T) {
	f := &fakeStore{rules: []*store.AlertRule{cpuRule(0)}}
	e, _ := newTestEngine(f)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e.check(context.Background(), base, cpuRule(0), agent(), "", "", 95)
	if len(f.events) != 1 || f.events[0].State != "firing" {
		t.Fatalf("duration=0 应立即触发: events=%+v", f.events)
	}
}

func TestResolve(t *testing.T) {
	f := &fakeStore{rules: []*store.AlertRule{cpuRule(0)}}
	e, rec := newTestEngine(f)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()
	a := agent()

	e.check(ctx, base, cpuRule(0), a, "", "", 90)
	if len(f.events) != 1 {
		t.Fatalf("should fire first, events=%d", len(f.events))
	}
	e.check(ctx, base.Add(30*time.Second), cpuRule(0), a, "", "", 10) // 恢复
	if f.events[0].State != "resolved" || f.events[0].ResolvedAt == nil {
		t.Fatalf("恢复后应 resolve: %+v", f.events[0])
	}
	// 恢复也应通知一次：firing + resolved = 2 次，且 resolved 带 alert_key。
	if rec.calls() != 2 {
		t.Fatalf("应触发+恢复各通知一次, got %d", rec.calls())
	}
	r := rec.notices[1]
	if r.Status != "resolved" || r.AlertKey != "r1|a1|" || r.Event.State != "resolved" {
		t.Fatalf("resolved 通知身份错误: %+v", r)
	}
}

func TestNotifyCooldown(t *testing.T) {
	f := &fakeStore{rules: []*store.AlertRule{cpuRule(0)}}
	e, rec := newTestEngine(f)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()
	a := agent()

	e.check(ctx, base, cpuRule(0), a, "", "", 90) // 触发并通知
	if rec.calls() != 1 {
		t.Fatalf("首次触发应通知, got %d", rec.calls())
	}
	fireID := rec.notices[0].Event.ID
	e.check(ctx, base.Add(5*time.Minute), cpuRule(0), a, "", "", 90) // 5min < 30min 冷却
	if rec.calls() != 1 {
		t.Fatalf("冷却期内不应重发, got %d", rec.calls())
	}
	e.check(ctx, base.Add(30*time.Minute), cpuRule(0), a, "", "", 90) // 到期重发
	if rec.calls() != 2 {
		t.Fatalf("冷却到期应重发, got %d", rec.calls())
	}
	if len(f.events) != 1 {
		t.Errorf("重发不应新建事件, events=%d", len(f.events))
	}
	// 重发用确定性新 event_id（fireID-r1），避免与首发内容不同而撞 nexus 409。
	if got := rec.notices[1].Event.ID; got != fireID+"-r1" {
		t.Errorf("重发 event_id = %q, want %q", got, fireID+"-r1")
	}
}

func TestOfflineMetric(t *testing.T) {
	rule := &store.AlertRule{ID: "r2", Name: "offline", Metric: MetricOffline, Op: ">", Threshold: 0.5, Enabled: true}
	offline := &store.Agent{AgentID: "a1", Hostname: "off-node", LastSeen: time.Now().Add(-10 * time.Minute)}
	online := &store.Agent{AgentID: "a2", Hostname: "on-node", LastSeen: time.Now().Add(-1 * time.Minute)}
	f := &fakeStore{rules: []*store.AlertRule{rule}, agents: []*store.Agent{offline, online}}
	e, _ := newTestEngine(f)

	ctx := context.Background()
	now := time.Now().UTC()
	e.evaluateAgent(ctx, now, offline, f.rules) // 10min 无心跳 → 离线
	if len(f.events) != 1 {
		t.Fatalf("离线节点应触发: events=%d", len(f.events))
	}
	if f.events[0].AgentID != "a1" || f.events[0].Value != 1 {
		t.Errorf("事件异常: %+v", f.events[0])
	}
	e.evaluateAgent(ctx, now, online, f.rules) // 1min 有心跳 → 在线
	if len(f.events) != 1 {
		t.Fatalf("在线节点不应触发: events=%d", len(f.events))
	}
}

func TestLessThanOp(t *testing.T) {
	rule := &store.AlertRule{ID: "r3", Name: "cpu idle low", Metric: MetricCPU, Op: "<", Threshold: 20, Enabled: true}
	f := &fakeStore{rules: []*store.AlertRule{rule}}
	e, _ := newTestEngine(f)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e.check(context.Background(), base, rule, agent(), "", "", 10)
	if len(f.events) != 1 || f.events[0].State != "firing" {
		t.Fatalf("value=10 < 20 应触发: %+v", f.events)
	}
}

func TestContainerDown(t *testing.T) {
	rule := &store.AlertRule{ID: "r4", Name: "web down", Metric: MetricContDown, Op: ">", Threshold: 0.5, Enabled: true}
	f := &fakeStore{
		rules: []*store.AlertRule{rule},
		conts: map[string][]*store.ContainerSample{"a1": {
			{AgentID: "a1", ContainerID: "c1", Name: "web", State: "exited"},
			{AgentID: "a1", ContainerID: "c2", Name: "db", State: "running"},
		}},
	}
	e, _ := newTestEngine(f)
	e.evaluateAgent(context.Background(), time.Now().UTC(), agent(), f.rules)
	if len(f.events) != 1 {
		t.Fatalf("应只触发停止的容器: events=%d", len(f.events))
	}
	if f.events[0].EntityID != "c1" || f.events[0].Value != 1 {
		t.Errorf("事件对象错误: %+v", f.events[0])
	}
}
