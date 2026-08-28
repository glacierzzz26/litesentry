// Package frp 将 Server 侧存储的 frp 配置（store.FrpConfig）渲染为 frpc.toml / frps.toml，
// 文本随 DesiredState 下发，Agent 侧写入本机并拉起进程。
//
// proto 契约冻结（S1 预留 frp_toml / frp_enabled 字段），kind 由 toml 首行标记注释携带：
//
//	# litesentry-kind: frpc|frps
//
// Agent 据此选二进制名（frpc / frps），并解析 webServer.port/user/password 做 admin API 轮询。
// admin 凭据与 loopback 端口是单一事实来源（= 本渲染器），Agent 不做模板逻辑。
package frp

import (
	"fmt"
	"strings"

	"litesentry/server/internal/store"
)

// 渲染常量：单台机最多一个 frpc + 一个 frps，loopback 固定端口无冲突。
const (
	adminAddr     = "127.0.0.1"
	adminUser     = "admin"
	frpcAdminPort = 7400
	frpsAdminPort = 7500
)

// Render 将 frp 配置渲染为 toml 文本，返回 kind 与完整渲染结果（纯函数，可单测）。
//   - frpc：serverAddr / serverPort / auth.token + webServer + 每条 proxy → [[proxies]]
//   - frps：bindPort（= server_port）/ auth.token + webServer（server_addr / proxies 不用）
//
// token 同时作为 frp 认证令牌与 webServer 密码，经 tomlStr 转义后写入。
func Render(c *store.FrpConfig) (kind, toml string) {
	kind = c.Kind
	var b strings.Builder
	fmt.Fprintf(&b, "# litesentry-kind: %s\n", kind)

	if kind == "frpc" {
		fmt.Fprintf(&b, "serverAddr = %s\n", tomlStr(c.ServerAddr))
		fmt.Fprintf(&b, "serverPort = %d\n", c.ServerPort)
	} else {
		fmt.Fprintf(&b, "bindPort = %d\n", c.ServerPort)
	}
	fmt.Fprintf(&b, "\nauth.token = %s\n", tomlStr(c.Token))

	port := frpcAdminPort
	if kind == "frps" {
		port = frpsAdminPort
	}
	fmt.Fprintf(&b, "\nwebServer.addr = %s\n", tomlStr(adminAddr))
	fmt.Fprintf(&b, "webServer.port = %d\n", port)
	fmt.Fprintf(&b, "webServer.user = %s\n", tomlStr(adminUser))
	fmt.Fprintf(&b, "webServer.password = %s\n", tomlStr(c.Token))

	for _, p := range c.Proxies {
		b.WriteString("\n[[proxies]]\n")
		fmt.Fprintf(&b, "name = %s\n", tomlStr(p.Name))
		fmt.Fprintf(&b, "type = %s\n", tomlStr(p.Type))
		if p.LocalIP != "" {
			fmt.Fprintf(&b, "localIP = %s\n", tomlStr(p.LocalIP))
		}
		if p.LocalPort > 0 {
			fmt.Fprintf(&b, "localPort = %d\n", p.LocalPort)
		}
		if p.RemotePort > 0 {
			fmt.Fprintf(&b, "remotePort = %d\n", p.RemotePort)
		}
	}
	return kind, b.String()
}

// tomlStr 将字符串渲染为 TOML 基本字符串字面量：转义反斜杠 / 双引号 / 控制字符。
// frp token 通常为字母数字，但转义保证任意用户输入（含引号/换行）也能安全进入配置。
func tomlStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
