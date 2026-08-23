//! 主机指标采集（sysinfo）。网络速率用前后两采样差分。
//! 阶段一内置于 Agent；阶段二将降级为 Server 下发的内置插件（core/collector 已分层）。

use std::time::Instant;

use sysinfo::{Disks, Networks, System};

use crate::ip::{self, AddrInfo};

#[derive(Debug, Default, Clone)]
pub struct DiskSample {
    pub mount: String,
    pub fs: String,
    pub total: u64,
    pub used: u64,
}

#[derive(Debug, Default, Clone)]
pub struct HostSample {
    pub hostname: String,
    pub os: String,
    pub arch: String,
    pub kernel: String,
    pub uptime_s: u64,
    pub load_1m: f64,
    pub load_5m: f64,
    pub cpu_pct: f64,
    pub mem_total: u64,
    pub mem_used: u64,
    pub swap_total: u64,
    pub swap_used: u64,
    pub net_rx_bps: u64,
    pub net_tx_bps: u64,
    pub agent_cpu_pct: f64,
    pub agent_mem_rss: u64,
    pub disks: Vec<DiskSample>,
    pub ips: Vec<AddrInfo>,
}

/// 网络速率采样器（差分 → bps）
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

// ============ Agent 自身进程占用（/proc/self，仅 Linux） ============

#[cfg(target_os = "linux")]
fn self_cpu_ticks() -> Option<u64> {
    let s = std::fs::read_to_string("/proc/self/stat").ok()?;
    // 第 2 字段 comm 可能含空格/括号，从最后一个 ')' 后开始按空白拆分
    let after = s.rfind(')')? + 1;
    let rest: Vec<&str> = s[after..].split_whitespace().collect();
    // rest[0]=state(字段3) … rest[11]=utime(14) rest[12]=stime(15)
    let utime: u64 = rest.get(11)?.parse().ok()?;
    let stime: u64 = rest.get(12)?.parse().ok()?;
    Some(utime + stime)
}

#[cfg(not(target_os = "linux"))]
fn self_cpu_ticks() -> Option<u64> {
    None
}

#[cfg(target_os = "linux")]
fn self_rss_bytes() -> u64 {
    // /proc/self/statm 第 2 个字段 = resident 页数
    let Ok(s) = std::fs::read_to_string("/proc/self/statm") else { return 0 };
    let Some(pages) = s.split_whitespace().nth(1) else { return 0 };
    let Ok(pages) = pages.parse::<u64>() else { return 0 };
    let page = unsafe { libc::sysconf(libc::_SC_PAGESIZE) };
    let page = if page > 0 { page as u64 } else { 4096 };
    pages.saturating_mul(page)
}

#[cfg(not(target_os = "linux"))]
fn self_rss_bytes() -> u64 {
    0
}

/// 自身进程 CPU 采样器：前后两采样 utime+stime 差分 → 周期 CPU%（进程口径，可 >100）。
struct SelfCpu {
    last_ticks: u64,
    last_ts: Option<Instant>,
}

impl SelfCpu {
    fn new() -> Self {
        Self { last_ticks: 0, last_ts: None }
    }
    fn sample(&mut self) -> f64 {
        let Some(ticks) = self_cpu_ticks() else { return 0.0 };
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

/// 主机采集器：持有 System 以便跨采样保持 CPU/网络增量。
pub struct Collector {
    sys: System,
    nets: Networks,
    disks: Disks,
    net_sampler: NetSampler,
    self_cpu: SelfCpu,
}

impl Collector {
    pub fn new() -> Self {
        let mut sys = System::new_all();
        // CPU 需要至少两次 refresh 才有差值，首轮 global_cpu_usage 视为 0 由调用方处理
        sys.refresh_cpu_usage();
        Collector {
            sys,
            nets: Networks::new(),
            disks: Disks::new_with_refreshed_list(),
            net_sampler: NetSampler::new(),
            self_cpu: SelfCpu::new(),
        }
    }

    /// 采集一次主机指标。
    pub fn collect(&mut self) -> HostSample {
        self.sys.refresh_cpu_usage();
        self.sys.refresh_memory();
        self.nets.refresh(false);
        self.disks.refresh(false);

        let now = Instant::now();
        let (rx_bps, tx_bps) = {
            let mut rx = 0u64;
            let mut tx = 0u64;
            for (_name, d) in self.nets.iter() {
                rx = rx.saturating_add(d.received());
                tx = tx.saturating_add(d.transmitted());
            }
            self.net_sampler.sample(rx, tx, now)
        };

        // sysinfo ≥0.30：静态信息类 API 为关联函数
        let load = System::load_average();

        HostSample {
            hostname: System::host_name().unwrap_or_default(),
            os: System::long_os_version().unwrap_or_else(|| std::env::consts::OS.to_string()),
            arch: std::env::consts::ARCH.to_string(),
            kernel: System::kernel_version().unwrap_or_default(),
            uptime_s: System::uptime(),
            load_1m: load.one,
            load_5m: load.five,
            cpu_pct: self.sys.global_cpu_usage() as f64,
            mem_total: self.sys.total_memory(),
            mem_used: self.sys.used_memory(),
            swap_total: self.sys.total_swap(),
            swap_used: self.sys.used_swap(),
            net_rx_bps: rx_bps,
            net_tx_bps: tx_bps,
            agent_cpu_pct: self.self_cpu.sample(),
            agent_mem_rss: self_rss_bytes(),
            disks: self
                .disks
                .iter()
                .map(|d| DiskSample {
                    mount: d.mount_point().to_string_lossy().to_string(),
                    fs: d.file_system().to_string_lossy().to_string(),
                    total: d.total_space(),
                    used: d.total_space().saturating_sub(d.available_space()),
                })
                .collect(),
            ips: ip::collect_addrs(),
        }
    }
}
