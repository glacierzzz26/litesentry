//! 阶段二 S3：frp 进程管理器（frps / frpc）。
//!
//! Agent 侧只做「执行」与「观测」，模板逻辑在 Server（`server/internal/frp/render.go`）：
//! - DesiredState.frp_toml 写入 `${LS_STATE_DIR}/frp.toml`（0600，含 auth token）
//! - 按 toml 首行 `# litesentry-kind: frpc|frps` 选二进制（`${LS_FRP_DIR}/{frpc,frps}`）
//! - 仅当 toml 内容变化才重启进程；进程意外退出自动重拉
//! - 解析 webServer 三键（单一事实来源 = 渲染 toml）→ Basic auth 轮询 admin API
//!   （`GET /api/status`）→ 隧道状态 → MetricsBatch.frp 上报
//!
//! 轻量约束（≤10MB 单二进制）：不引 reqwest，手写最小 HTTP/1.1 GET over TCP + 极简 base64。

use std::path::PathBuf;
use std::process::Stdio;
use std::time::{Duration, Instant};

use anyhow::{anyhow, Context, Result};
use tokio::io::AsyncWriteExt;
use tokio::net::TcpStream;
use tokio::process::{Child, Command};
use tracing::{info, warn};

use crate::pb::{DesiredState, FrpStatus, FrpTunnelStatus};

/// kind 标记注释前缀（与 server/internal/frp/render.go 对齐）。
const KIND_MARKER: &str = "# litesentry-kind: ";

/// 自愈重拉最小间隔：进程被杀/崩溃后每心跳都可能触发重拉，
/// 若配置本身有毛病（如端口冲突），会被 10s 限流，避免热循环刷日志。
const RESPAWN_MIN_INTERVAL: Duration = Duration::from_secs(10);

/// frp 进程管理器。
///
/// `applied_toml` 兼作「是否启用」信号：None = 未启用/未应用（collect 返回 None，
/// 不占 proto 字段，符合 S3 E6）；Some(toml) = 已应用，collect 返回进程 + 隧道状态。
pub struct FrpManager {
    bin_dir: PathBuf,
    toml_path: PathBuf,
    applied_toml: Option<String>,
    child: Option<Child>,
    last_respawn: Option<Instant>,
}

impl FrpManager {
    pub fn new(bin_dir: &str, state_dir: &str) -> Self {
        Self {
            bin_dir: PathBuf::from(bin_dir),
            toml_path: PathBuf::from(state_dir).join("frp.toml"),
            applied_toml: None,
            child: None,
            last_respawn: None,
        }
    }

    /// 应用 DesiredState 的 frp 字段（main.rs 的 state_version 门内调用，幂等）。
    ///
    /// - 未启用或 toml 为空 → 停进程、清状态（返回 Ok，collect 变为 None）
    /// - toml 变化 → 停旧进程 → 落盘(0600) → 拉起 → 记录 applied_toml
    /// - toml 未变但进程已死 → 重拉（不重写文件，避免无谓 inode 更新）
    ///
    /// 单点失败（如二进制缺失）返回 Err 且不推进 applied_version，下次心跳重试；
    /// 此时 enabled 状态已记录，collect() 会以 running:false + error 上报（可排查）。
    pub async fn apply(&mut self, ds: &DesiredState) -> Result<()> {
        let enabled = ds.frp_enabled;
        let toml = ds.frp_toml.clone();

        if !enabled || toml.trim().is_empty() {
            self.applied_toml = None;
            self.stop().await;
            return Ok(());
        }

        let changed = self.applied_toml.as_deref() != Some(toml.as_str());
        if changed {
            self.stop().await;
            self.write_toml(&toml)?;
            self.applied_toml = Some(toml.clone());
            self.spawn(&toml).await.map_err(|e| {
                warn!("frp 启动失败（collect 将上报原因）: {e}");
                e
            })?;
        } else if !self.is_alive() {
            // toml 未变但进程已退出：重拉（磁盘上配置还在）
            self.spawn(&toml).await.map_err(|e| {
                warn!("frp 重启失败: {e}");
                e
            })?;
        }
        Ok(())
    }

    /// 收集上报状态。未启用/未应用 → None；启用 → 进程存活 + 隧道状态。
    ///
    /// 自愈：期望运行但进程已退出（外部 kill / 意外崩溃）→ 重拉。
    /// 限流 RESPAWN_MIN_INTERVAL，避免配置有毛病时崩溃热循环刷日志。
    pub async fn collect(&mut self) -> Option<FrpStatus> {
        let toml = self.applied_toml.clone()?;

        if !self.is_alive()
            && self
                .last_respawn
                .map(|t| t.elapsed() >= RESPAWN_MIN_INTERVAL)
                .unwrap_or(true)
        {
            self.last_respawn = Some(Instant::now());
            if let Err(e) = self.spawn(&toml).await {
                warn!("frp 自愈重拉失败（本次心跳继续上报原因）: {e}");
            }
        }

        if !self.is_alive() {
            let kind = parse_kind(&toml).unwrap_or_else(|| "frpc".to_string());
            let bin = self.bin_dir.join(&kind);
            let error = if !bin.exists() {
                format!("{kind} 未安装: {}", bin.display())
            } else {
                format!("{kind} 进程已退出或未拉起")
            };
            return Some(FrpStatus {
                running: false,
                frp_version: String::new(),
                error,
                tunnels: Vec::new(),
            });
        }

        let kind = parse_kind(&toml).unwrap_or_else(|| "frpc".to_string());
        match admin_status(&kind, &toml).await {
            Ok((version, tunnels)) => Some(FrpStatus {
                running: true,
                frp_version: version,
                error: String::new(),
                tunnels,
            }),
            Err(e) => Some(FrpStatus {
                running: true, // 进程在，admin 查询失败（服务刚启动 / 端口冲突等）
                frp_version: String::new(),
                error: format!("admin API 查询失败: {e}"),
                tunnels: Vec::new(),
            }),
        }
    }

    /// 写 frp.toml（0600，含 auth token）。目录不存在则创建。
    fn write_toml(&self, toml: &str) -> Result<()> {
        if let Some(parent) = self.toml_path.parent() {
            std::fs::create_dir_all(parent)?;
        }
        std::fs::write(&self.toml_path, toml)?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(&self.toml_path, std::fs::Permissions::from_mode(0o600))?;
        }
        Ok(())
    }

    /// 拉起 frp 进程：`<bin_dir>/<kind> -c <frp.toml>`，stderr 打点。
    async fn spawn(&mut self, toml: &str) -> Result<()> {
        let kind = parse_kind(toml).unwrap_or_else(|| "frpc".to_string());
        let bin = self.bin_dir.join(&kind);
        if !bin.exists() {
            return Err(anyhow!("{kind} 未安装: {}", bin.display()));
        }
        let mut child = Command::new(&bin)
            .args(["-c", self.toml_path.to_str().unwrap_or("frp.toml")])
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::piped())
            .spawn()
            .with_context(|| format!("spawn {kind}"))?;

        // stderr 打点（frp 的启动/连接错误都走 stderr）
        let stderr = child.stderr.take().ok_or_else(|| anyhow!("no stderr"))?;
        let tag = kind.clone();
        tokio::spawn(async move {
            use tokio::io::AsyncBufReadExt;
            let mut reader = tokio::io::BufReader::new(stderr);
            let mut line = String::new();
            loop {
                line.clear();
                match reader.read_line(&mut line).await {
                    Ok(0) | Err(_) => break, // EOF → 进程退出
                    Ok(_) => {
                        let l = line.trim();
                        if !l.is_empty() {
                            warn!("frp[{tag}] stderr: {l}");
                        }
                    }
                }
            }
        });

        self.child = Some(child);
        info!("frp {kind} 已拉起（-c {}）", self.toml_path.display());
        Ok(())
    }

    /// 停止 frp 进程：强杀 + 回收。
    async fn stop(&mut self) {
        if let Some(mut child) = self.child.take() {
            let _ = child.kill().await;
            let _ = child.wait().await;
            info!("frp 进程已停止");
        }
    }

    /// 进程是否存活（try_wait 探测；None = 仍在运行）。
    fn is_alive(&mut self) -> bool {
        match &mut self.child {
            Some(c) => matches!(c.try_wait(), Ok(None)),
            None => false,
        }
    }
}

/// 从渲染 toml 首行读取 kind 标记注释（Server 渲染器确定性产出）。
fn parse_kind(toml: &str) -> Option<String> {
    toml.lines().next().and_then(|l| {
        l.trim()
            .strip_prefix(KIND_MARKER)
            .map(|s| s.trim().to_string())
    })
}

/// webServer 三键（从渲染 toml 解析，单一事实来源 = Server 渲染器）。
#[derive(Debug)]
struct AdminConf {
    port: u16,
    user: String,
    password: String,
}

fn parse_admin_conf(toml: &str) -> AdminConf {
    let mut c = AdminConf {
        port: 7400,
        user: "admin".into(),
        password: String::new(),
    };
    for line in toml.lines() {
        let l = line.trim();
        if let Some(v) = l.strip_prefix("webServer.port =") {
            c.port = v.trim().parse().unwrap_or(c.port);
        } else if let Some(v) = l.strip_prefix("webServer.user =") {
            c.user = strip_toml_str(v);
        } else if let Some(v) = l.strip_prefix("webServer.password =") {
            c.password = strip_toml_str(v);
        }
    }
    c
}

/// 解析 TOML 基本字符串字面量（只处理渲染器可能产出的转义：\" \\ \n \r \t \uXXXX）。
fn strip_toml_str(s: &str) -> String {
    let s = s.trim();
    if s.len() < 2 || !s.starts_with('"') || !s.ends_with('"') {
        return s.to_string();
    }
    let inner = &s[1..s.len() - 1];
    let mut out = String::with_capacity(inner.len());
    let mut chars = inner.chars();
    while let Some(ch) = chars.next() {
        if ch != '\\' {
            out.push(ch);
            continue;
        }
        match chars.next() {
            Some('"') => out.push('"'),
            Some('\\') => out.push('\\'),
            Some('n') => out.push('\n'),
            Some('r') => out.push('\r'),
            Some('t') => out.push('\t'),
            Some('u') => {
                let mut hex = String::with_capacity(4);
                for _ in 0..4 {
                    if let Some(c) = chars.next() {
                        hex.push(c);
                    }
                }
                if let Ok(code) = u32::from_str_radix(&hex, 16) {
                    if let Some(chr) = char::from_u32(code) {
                        out.push(chr);
                    }
                }
            }
            _ => {}
        }
    }
    out
}

/// 轮询 frp admin API（Basic auth），按 kind 选端点：
///   - frpc → GET /api/status：返回各 proxy 隧道状态（本进程管理的隧道明细）
///   - frps → GET /api/serverinfo：只有服务端聚合信息 + 版本；frps 无批量隧道端点
///     （隧道明细由各 frpc 客户端侧上报，frps 卡片只展示进程健康）
/// 返回 (frp 版本, 隧道状态列表)。手写最小 HTTP/1.1，不引重量级客户端。
async fn admin_status(kind: &str, toml: &str) -> Result<(String, Vec<FrpTunnelStatus>)> {
    let conf = parse_admin_conf(toml);
    let addr = format!("127.0.0.1:{}", conf.port);
    let path = if kind == "frps" { "/api/serverinfo" } else { "/api/status" };
    let mut stream = TcpStream::connect(&addr)
        .await
        .with_context(|| format!("连接 frp admin {addr}"))?;

    let auth = base64(format!("{}:{}", conf.user, conf.password).as_bytes());
    let req = format!(
        "GET {path} HTTP/1.1\r\nHost: {addr}\r\nAuthorization: Basic {auth}\r\nConnection: close\r\n\r\n"
    );
    stream.write_all(req.as_bytes()).await?;
    let _ = stream.shutdown().await; // 半关写，等待响应

    let mut buf = Vec::new();
    use tokio::io::AsyncReadExt;
    stream.read_to_end(&mut buf).await?;
    let text = String::from_utf8_lossy(&buf);
    if !text.starts_with("HTTP/1.1 200") && !text.starts_with("HTTP/1.0 200") {
        return Err(anyhow!("admin 返回非 200: {}", first_line(&text)));
    }
    let body = text
        .split("\r\n\r\n")
        .nth(1)
        .ok_or_else(|| anyhow!("admin 响应无 body"))?;
    if kind == "frps" {
        parse_serverinfo(body)
    } else {
        parse_admin_json(body)
    }
}

/// 解析 frps admin /api/serverinfo：取版本号。无批量隧道字段 → tunnels 恒空。
fn parse_serverinfo(body: &str) -> Result<(String, Vec<FrpTunnelStatus>)> {
    let v: serde_json::Value =
        serde_json::from_str(body).with_context(|| "serverinfo JSON 解析失败")?;
    let version = v.get("version").and_then(|x| x.as_str()).unwrap_or("").to_string();
    Ok((version, Vec::new()))
}

fn first_line(s: &str) -> &str {
    s.lines().next().unwrap_or("")
}

/// 解析 admin /api/status JSON（兼容 `{data:{...}}` 与裸 `{tcp:[...]}` 两种形态）。
fn parse_admin_json(body: &str) -> Result<(String, Vec<FrpTunnelStatus>)> {
    let v: serde_json::Value =
        serde_json::from_str(body).with_context(|| "admin JSON 解析失败")?;
    let mut obj = &v;
    if let Some(d) = v.get("data") {
        if d.is_object() {
            obj = d;
        }
    }

    let mut version = String::new();
    if let Some(s) = obj.get("server").and_then(|s| s.get("version")).and_then(|x| x.as_str()) {
        version = s.to_string();
    }

    let mut tunnels = Vec::new();
    for key in ["tcp", "udp", "http", "https", "stcp", "xtcp", "tcpmux"] {
        let Some(arr) = obj.get(key).and_then(|x| x.as_array()) else {
            continue;
        };
        for t in arr {
            let name = t.get("name").and_then(|x| x.as_str()).unwrap_or("").to_string();
            let typ = t.get("type").and_then(|x| x.as_str()).unwrap_or(key).to_string();
            let status = t.get("status").and_then(|x| x.as_str()).unwrap_or("").to_string();
            let err = t.get("err").and_then(|x| x.as_str()).unwrap_or("").to_string();
            // 优先 total（累计），缺省回落 today（当日）
            let rx = traffic(t, "total_traffic_in")
                .or_else(|| traffic(t, "today_traffic_in"))
                .unwrap_or(0);
            let tx = traffic(t, "total_traffic_out")
                .or_else(|| traffic(t, "today_traffic_out"))
                .unwrap_or(0);
            tunnels.push(FrpTunnelStatus {
                name,
                r#type: typ,
                status,
                err,
                rx_bytes: rx,
                tx_bytes: tx,
            });
        }
    }
    Ok((version, tunnels))
}

fn traffic(t: &serde_json::Value, key: &str) -> Option<u64> {
    t.get(key).and_then(|v| v.as_u64().or_else(|| v.as_i64().map(|x| x.max(0) as u64)))
}

/// 极简 base64 编码（RFC 4648，无填充外实现；用于 Basic auth）。
fn base64(data: &[u8]) -> String {
    const TABLE: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::with_capacity(data.len().div_ceil(3) * 4);
    for chunk in data.chunks(3) {
        let n = ((chunk[0] as u32) << 16)
            | ((*chunk.get(1).unwrap_or(&0) as u32) << 8)
            | (*chunk.get(2).unwrap_or(&0) as u32);
        out.push(TABLE[(n >> 18) as usize & 63] as char);
        out.push(TABLE[(n >> 12) as usize & 63] as char);
        out.push(if chunk.len() > 1 { TABLE[(n >> 6) as usize & 63] as char } else { '=' });
        out.push(if chunk.len() > 2 { TABLE[n as usize & 63] as char } else { '=' });
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    fn frp_toml(kind: &str, port: u16, pass: &str) -> String {
        format!(
            "# litesentry-kind: {kind}\nserverPort = 7000\n\nauth.token = \"tok\"\n\n\
             webServer.addr = \"127.0.0.1\"\nwebServer.port = {port}\n\
             webServer.user = \"admin\"\nwebServer.password = \"{pass}\"\n\n\
             [[proxies]]\nname = \"ssh\"\ntype = \"tcp\"\nlocalIP = \"127.0.0.1\"\nlocalPort = 22\nremotePort = 6000\n"
        )
    }

    #[test]
    fn parse_kind_reads_marker() {
        assert_eq!(parse_kind(&frp_toml("frpc", 7400, "p")), Some("frpc".into()));
        assert_eq!(parse_kind(&frp_toml("frps", 7500, "p")), Some("frps".into()));
        assert_eq!(parse_kind("no marker"), None);
    }

    #[test]
    fn parse_admin_conf_reads_web_server() {
        let c = parse_admin_conf(&frp_toml("frpc", 7411, "s3cr3t"));
        assert_eq!(c.port, 7411);
        assert_eq!(c.user, "admin");
        assert_eq!(c.password, "s3cr3t");
    }

    #[test]
    fn strip_toml_str_unescapes() {
        assert_eq!(strip_toml_str(r#""a\"b\\c\n""#), "a\"b\\c\n");
        assert_eq!(strip_toml_str("plain"), "plain");
    }

    #[test]
    fn base64_basic() {
        assert_eq!(base64(b"admin:pw"), "YWRtaW46cHc=");
        assert_eq!(base64(b"a"), "YQ==");
        assert_eq!(base64(b"ab"), "YWI=");
        assert_eq!(base64(b"abc"), "YWJj");
    }

    #[test]
    fn parse_admin_json_both_shapes() {
        // 裸形态（新版 frp admin）
        let bare = r#"{"tcp":[{"name":"ssh","type":"tcp","status":"online","err":"",
            "today_traffic_in":100,"today_traffic_out":200}],"server":{"version":"0.61.1"}}"#;
        let (ver, tun) = parse_admin_json(bare).unwrap();
        assert_eq!(ver, "0.61.1");
        assert_eq!(tun.len(), 1);
        assert_eq!(tun[0].name, "ssh");
        assert_eq!(tun[0].status, "online");
        assert_eq!(tun[0].rx_bytes, 100);
        assert_eq!(tun[0].tx_bytes, 200);

        // {data:{...}} 包裹（旧版）
        let wrapped = r#"{"data":{"udp":[{"name":"dns","type":"udp","status":"online","err":"",
            "total_traffic_in":10,"total_traffic_out":20}]}}"#;
        let (_, tun) = parse_admin_json(wrapped).unwrap();
        assert_eq!(tun.len(), 1);
        assert_eq!(tun[0].r#type, "udp");
        assert_eq!(tun[0].rx_bytes, 10);
        assert_eq!(tun[0].tx_bytes, 20);

        // 非法 JSON → Err
        assert!(parse_admin_json("not json").is_err());
    }

    #[test]
    fn parse_serverinfo_extracts_version() {
        let body = r#"{"version":"0.71.0","bindPort":7000,"proxyTypeCount":{"tcp":1},"clientCounts":1}"#;
        let (ver, tun) = parse_serverinfo(body).unwrap();
        assert_eq!(ver, "0.71.0");
        assert!(tun.is_empty(), "frps 无批量隧道端点，tunnels 应恒空");
        assert!(parse_serverinfo("nope").is_err());
    }
}
