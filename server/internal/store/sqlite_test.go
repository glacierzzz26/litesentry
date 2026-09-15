package store

import (
	"context"
	"testing"
	"time"
)

// 新建内存库（WAL 模式下 :memory: 多连接需 shared cache，且单写者 1 连接，足够测试）。
func newTestSQLite(t *testing.T) *SQLite {
	t.Helper()
	s, err := NewSQLite("file:litesentry_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// S4.1：Cleanup 按 started_at 裁剪超保留期的 task_runs，近期行保留。
func TestCleanupPrunesTaskRuns(t *testing.T) {
	s := newTestSQLite(t)
	ctx := context.Background()

	old := time.Now().Add(-2 * time.Hour).UTC()
	recent := time.Now().Add(-time.Minute).UTC()
	code := int32(0)
	for _, st := range []time.Time{old, recent} {
		if err := s.AppendTaskRun(ctx, &TaskRun{
			TaskID: "t-clean", AgentID: "a", Status: "ok", ExitCode: &code,
			Output: "x", StartedAt: st, FinishedAt: &st,
		}); err != nil {
			t.Fatalf("AppendTaskRun: %v", err)
		}
	}

	n, err := s.Cleanup(ctx, time.Hour)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if n != 1 {
		t.Fatalf("保留期 1h 应恰好清理 1 行（2h 前的旧行），实际 %d", n)
	}

	runs, err := s.QueryTaskRuns(ctx, "t-clean", 50)
	if err != nil {
		t.Fatalf("QueryTaskRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("期望保留 1 行，实际 %d", len(runs))
	}
	if d := runs[0].StartedAt.Sub(recent); d > time.Second || d < -time.Second {
		t.Fatalf("保留的应是近期行，实际 started_at=%v", runs[0].StartedAt)
	}
}

// series 保留裁剪：Cleanup 按 ts 裁剪超保留期的 series，近期行保留（此前 series 从不清理）。
func TestCleanupPrunesSeries(t *testing.T) {
	s := newTestSQLite(t)
	ctx := context.Background()

	old := time.Now().Add(-2 * time.Hour).UTC()
	recent := time.Now().Add(-time.Minute).UTC()
	for i, ts := range []time.Time{old, recent} {
		if err := s.AppendSeries(ctx, "a", []*Series{{
			Name: "host.info", Tags: map[string]string{"hostname": "h"},
			Fields: map[string]float64{"cpu_pct": float64(i)}, TS: ts,
		}}); err != nil {
			t.Fatalf("AppendSeries: %v", err)
		}
	}

	n, err := s.Cleanup(ctx, time.Hour)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if n != 1 {
		t.Fatalf("保留期 1h 应恰好清理 1 行（2h 前的旧 series），实际 %d", n)
	}

	var cnt int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM series`).Scan(&cnt); err != nil {
		t.Fatalf("count series: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("期望保留 1 行 series，实际 %d", cnt)
	}
}

// S4.2：SetTaskRunNow 置位/清除持久化，GetTask 回读一致。
func TestSetTaskRunNowRoundTrip(t *testing.T) {	s := newTestSQLite(t)
	ctx := context.Background()

	tk := &Task{Name: "rn", Cron: "0 0 1 1 *", PluginID: "hello", TargetAgentID: "a", Enabled: true}
	if err := s.SaveTask(ctx, tk); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}

	got, err := s.GetTask(ctx, tk.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.RunNow {
		t.Fatal("新建任务 run_now 应为 false")
	}

	if err := s.SetTaskRunNow(ctx, tk.ID, true); err != nil {
		t.Fatalf("SetTaskRunNow(true): %v", err)
	}
	if got, _ = s.GetTask(ctx, tk.ID); !got.RunNow {
		t.Fatal("置位后 run_now 应为 true")
	}

	if err := s.SetTaskRunNow(ctx, tk.ID, false); err != nil {
		t.Fatalf("SetTaskRunNow(false): %v", err)
	}
	if got, _ = s.GetTask(ctx, tk.ID); got.RunNow {
		t.Fatal("清除后 run_now 应为 false")
	}
}

// 插件多版本：(id, version) 复合主键允许同 id 并存多版本；被指派版本拒删，取消指派后可删。
func TestPluginMultiVersionAndDelete(t *testing.T) {
	s := newTestSQLite(t)
	ctx := context.Background()

	for _, v := range []string{"0.1.0", "0.2.0"} {
		if err := s.SavePlugin(ctx, &Plugin{
			ID: "hello", Name: "hello", Kind: "binary", Version: v,
			SHA256: "deadbeef", Data: []byte("bin-" + v),
		}); err != nil {
			t.Fatalf("SavePlugin %s: %v", v, err)
		}
	}
	rows, err := s.ListPlugins(ctx)
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("同 id 应并存 2 个版本，实际 %d", len(rows))
	}

	// 指派 0.2.0 → 该版本被引用，删除应报错
	if err := s.AssignPlugin(ctx, &AgentPlugin{AgentID: "a", PluginID: "hello", Version: "0.2.0"}); err != nil {
		t.Fatalf("AssignPlugin: %v", err)
	}
	if err := s.DeletePluginVersion(ctx, "hello", "0.2.0"); err == nil {
		t.Fatal("被指派版本应拒绝删除")
	}
	// 未指派版本可删
	if err := s.DeletePluginVersion(ctx, "hello", "0.1.0"); err != nil {
		t.Fatalf("未指派版本应可删: %v", err)
	}
	// 取消指派后可删
	if err := s.UnassignPlugin(ctx, "a", "hello"); err != nil {
		t.Fatalf("UnassignPlugin: %v", err)
	}
	if err := s.DeletePluginVersion(ctx, "hello", "0.2.0"); err != nil {
		t.Fatalf("取消指派后应可删: %v", err)
	}
}
