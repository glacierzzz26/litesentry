// nexus 事件上报单元测试：用 httptest 捕获请求，断言字段映射、鉴权头、幂等/错误与重试行为。
package alert

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"litesentry/server/internal/store"
)

// settingStore 只实现 nexus 发送用到的 GetSetting/SetSetting。
type settingStore struct {
	store.Store
	m map[string]string
}

func newSettingStore() *settingStore { return &settingStore{m: map[string]string{}} }

func (s *settingStore) GetSetting(_ context.Context, k string) (string, error) { return s.m[k], nil }
func (s *settingStore) SetSetting(_ context.Context, k, v string) error        { s.m[k] = v; return nil }

// withNexus 配置一个指向 httptest 的 store。
func withNexus(url, token, source string) *settingStore {
	s := newSettingStore()
	s.m[keyNexusURL] = url
	s.m[keyNexusToken] = token
	s.m[keyNexusSource] = source
	return s
}

func firingNotice() Notification {
	return Notification{
		Event: &store.AlertEvent{
			ID: "e1", RuleID: "r1", RuleName: "cpu high",
			AgentID: "a1", AgentName: "node1",
			Metric: MetricCPU, Value: 91.5, Threshold: 80, Severity: "warning",
			State: "firing", StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		},
		Status: "firing", AlertKey: "r1|a1|",
	}
}

func TestSendNexusMapping(t *testing.T) {
	var gotAuth, gotCT string
	var got nexusEventRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		if r.URL.Path != "/api/v1/events" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	if _, err := sendNexus(context.Background(), withNexus(srv.URL, "secret-token", ""), firingNotice()); err != nil {
		t.Fatalf("sendNexus: %v", err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if !strings.Contains(gotCT, "application/json") {
		t.Errorf("Content-Type = %q", gotCT)
	}
	if got.EventID != "e1" || got.Source != "litesentry" || got.Service != "litesentry" {
		t.Errorf("id/source/service = %q/%q/%q", got.EventID, got.Source, got.Service)
	}
	if got.Severity != "WARNING" || got.Status != "firing" || got.Category != "ops" {
		t.Errorf("severity/status/category = %q/%q/%q", got.Severity, got.Status, got.Category)
	}
	if got.AlertKey != "r1|a1|" {
		t.Errorf("alert_key = %q", got.AlertKey)
	}
	if got.EventType != "alert."+MetricCPU {
		t.Errorf("event_type = %q", got.EventType)
	}
	if got.Host != "node1" {
		t.Errorf("host = %q", got.Host)
	}
	if _, err := time.Parse(time.RFC3339, got.OccurredAt); err != nil {
		t.Errorf("occurred_at 非 RFC3339: %q", got.OccurredAt)
	}
	if got.Labels["rule_id"] != "r1" || got.Labels["agent_id"] != "a1" {
		t.Errorf("labels = %+v", got.Labels)
	}
}

func TestSendNexusSeverityCritical(t *testing.T) {
	var got nexusEventRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	n := firingNotice()
	n.Event.Severity = "critical"
	if _, err := sendNexus(context.Background(), withNexus(srv.URL, "t", ""), n); err != nil {
		t.Fatalf("sendNexus: %v", err)
	}
	if got.Severity != "CRITICAL" {
		t.Errorf("severity = %q, want CRITICAL", got.Severity)
	}
}

func TestSendNexusResolved(t *testing.T) {
	var got nexusEventRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusOK) // 幂等重发也返回 200
	}))
	defer srv.Close()

	n := firingNotice()
	n.Event.ID = "e1-resolved"
	n.Event.State = "resolved"
	n.Status = "resolved"
	if _, err := sendNexus(context.Background(), withNexus(srv.URL, "t", ""), n); err != nil {
		t.Fatalf("sendNexus: %v", err)
	}
	if got.Status != "resolved" || got.EventID != "e1-resolved" || got.AlertKey != "r1|a1|" {
		t.Errorf("resolved 映射错误: %+v", got)
	}
}

func TestSendNexusResolvedRequiresAlertKey(t *testing.T) {
	n := firingNotice()
	n.Status = "resolved" // AlertKey 保留
	n.AlertKey = ""
	_, err := sendNexus(context.Background(), withNexus("http://x", "t", ""), n)
	if err == nil || !strings.Contains(err.Error(), "alert_key") {
		t.Fatalf("应因缺 alert_key 报错, got %v", err)
	}
}

func TestSendNexusUnconfigured(t *testing.T) {
	if _, err := sendNexus(context.Background(), newSettingStore(), firingNotice()); err == nil {
		t.Fatal("空地址应报错")
	}
	s := withNexus("http://x", "", "")
	if _, err := sendNexus(context.Background(), s, firingNotice()); err == nil {
		t.Fatal("空 token 应报错")
	}
}

func TestSendNexusHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	_, err := sendNexus(context.Background(), withNexus(srv.URL, "topsecret", ""), firingNotice())
	if err == nil {
		t.Fatal("400 应报错")
	}
	// token 绝不能出现在错误串里。
	if strings.Contains(err.Error(), "topsecret") {
		t.Fatalf("错误泄漏 token: %v", err)
	}
}

func TestSendNexusRetryOnFailure(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError) // 首次失败
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	if _, err := sendNexus(context.Background(), withNexus(srv.URL, "t", ""), firingNotice()); err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if n.Load() != 2 {
		t.Errorf("应请求 2 次, got %d", n.Load())
	}
}

func TestSendNexusCustomSource(t *testing.T) {
	var got nexusEventRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	if _, err := sendNexus(context.Background(), withNexus(srv.URL, "t", "my-src"), firingNotice()); err != nil {
		t.Fatalf("sendNexus: %v", err)
	}
	if got.Source != "my-src" {
		t.Errorf("source = %q, want my-src", got.Source)
	}
}

func TestRuneTruncate(t *testing.T) {
	s := strings.Repeat("中", 300)
	got := runeTruncate(s, 256)
	if len([]rune(got)) != 256 {
		t.Errorf("rune 长度 = %d, want 256", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("应留省略号: %q", got)
	}
	if runeTruncate("短", 256) != "短" {
		t.Errorf("未超限不应改动")
	}
}
