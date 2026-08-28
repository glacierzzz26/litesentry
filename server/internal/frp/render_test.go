package frp

import (
	"strings"
	"testing"

	"litesentry/server/internal/store"
)

// TestRenderFrpc 验证 frpc 渲染：首行 kind 标记、连接/认证/webServer 关键行、每条 proxy。
func TestRenderFrpc(t *testing.T) {
	c := &store.FrpConfig{
		Kind:       "frpc",
		AgentID:    "a-1",
		ServerAddr: "192.168.1.100",
		ServerPort: 7000,
		Token:      "t0k-en",
		Proxies: []store.FrpTunnel{
			{Name: "ssh", Type: "tcp", LocalIP: "127.0.0.1", LocalPort: 22, RemotePort: 6000},
			{Name: "web", Type: "tcp", LocalIP: "127.0.0.1", LocalPort: 8080, RemotePort: 8081},
		},
	}
	kind, toml := Render(c)
	if kind != "frpc" {
		t.Fatalf("kind 应为 frpc，实得 %s", kind)
	}
	if !strings.HasPrefix(toml, "# litesentry-kind: frpc\n") {
		t.Fatalf("首行应携带 kind 标记注释，实得:\n%s", toml)
	}
	for _, want := range []string{
		`serverAddr = "192.168.1.100"`,
		`serverPort = 7000`,
		`auth.token = "t0k-en"`,
		`webServer.addr = "127.0.0.1"`,
		`webServer.port = 7400`,
		`webServer.user = "admin"`,
		`webServer.password = "t0k-en"`,
		`name = "ssh"`,
		`localIP = "127.0.0.1"`,
		`localPort = 22`,
		`remotePort = 6000`,
		`name = "web"`,
		`localPort = 8080`,
		`remotePort = 8081`,
	} {
		if !strings.Contains(toml, want) {
			t.Errorf("frpc 渲染缺行 %q，完整:\n%s", want, toml)
		}
	}
}

// TestRenderFrps 验证 frps 渲染：bindPort 取 server_port、webServer 端口 7500、不出现 serverAddr/proxies。
func TestRenderFrps(t *testing.T) {
	c := &store.FrpConfig{Kind: "frps", AgentID: "", ServerPort: 7001, Token: "s-tok"}
	kind, toml := Render(c)
	if kind != "frps" {
		t.Fatalf("kind 应为 frps，实得 %s", kind)
	}
	if !strings.HasPrefix(toml, "# litesentry-kind: frps\n") {
		t.Fatalf("首行应携带 kind 标记注释，实得:\n%s", toml)
	}
	for _, want := range []string{
		`bindPort = 7001`,
		`auth.token = "s-tok"`,
		`webServer.port = 7500`,
		`webServer.password = "s-tok"`,
	} {
		if !strings.Contains(toml, want) {
			t.Errorf("frps 渲染缺行 %q，完整:\n%s", want, toml)
		}
	}
	for _, forbid := range []string{"serverAddr", "serverPort", "proxies"} {
		if strings.Contains(toml, forbid) {
			t.Errorf("frps 不应出现 %q，完整:\n%s", forbid, toml)
		}
	}
}

// TestRenderTokenEscape 验证 token 中的 TOML 特殊字符被转义（反斜杠 / 双引号 / 换行）。
func TestRenderTokenEscape(t *testing.T) {
	c := &store.FrpConfig{Kind: "frps", Token: "a\"b\\c\nline"}
	_, toml := Render(c)
	if !strings.Contains(toml, `auth.token = "a\"b\\c\nline"`) {
		t.Errorf("token 转义不符，完整:\n%s", toml)
	}
	if !strings.Contains(toml, `webServer.password = "a\"b\\c\nline"`) {
		t.Errorf("webServer.password 转义不符，完整:\n%s", toml)
	}
}

// TestRenderEmptyProxies 验证 frps / 空 proxy frpc 渲染不产生 [[proxies]] 段。
func TestRenderEmptyProxies(t *testing.T) {
	for _, kind := range []string{"frpc", "frps"} {
		_, toml := Render(&store.FrpConfig{Kind: kind, ServerPort: 7000, Token: "x"})
		if strings.Contains(toml, "[[proxies]]") {
			t.Errorf("%s 无 proxy 时不应渲染 [[proxies]]，完整:\n%s", kind, toml)
		}
	}
}
