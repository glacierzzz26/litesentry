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

// S4.2：SetTaskRunNow 置位/清除持久化，GetTask 回读一致。
func TestSetTaskRunNowRoundTrip(t *testing.T) {
	s := newTestSQLite(t)
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
