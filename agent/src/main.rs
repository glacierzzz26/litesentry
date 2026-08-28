//! litesentry-agent：轻量监控采集端（Rust）。
//!
//! 阶段二 S2：主机/容器/磁盘采集已迁入内置插件（host/docker/disk，随 agent 旁路发布）。
//! Agent 核心只保留传输核（gRPC push）+ 插件宿主 + 注册；core 分层保持传输核不变。

mod builtin;
mod client;
mod config;
mod frp;
mod plugin;

pub mod pb {
    tonic::include_proto!("litesentry");
}

use std::time::Duration;

use anyhow::Result;
use tokio::time;

use crate::client::RegisterInfo;
use crate::config::{cache_agent_id, machine_id, Config, TlsConfig};
use crate::pb::MetricsBatch;

/// 注册元数据：轻量静态读取（不再实例化完整 Collector）。
fn register_meta() -> (String, String, String, String) {
    let hostname = sysinfo::System::host_name().unwrap_or_else(|| "unknown".to_string());
    let os = sysinfo::System::long_os_version().unwrap_or_else(|| "unknown".to_string());
    let kernel = sysinfo::System::kernel_version().unwrap_or_else(|| "unknown".to_string());
    let arch = std::env::consts::ARCH.to_string();
    (hostname, os, arch, kernel)
}

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
        let (hostname, os, arch, kernel) = register_meta();
        let info = RegisterInfo {
            hostname,
            machine_id: machine_id(),
            os,
            arch,
            kernel,
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
    // S2：拉起内置采集插件（host/docker/disk），always-on
    host.start_builtins(&mut client, &cfg.token).await;

    // S3：frp 进程管理器（frps/frpc，配置经 DesiredState 下发）
    let mut frp_mgr = frp::FrpManager::new(&cfg.frp_dir, &cfg.state_dir);

    let mut applied_version: u64 = 0; // 已应用的 DesiredState 版本（心跳下发，等于即跳过）

    let mut ticker = time::interval(Duration::from_secs(cfg.interval_secs));
    loop {
        ticker.tick().await;

        // S2：主机/容器/磁盘数据全部来自内置插件 series；host/containers 字段留空
        let mut batch = build_batch(&agent_id);
        batch.series = host.drain_series();
        // S3：frp 状态（进程 + 隧道）。未启用 → None（不占 proto 字段）
        batch.frp = frp_mgr.collect().await;

        match client.push(&cfg.token, batch).await {
            Ok(ack) => {
                if let Some(ds) = ack.desired_state {
                    if ds.state_version != applied_version {
                        // 全部应用成功才推进版本号，失败留待下次心跳重试
                        let mut ok = true;
                        if let Err(e) = frp_mgr.apply(&ds).await {
                            tracing::warn!("应用 frp DesiredState 失败: {e}");
                            ok = false;
                        }
                        if let Err(e) = host.apply(&mut client, &cfg.token, &ds.plugins).await {
                            tracing::warn!("应用 DesiredState 失败: {e}");
                            ok = false;
                        }
                        if ok {
                            applied_version = ds.state_version;
                            tracing::info!("已应用 DesiredState v{}（{} 个插件）", ds.state_version, ds.plugins.len());
                        }
                    }
                }
                tracing::debug!("pushed server_time={}", ack.server_time);
            }
            Err(e) => tracing::warn!("push 失败: {e}"),
        }
    }
}

/// S2：batch 只带 series（主机/容器/磁盘均由插件产出），host/containers 留空。
fn build_batch(agent_id: &str) -> MetricsBatch {
    MetricsBatch {
        agent_id: agent_id.to_string(),
        ts: chrono::Utc::now().timestamp() as u64,
        host: None,             // S2：主机采集迁入内置插件（host.info/host.ip series）
        containers: Vec::new(), // S2：容器采集迁入内置插件（container.info series）
        series: Vec::new(),     // 主循环汇入插件系列
        frp: None,              // 阶段二 S3：frp 状态
        task_runs: Vec::new(),  // 阶段二 S4：定时任务执行结果
    }
}
