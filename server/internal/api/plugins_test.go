package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"litesentry/server/internal/store"
)

// newTestAPI 起一个内存 SQLite + 已签发 JWT 的 gin 引擎（含 auth 中间件）。
// 返回引擎、store 与可直接用的 Bearer token。
func newTestAPI(t *testing.T) (*gin.Engine, store.Store, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	st, err := store.NewSQLite("file:litesentry_api_test?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	secret := []byte("test-secret")
	s := New(st, secret, time.Hour)
	tok, _, err := signJWT(secret, time.Hour, &store.User{ID: "u1", Username: "tester"})
	if err != nil {
		t.Fatalf("signJWT: %v", err)
	}
	return s.Routes(), st, tok
}

// doJSON 发一个带 Bearer 的请求，返回状态码与响应体。
func doJSON(t *testing.T, r *gin.Engine, method, path, token, body string) (int, string) {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

// seedAgent 注册一个节点（走 store，模拟 agent 已上报）。
func seedAgent(t *testing.T, st store.Store, id, hostname string) {
	t.Helper()
	now := time.Now().UTC()
	if _, _, err := st.RegisterAgent(context.Background(), "machine-"+id, &store.Agent{
		Hostname: hostname, MachineID: "machine-" + id, LastSeen: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
}

// TestAssignPluginServerSentinel 覆盖「公网机哨兵」两种写法的一致性：
// 前端历史上发空串、FRP/任务页发 "server" —— 二者都必须解析到 settings.server_agent_id，
// 不能一个 400 一个 404（回归：修复前空串被 "agent_id/version 不能为空" 挡掉）。
func TestAssignPluginServerSentinel(t *testing.T) {
	r, st, tok := newTestAPI(t)
	ctx := context.Background()

	// 先注册插件版本（非内置 → 走 plugins 仓库校验路径）
	if err := st.SavePlugin(ctx, &store.Plugin{
		ID: "hello", Name: "hello", Kind: "binary", Version: "0.1.0",
		SHA256: strings.Repeat("a", 64), Size: 3, Data: []byte("abc"),
	}); err != nil {
		t.Fatalf("SavePlugin: %v", err)
	}

	body := func(agentID string) string {
		b, _ := json.Marshal(assignPluginBody{AgentID: agentID, Version: "0.1.0"})
		return string(b)
	}

	// 1) 未设置 server_agent_id：空串与 "server" 都应报「未设置公网机 Agent」（404），而非 400
	for _, ag := range []string{"", "server"} {
		code, resp := doJSON(t, r, http.MethodPost, "/api/plugins/hello/assign", tok, body(ag))
		if code != http.StatusNotFound {
			t.Errorf("agent_id=%q 未设公网机时 status=%d body=%s，期望 404", ag, code, resp)
		}
		if !strings.Contains(resp, "未设置公网机 Agent") {
			t.Errorf("agent_id=%q 未设公网机时 body=%s，期望提示未设置公网机", ag, resp)
		}
	}

	// 2) 设置公网机后，两种写法都应成功且落到同一个 agent
	serverAgentID, _, err := st.RegisterAgent(ctx, "machine-self", &store.Agent{
		Hostname: "server-host", MachineID: "machine-self",
		LastSeen: time.Now().UTC(), CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("RegisterAgent(self): %v", err)
	}
	if err := st.SetSetting(ctx, "server_agent_id", serverAgentID); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	for _, ag := range []string{"", "server"} {
		code, resp := doJSON(t, r, http.MethodPost, "/api/plugins/hello/assign", tok, body(ag))
		if code != http.StatusOK {
			t.Errorf("agent_id=%q 设了公网机时 status=%d body=%s，期望 200", ag, code, resp)
		}
	}
	rows, err := st.AgentPlugins(ctx, serverAgentID)
	if err != nil {
		t.Fatalf("AgentPlugins: %v", err)
	}
	if len(rows) != 1 || rows[0].PluginID != "hello" {
		t.Errorf("公网机指派 = %+v，期望 1 条 hello（两种哨兵写法归一到同一 agent）", rows)
	}

	// 3) 版本为空仍须 400
	code, _ := doJSON(t, r, http.MethodPost, "/api/plugins/hello/assign", tok,
		`{"agent_id":"server","version":""}`)
	if code != http.StatusBadRequest {
		t.Errorf("空 version status=%d，期望 400", code)
	}
}

// TestPutSettingsServerAgentID 覆盖设置项写入语义：
// 缺省字段 = 不修改；指向未注册节点 = 400；空串 = 清除声明。
func TestPutSettingsServerAgentID(t *testing.T) {
	r, st, tok := newTestAPI(t)
	ctx := context.Background()
	seedAgent(t, st, "agent-1", "node-1")
	agents, err := st.Agents(ctx)
	if err != nil || len(agents) == 0 {
		t.Fatalf("Agents: %v (%d)", err, len(agents))
	}
	// RegisterAgent 可能复用 machine 指纹，取实际落库的 id
	realID := agents[0].AgentID

	// 缺省字段：不修改（当前为空 → 仍为空）
	code, _ := doJSON(t, r, http.MethodPut, "/api/settings", tok, `{"feishu_webhook":""}`)
	if code != http.StatusOK {
		t.Fatalf("PUT settings status=%d，期望 200", code)
	}
	if v, _ := st.GetSetting(ctx, "server_agent_id"); v != "" {
		t.Errorf("缺省字段不应写入，得到 %q", v)
	}

	// 指向未注册 id → 400
	code, resp := doJSON(t, r, http.MethodPut, "/api/settings", tok,
		`{"feishu_webhook":"","server_agent_id":"nonexistent"}`)
	if code != http.StatusBadRequest {
		t.Errorf("未注册 agent_id status=%d body=%s，期望 400", code, resp)
	}

	// 指向已注册 id → 200 且落库
	code, resp = doJSON(t, r, http.MethodPut, "/api/settings", tok,
		`{"feishu_webhook":"","server_agent_id":"`+realID+`"}`)
	if code != http.StatusOK {
		t.Fatalf("已注册 agent_id status=%d body=%s，期望 200", code, resp)
	}
	if v, _ := st.GetSetting(ctx, "server_agent_id"); v != realID {
		t.Errorf("server_agent_id = %q，期望 %q", v, realID)
	}
	// GET 应回传（前端选择器回填依赖）
	code, resp = doJSON(t, r, http.MethodGet, "/api/settings", tok, "")
	if code != http.StatusOK || !strings.Contains(resp, realID) {
		t.Errorf("GET settings 未回传 server_agent_id: %d %s", code, resp)
	}

	// 空串 = 清除
	code, _ = doJSON(t, r, http.MethodPut, "/api/settings", tok,
		`{"feishu_webhook":"","server_agent_id":""}`)
	if code != http.StatusOK {
		t.Fatalf("清除 status=%d，期望 200", code)
	}
	if v, _ := st.GetSetting(ctx, "server_agent_id"); v != "" {
		t.Errorf("清除后 server_agent_id = %q，期望空", v)
	}
}
