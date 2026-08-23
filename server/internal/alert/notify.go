// 飞书自定义机器人通知：sign = base64(HMAC-SHA256(key=secret, msg=timestamp+"\n"+secret))。
// 每次发送实时读 settings（改配置即时生效）；未配置 webhook 则跳过并返回错误提示。
// 安全：secret 绝不写入日志，只出现在 HMAC 计算中。
package alert

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"litesentry/server/internal/store"
)

const (
	keyFeishuWebhook = "feishu_webhook"
	keyFeishuSecret  = "feishu_secret"
)

type feishuPayload struct {
	Timestamp string            `json:"timestamp"`
	Sign      string            `json:"sign"`
	MsgType   string            `json:"msg_type"`
	Content   map[string]string `json:"content"`
}

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

// notify 发送一条告警通知。返回发送时间。
func (e *Engine) notify(ctx context.Context, ev *store.AlertEvent) (time.Time, error) {
	return sendFeishu(ctx, e.st, feishuText(ev))
}

// SendTest 发送测试消息（设置页「发送测试」按钮调用）。未配置 webhook 时报错。
func SendTest(ctx context.Context, st store.Store) error {
	_, err := sendFeishu(ctx, st, "litesentry 测试消息：飞书机器人配置成功 ✅")
	return err
}

// sendFeishu 读设置并 POST 到飞书 webhook（失败 1 次重试）。
func sendFeishu(ctx context.Context, st store.Store, text string) (time.Time, error) {
	webhook, err := st.GetSetting(ctx, keyFeishuWebhook)
	if err != nil {
		return time.Time{}, fmt.Errorf("读取 webhook 设置失败: %w", err)
	}
	if webhook == "" {
		return time.Time{}, fmt.Errorf("飞书 webhook 未配置（设置页填写后保存）")
	}
	secret, _ := st.GetSetting(ctx, keyFeishuSecret)

	now := time.Now()
	ts := fmt.Sprintf("%d", now.Unix())
	sign := ""
	if secret != "" {
		sign = feishuSign(secret, ts)
	}
	raw, _ := json.Marshal(feishuPayload{
		Timestamp: ts,
		Sign:      sign,
		MsgType:   "text",
		Content:   map[string]string{"text": text},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(raw))
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	client := &http.Client{Timeout: 10 * time.Second}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return now, nil
			}
			lastErr = fmt.Errorf("飞书返回 HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		if attempt == 0 {
			time.Sleep(time.Second)
		}
	}
	return time.Time{}, lastErr
}

// feishuSign 计算飞书签名（timestamp\nsecret 的 HMAC-SHA256，base64 编码）。
func feishuSign(secret, ts string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// feishuText 组装告警文案（纯文本，飞书 text 消息）。
func feishuText(ev *store.AlertEvent) string {
	sev := "warning"
	if ev.Severity == "critical" {
		sev = "critical"
	}
	obj := ev.AgentName
	if ev.EntityName != "" {
		obj = fmt.Sprintf("%s · 容器 %s", ev.AgentName, ev.EntityName)
	}
	unit := ""
	if isPctMetric(ev.Metric) {
		unit = "%"
	}
	state := "触发"
	if ev.State == "resolved" {
		state = "恢复"
	}
	return fmt.Sprintf("litesentry 告警 [%s %s]\n规则：%s\n对象：%s\n当前值：%.2f%s（阈值：%.2f%s）\n时间：%s",
		state, sev, ev.RuleName, obj,
		ev.Value, unit, ev.Threshold, unit,
		ev.StartedAt.Format("2006-01-02 15:04:05"))
}
