//! litesentry-agent：轻量监控采集端（Rust）。
//!
//! 阶段一职责：定时采集主机 + 容器指标，经 gRPC 推送到 Server。
//! core（传输核）/ collector 分层：阶段二 collector 将降级为 Server 下发的内置插件，
//! core 保持不变。

mod client;
mod collector;
mod config;
mod docker;
mod ip;
mod plugin;

pub mod pb {
    tonic::include_proto!("litesentry");
}

use std::time::Duration;

use anyhow::Result;
use tokio::time;

use crate::client::RegisterInfo;
use crate::collector::HostSample;
use crate::config::{cache_agent_id, machine_id, Config, TlsConfig};
use crate::pb::{ContainerMetrics, HostMetrics as PbHost, MetricsBatch};

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "info".into()),
        )
        .init();

    let cfg = Config::from_env();
    if cfg.token.is_empty() {
        tracing::warn!("LS_TOKEN 未设置（Server 未配置 token 时才可联调）");
    }

    let mut collector = collector::Collector::new();
    let dcollector = docker::DockerCollector::new();

    // 首次连接失败直接退出，由 systemd/容器编排负责重启（Keep simple）
    let tls: TlsConfig = cfg.tls();
    let mut client = client::Client::connect(&cfg.server, &tls).await?;
    if tls.enabled() {
        tracing::info!("已连接 Server（mTLS）");
    } else {
        tracing::warn!("已连接 Server（明文，未配置 LS_TLS_*，仅限开发联调）");
    }

    // agent_id：LS_AGENT_ID 显式指定则直接用；否则用自身机器信息向 Server 注册
    // （Server 按机器指纹复用历史 id，重装/重启也不变）
    let agent_id = if cfg.agent_id.is_empty() {
        let meta = collector.collect(); // 顺带预热 CPU/网络差分基线
        let info = RegisterInfo {
            hostname: meta.hostname.clone(),
            machine_id: machine_id(),
            os: meta.os.clone(),
            arch: meta.arch.clone(),
            kernel: meta.kernel.clone(),
            version: env!("CARGO_PKG_VERSION").to_string(),
        };
        let id = client.register(&cfg.token, &info).await?;
        cache_agent_id(&cfg.id_file, &id);
        id
    } else {
        cfg.agent_id.clone()
    };
    tracing::info!("agent {agent_id} → {} (interval {}s, 心跳=上报)", cfg.server, cfg.interval_secs);

    let mut host = plugin::PluginHost::new(&cfg.plugin_dir, cfg.interval_secs);
    let mut applied_version: u64 = 0; // 已应用的 DesiredState 版本（心跳下发，等于即跳过）

    let mut ticker = time::interval(Duration::from_secs(cfg.interval_secs));
    loop {
        ticker.tick().await;

        let host_sample = collector.collect();
        let containers = match dcollector.collect().await {
            Ok(v) => v,
            Err(e) => {
                tracing::warn!("容器采集失败: {e}");
                Vec::new()
            }
        };

        // 汇入插件系列（阶段二）
        let mut batch = build_batch(&agent_id, &host_sample, &containers);
        batch.series = host.drain_series();

        match client.push(&cfg.token, batch).await {
            Ok(ack) => {
                if let Some(ds) = ack.desired_state {
                    if ds.state_version != applied_version {
                        match host.apply(&mut client, &cfg.token, &ds.plugins).await {
                            Ok(()) => {
                                applied_version = ds.state_version;
                                tracing::info!("已应用 DesiredState v{}（{} 个插件）", ds.state_version, ds.plugins.len());
                            }
                            Err(e) => tracing::warn!("应用 DesiredState 失败: {e}"),
                        }
                    }
                }
                tracing::debug!("pushed server_time={}", ack.server_time);
            }
            Err(e) => tracing::warn!("push 失败: {e}"),
        }
    }
}

fn build_batch(
    agent_id: &str,
    host: &HostSample,
    containers: &[docker::ContainerSample],
) -> MetricsBatch {
    let ips = host
        .ips
        .iter()
        .map(|a| pb::IpAddr {
            family: a.family.clone(),
            addr: a.addr.clone(),
            iface: a.iface.clone(),
            scope: a.scope.clone(),
        })
        .collect();

    let disks = host
        .disks
        .iter()
        .map(|d| pb::Disk {
            mount: d.mount.clone(),
            fs: d.fs.clone(),
            total: d.total,
            used: d.used,
        })
        .collect();

    let pb_host = PbHost {
        hostname: host.hostname.clone(),
        os: host.os.clone(),
        arch: host.arch.clone(),
        kernel: host.kernel.clone(),
        uptime_s: host.uptime_s,
        load_1m: host.load_1m as f32,
        load_5m: host.load_5m as f32,
        cpu_pct: host.cpu_pct as f32,
        agent_cpu_pct: host.agent_cpu_pct as f32,
        agent_mem_rss: host.agent_mem_rss,
        mem: Some(pb::Mem { total: host.mem_total, used: host.mem_used }),
        swap: Some(pb::Mem { total: host.swap_total, used: host.swap_used }),
        disks,
        net: Some(pb::Net { rx_bps: host.net_rx_bps, tx_bps: host.net_tx_bps }),
        ips,
    };

    let pb_containers = containers
        .iter()
        .map(|c| ContainerMetrics {
            id: c.id.clone(),
            name: c.name.clone(),
            image: c.image.clone(),
            state: c.state.clone(),
            restarts: c.restarts,
            uptime_s: c.uptime_s,
            cpu_pct: c.cpu_pct as f32,
            mem: Some(pb::ContainerMem { usage: c.mem_usage, limit: c.mem_limit }),
            net: Some(pb::Net { rx_bps: c.net_rx_bps, tx_bps: c.net_tx_bps }),
        })
        .collect();

    MetricsBatch {
        agent_id: agent_id.to_string(),
        ts: chrono::Utc::now().timestamp() as u64,
        host: Some(pb_host),
        containers: pb_containers,
        series: Vec::new(), // 阶段二：主循环汇入插件系列
        frp: None,          // 阶段二 S3：frp 状态
        task_runs: Vec::new(), // 阶段二 S4：定时任务执行结果
    }
}
