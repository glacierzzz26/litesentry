//! host 内置插件：主机指标采集（sysinfo + get_if_addrs），阶段二 S2 由 Agent 内迁出。
//!
//! 协议（见 agent/src/plugin.rs）：
//! - stdin 收一行启动命令 `{"cmd":"start","args":{...},"interval":N}`
//! - stdout 每行输出 `{"ts":...,"series":[...]}`，按 interval 周期产出
//!
//! 产出（与 Server translate.go 的契约，改动需同步两端）：
//! - `host.info`（每 tick 1 条）：tags {hostname,os,arch,kernel}，fields 全量主机资源指标
//! - `host.ip`（每地址 1 条）：tags {addr,family,iface,scope}，fields 空
//!
//! agent_cpu/agent_mem 语义：插件由 Agent 直接 spawn，读 `/proc/<getppid()>/stat`（父进程 = Agent），
//! 口径与阶段一「Agent 自身进程占用」一致。

use std::io::{BufRead, Write};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use sysinfo::{Networks, System};

fn now_ts() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

// ============ 网络速率采样（前后两采样差分 → bps） ============

struct NetSampler {
    last_rx: u64,
    last_tx: u64,
    last_ts: Option<Instant>,
}

impl NetSampler {
    fn new() -> Self {
        Self { last_rx: 0, last_tx: 0, last_ts: None }
    }
    fn sample(&mut self, rx: u64, tx: u64, now: Instant) -> (u64, u64) {
        let Some(last_ts) = self.last_ts else {
            self.last_rx = rx;
            self.last_tx = tx;
            self.last_ts = Some(now);
            return (0, 0);
        };
        let dt = now.duration_since(last_ts).as_secs_f64();
        let rx_bps = if dt > 0.0 { (rx.saturating_sub(self.last_rx) as f64 / dt) as u64 } else { 0 };
        let tx_bps = if dt > 0.0 { (tx.saturating_sub(self.last_tx) as f64 / dt) as u64 } else { 0 };
        self.last_rx = rx;
        self.last_tx = tx;
        self.last_ts = Some(now);
        (rx_bps, tx_bps)
    }
}

// ============ Agent 自身进程占用（读 /proc/<父进程>/…，Linux） ============

fn proc_path(name: &str) -> String {
    format!("/proc/{}/{}", unsafe { libc::getppid() }, name)
}

#[cfg(target_os = "linux")]
fn agent_cpu_ticks() -> Option<u64> {
    let s = std::fs::read_to_string(proc_path("stat")).ok()?;
    // 第 2 字段 comm 可能含空格/括号，从最后一个 ')' 后开始按空白拆分
    let after = s.rfind(')')? + 1;
    let rest: Vec<&str> = s[after..].split_whitespace().collect();
    // rest[0]=state(字段3) … rest[11]=utime(14) rest[12]=stime(15)
    let utime: u64 = rest.get(11)?.parse().ok()?;
    let stime: u64 = rest.get(12)?.parse().ok()?;
    Some(utime + stime)
}

#[cfg(not(target_os = "linux"))]
fn agent_cpu_ticks() -> Option<u64> {
    None
}

#[cfg(target_os = "linux")]
fn agent_rss_bytes() -> u64 {
    // /proc/<pid>/statm 第 2 个字段 = resident 页数
    let Ok(s) = std::fs::read_to_string(proc_path("statm")) else { return 0 };
    let Some(pages) = s.split_whitespace().nth(1) else { return 0 };
    let Ok(pages) = pages.parse::<u64>() else { return 0 };
    let page = unsafe { libc::sysconf(libc::_SC_PAGESIZE) };
    let page = if page > 0 { page as u64 } else { 4096 };
    pages.saturating_mul(page)
}

#[cfg(not(target_os = "linux"))]
fn agent_rss_bytes() -> u64 {
    0
}

// 父进程 CPU 采样器：前后两采样 utime+stime 差分 → 周期 CPU%（进程口径，可 >100）。
struct SelfCpu {
    last_ticks: u64,
    last_ts: Option<Instant>,
}

impl SelfCpu {
    fn new() -> Self {
        Self { last_ticks: 0, last_ts: None }
    }
    fn sample(&mut self) -> f64 {
        let Some(ticks) = agent_cpu_ticks() else { return 0.0 };
        let now = Instant::now();
        let Some(last_ts) = self.last_ts else {
            self.last_ticks = ticks;
            self.last_ts = Some(now);
            return 0.0; // 首轮无基线
        };
        let dt = now.duration_since(last_ts).as_secs_f64();
        let dcpu = ticks.saturating_sub(self.last_ticks) as f64;
        self.last_ticks = ticks;
        self.last_ts = Some(now);
        if dt <= 0.0 {
            return 0.0;
        }
        let tps = unsafe { libc::sysconf(libc::_SC_CLK_TCK) };
        let tps = if tps > 0 { tps as f64 } else { 100.0 };
        dcpu / tps / dt * 100.0
    }
}

// ============ IP 地址（沿用阶段一 ip.rs 口径） ============

/// 采集本机全部 IPv4 + 全局 IPv6 地址。
fn collect_addrs() -> Vec<serde_json::Value> {
    let Ok(interfaces) = get_if_addrs::get_if_addrs() else {
        return Vec::new();
    };
    let mut out = Vec::with_capacity(interfaces.len());
    for iface in interfaces {
        let ip = iface.addr.ip();
        let scope = classify_scope(ip);
        match ip {
            std::net::IpAddr::V4(_) => out.push(serde_json::json!({
                "addr": ip.to_string(), "family": "ipv4", "iface": iface.name, "scope": scope,
            })),
            std::net::IpAddr::V6(_) if scope == "global" => out.push(serde_json::json!({
                "addr": ip.to_string(), "family": "ipv6", "iface": iface.name, "scope": scope,
            })),
            _ => {} // IPv6 链路本地 / 回环不参与直连，跳过
        }
    }
    out
}

fn classify_scope(ip: std::net::IpAddr) -> String {
    if ip.is_loopback() {
        return "loopback".into();
    }
    match ip {
        std::net::IpAddr::V6(v6) => {
            // 链路本地 fe80::/10
            if v6.segments()[0] & 0xffc0 == 0xfe80 {
                "link".into()
            } else {
                "global".into()
            }
        }
        std::net::IpAddr::V4(_) => "global".into(),
    }
}

fn main() {
    // 启动命令：读一行；stdin 关闭（EOF）则直接退出
    let stdin = std::io::stdin();
    let mut line = String::new();
    if stdin.lock().read_line(&mut line).is_err() || line.trim().is_empty() {
        return;
    }
    let interval: u64 = serde_json::from_str::<serde_json::Value>(&line)
        .ok()
        .and_then(|v| v["interval"].as_u64())
        .unwrap_or(60)
        .max(1);

    let mut sys = System::new_all();
    // CPU 需要至少两次 refresh 才有差值，首轮 global_cpu_usage 视为 0
    sys.refresh_cpu_usage();
    let mut nets = Networks::new();
    let mut net_sampler = NetSampler::new();
    let mut self_cpu = SelfCpu::new();

    let stdout = std::io::stdout();
    loop {
        sys.refresh_cpu_usage();
        sys.refresh_memory();
        nets.refresh(false);

        let now = Instant::now();
        let (rx_bps, tx_bps) = {
            let mut rx = 0u64;
            let mut tx = 0u64;
            for (_name, d) in nets.iter() {
                rx = rx.saturating_add(d.received());
                tx = tx.saturating_add(d.transmitted());
            }
            net_sampler.sample(rx, tx, now)
        };

        let load = System::load_average();
        let hostname = System::host_name().unwrap_or_default();
        let os = System::long_os_version().unwrap_or_else(|| std::env::consts::OS.to_string());
        let arch = std::env::consts::ARCH.to_string();
        let kernel = System::kernel_version().unwrap_or_default();

        // 汇总地址 → host.ip series
        let ips = collect_addrs();
        let mut series: Vec<serde_json::Value> = Vec::with_capacity(ips.len() + 1);
        series.push(serde_json::json!({
            "name": "host.info",
            "tags": { "hostname": hostname, "os": os, "arch": arch, "kernel": kernel },
            "fields": {
                "uptime_s": System::uptime(),
                "load_1m": load.one,
                "load_5m": load.five,
                "cpu_pct": sys.global_cpu_usage() as f64,
                "mem_total": sys.total_memory(),
                "mem_used": sys.used_memory(),
                "swap_total": sys.total_swap(),
                "swap_used": sys.used_swap(),
                "net_rx_bps": rx_bps,
                "net_tx_bps": tx_bps,
                "agent_cpu_pct": self_cpu.sample(),
                "agent_mem_rss": agent_rss_bytes(),
            }
        }));
        for ip in ips {
            series.push(serde_json::json!({ "name": "host.ip", "tags": ip, "fields": {} }));
        }

        let out = serde_json::json!({ "ts": now_ts(), "series": series });
        let mut lock = stdout.lock();
        if writeln!(lock, "{out}").is_err() || lock.flush().is_err() {
            return; // stdout 关闭（agent 已退出）→ 结束
        }
        std::thread::sleep(Duration::from_secs(interval));
    }
}
