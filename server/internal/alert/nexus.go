// Nexus 事件上报：把阈值告警作为**事件**推送到 nexus —— 个人基础设施的统一事件/通知中心，
// 由 nexus 负责去重、路由与发送飞书（本服务不再直连飞书）。
//
// 每次发送实时读 settings 的 nexus_url / nexus_ingest_token / nexus_source（改配置即时生效）；
// 未配置则跳过并返回错误提示，不阻塞评估。
//
// 安全：ingest token 绝不写入日志，只出现在 Authorization 头里。
//
// 契约（nexus POST /api/v1/events）：见 server/internal/alert/nexus_test.go 与 DESIGN.md。
//   - event_id 唯一、重试需字节一致（否则 409）；本服务按「firing=事件ID / resolved=ID-resolved /
//     重发=ID-rN」构造**确定性** ID，保证重试幂等。
//   - status=resolved 必须带 alert_key；alert_key=ruleID|agentID|entityID，稳定 → firing/resolved 折叠。
package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"litesentry/server/internal/store"
)

// settings 键（存 DB；设置页可改，无需重启）。
const (
	keyNexusURL    = "nexus_url"
	keyNexusToken  = "nexus_ingest_token"
	keyNexusSource = "nexus_source"

	defaultNexusSource = "litesentry"
	nexusServiceName   = "litesentry"
)

// metricLabels 指标 → 中文名（与前端「告警规则」页展示一致）。
var metricLabels = map[string]string{
	MetricCPU:      "CPU 使用率",
	MetricMem:      "内存使用率",
	MetricLoad1:    "系统负载 1m",
	MetricDiskPct:  "磁盘使用率",
	MetricContCPU:  "容器 CPU 使用率",
	MetricContMem:  "容器内存使用率",
	MetricContDown: "容器停止",
	MetricOffline:  "节点离线",
}

func metricLabel(m string) string {
	if l, ok := metricLabels[m]; ok {
		return l
	}
	return m
}

func isPctMetric(m string) bool {
	switch m {
	case MetricCPU, MetricMem, MetricDiskPct, MetricContCPU, MetricContMem:
		return true
	}
	return false
}

// nexusEventRequest 是 nexus POST /api/v1/events 的请求体（字段名与 nexus 契约逐字一致）。
type nexusEventRequest struct {
	EventID    string            `json:"event_id"`
	Source     string            `json:"source"`
	Service    string            `json:"service"`
	EventType  string            `json:"event_type"`
	Severity   string            `json:"severity"`
	Title      string            `json:"title"`
	Summary    string            `json:"summary"`
	OccurredAt string            `json:"occurred_at"`
	Host       string            `json:"host,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Details    map[string]any    `json:"details,omitempty"`
	AlertKey   string            `json:"alert_key,omitempty"`
	Status     string            `json:"status,omitempty"`
	Category   string            `json:"category,omitempty"`
}

// notify 上报一条告警事件到 nexus（Engine.Notify 的默认实现）。返回发送时间。
func (e *Engine) notify(ctx context.Context, n Notification) (time.Time, error) {
	return sendNexus(ctx, e.st, n)
}

// SendTest 发送一条测试事件（设置页「发送测试」按钮调用）。未配置 nexus 时报错。
func SendTest(ctx context.Context, st store.Store) error {
	n := Notification{
		Event: &store.AlertEvent{
			ID:        "litesentry-test-" + store.NewID(),
			RuleName:  "通知连通性测试",
			AgentName: "litesentry",
			Severity:  "info",
			State:     "firing",
			StartedAt: time.Now(),
		},
		Status: "firing",
	}
	_, err := sendNexus(ctx, st, n)
	return err
}

// sendNexus 读设置并 POST 事件到 nexus（失败 1 次重试）。
func sendNexus(ctx context.Context, st store.Store, n Notification) (time.Time, error) {
	rawURL, err := st.GetSetting(ctx, keyNexusURL)
	if err != nil {
		return time.Time{}, fmt.Errorf("读取 nexus 地址失败: %w", err)
	}
	endpoint := strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if endpoint == "" {
		return time.Time{}, fmt.Errorf("nexus 地址未配置（设置页填写后保存）")
	}
	token, _ := st.GetSetting(ctx, keyNexusToken)
	if token == "" {
		return time.Time{}, fmt.Errorf("nexus ingest token 未配置（设置页填写后保存）")
	}
	source, _ := st.GetSetting(ctx, keyNexusSource)
	if source == "" {
		source = defaultNexusSource
	}
	// nexus 契约：resolved 事件必须带 alert_key（用于关联原告警）；提前拦截给出清晰错误。
	if n.Status == "resolved" && n.AlertKey == "" {
		return time.Time{}, fmt.Errorf("resolved 事件缺少 alert_key")
	}

	now := time.Now().UTC()
	reqBody := buildNexusRequest(source, n, now)
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return time.Time{}, fmt.Errorf("序列化事件失败: %w", err)
	}
	endpoint += "/api/v1/events"

	client := &http.Client{Timeout: 10 * time.Second}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
		if err != nil {
			return time.Time{}, err
		}
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return now, nil
			}
			lastErr = fmt.Errorf("nexus 返回 HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		if attempt == 0 {
			time.Sleep(time.Second)
		}
	}
	// 注意：lastErr 只含状态码/网络错误，绝不含 token。
	return time.Time{}, lastErr
}

// buildNexusRequest 把 litesentry 告警映射为 nexus 事件（见本文件顶部契约）。
func buildNexusRequest(source string, n Notification, now time.Time) nexusEventRequest {
	ev := n.Event
	status := n.Status
	if status == "" {
		status = "firing"
	}
	stateCN := "触发"
	if status == "resolved" {
		stateCN = "恢复"
	}

	// 对象描述：主机，容器规则再缀容器名。
	obj := ev.AgentName
	if ev.EntityName != "" {
		obj = fmt.Sprintf("%s · 容器 %s", ev.AgentName, ev.EntityName)
	}

	unit := ""
	if isPctMetric(ev.Metric) {
		unit = "%"
	}
	occurred := ev.StartedAt
	if occurred.IsZero() {
		occurred = now
	}

	eventType := "alert." + ev.Metric
	if ev.Metric == "" {
		eventType = "alert.test"
	}

	labels := map[string]string{
		"rule_id":   ev.RuleID,
		"rule_name": ev.RuleName,
		"metric":    ev.Metric,
		"agent_id":  ev.AgentID,
		"node":      ev.AgentName,
	}
	if ev.EntityID != "" {
		labels["entity_id"] = ev.EntityID
	}
	details := map[string]any{
		"value":     ev.Value,
		"threshold": ev.Threshold,
		"severity":  ev.Severity,
		"state":     status,
	}

	return nexusEventRequest{
		EventID:   ev.ID,
		Source:    source,
		Service:   nexusServiceName,
		EventType: eventType,
		Severity:  nexusSeverity(ev.Severity),
		Title:     runeTruncate(fmt.Sprintf("[%s] %s · %s", stateCN, ev.RuleName, obj), 256),
		Summary: runeTruncate(fmt.Sprintf(
			"规则：%s\n对象：%s\n当前值：%.2f%s（阈值：%.2f%s）\n时间：%s",
			ev.RuleName, obj, ev.Value, unit, ev.Threshold, unit,
			occurred.Local().Format("2006-01-02 15:04:05")), 2048),
		OccurredAt: occurred.Format(time.RFC3339),
		Host:       ev.AgentName,
		Labels:     labels,
		Details:    details,
		AlertKey:   n.AlertKey,
		Status:     status,
		Category:   "ops",
	}
}

// nexusSeverity 映射 litesentry 级别到 nexus 枚举（INFO/WARNING/ERROR/CRITICAL；无默认值，必给）。
func nexusSeverity(s string) string {
	switch strings.ToLower(s) {
	case "critical":
		return "CRITICAL"
	case "warning":
		return "WARNING"
	case "info":
		return "INFO"
	default:
		return "WARNING"
	}
}

// runeTruncate 按**字符**（非字节）截断，避免中文被切出非法 UTF-8；超限时留省略号。
func runeTruncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}
