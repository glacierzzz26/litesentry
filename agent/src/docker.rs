//! 容器指标采集（bollard / Docker Engine API）。
//!
//! 连接失败（无 docker / 无权限）时返回空列表，不影响主机上报；
//! 只读 Docker socket，不做任何管理操作。

use std::time::{SystemTime, UNIX_EPOCH};

use anyhow::Result;
use bollard::container::{ListContainersOptions, Stats, StatsOptions};
use bollard::Docker;
use futures_util::StreamExt;

#[derive(Debug, Clone, Default)]
pub struct ContainerSample {
    pub id: String,
    pub name: String,
    pub image: String,
    pub state: String,
    pub restarts: u32,
    pub uptime_s: u64,
    pub cpu_pct: f64,
    pub mem_usage: u64,
    pub mem_limit: u64,
    pub net_rx_bps: u64,
    pub net_tx_bps: u64,
}

/// 容器采集器：持有 Docker 客户端（连接失败时为 None）。
pub struct DockerCollector {
    docker: Option<Docker>,
}

impl DockerCollector {
    /// 建立本地 docker 连接（best-effort）。
    pub fn new() -> Self {
        let docker = Docker::connect_with_local_defaults().ok();
        Self { docker }
    }

    /// 采集所有容器当前指标（list + 逐个 stats，非流式）。
    pub async fn collect(&self) -> Result<Vec<ContainerSample>> {
        let Some(docker) = &self.docker else {
            return Ok(Vec::new());
        };
        let containers = docker
            .list_containers(Some(ListContainersOptions::<String> {
                all: true,
                ..Default::default()
            }))
            .await?;

        let mut out = Vec::with_capacity(containers.len());
        let now_secs = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0);
        for c in containers {
            let id = c.id.as_deref().unwrap_or_default().to_string();
            let mut sample = ContainerSample {
                id: id.clone(),
                name: c.names.as_ref().and_then(|n| n.first()).map(|n| n.trim_start_matches('/').to_string()).unwrap_or_default(),
                image: c.image.as_deref().unwrap_or_default().to_string(),
                state: c.state.as_deref().unwrap_or_default().to_string(),
                ..Default::default()
            };
            // created 为 unix 秒（创建时间）→ 换算运行时长
            if let Some(created) = c.created {
                sample.uptime_s = now_secs.saturating_sub(created.max(0) as u64);
            }
            // list 不含重启次数，用 inspect 补齐（失败不影响该容器其余指标）
            if let Ok(info) = docker.inspect_container(&id, None).await {
                sample.restarts = info.restart_count.unwrap_or(0).max(0) as u32;
            }
            // 尽量取 stats（失败跳过该容器，不拖垮整批）
            if let Ok(stats) = self.fetch_stats(docker, &id).await {
                self.apply_stats(&mut sample, stats);
            }
            out.push(sample);
        }
        Ok(out)
    }

    /// 取一次容器实时 stats（one_shot）。
    async fn fetch_stats(&self, docker: &Docker, id: &str) -> Result<Stats> {
        let mut stream = docker.stats(
            id,
            Some(StatsOptions {
                stream: false,
                one_shot: true,
            }),
        );
        match stream.next().await {
            Some(Ok(s)) => Ok(s),
            Some(Err(e)) => Err(e.into()),
            None => anyhow::bail!("empty stats stream"),
        }
    }

    fn apply_stats(&self, sample: &mut ContainerSample, s: Stats) {
        // CPU%：docker 标准公式（bollard Stats 中 cpu_usage/system_cpu_usage 为 Option）
        if let (Some(sy), Some(psy)) = (s.cpu_stats.system_cpu_usage, s.precpu_stats.system_cpu_usage) {
            let total_delta = s.cpu_stats.cpu_usage.total_usage.saturating_sub(s.precpu_stats.cpu_usage.total_usage) as f64;
            let sys_delta = sy.saturating_sub(psy) as f64;
            let cores = s.cpu_stats.online_cpus.unwrap_or(1).max(1) as f64;
            if sys_delta > 0.0 {
                sample.cpu_pct = (total_delta / sys_delta) * cores * 100.0;
            }
        }
        // 内存
        sample.mem_usage = s.memory_stats.usage.unwrap_or(0);
        sample.mem_limit = s.memory_stats.limit.unwrap_or(0);
        // 网络（瞬时字节，阶段一展示即可；速率后续用采样器）
        if let Some(nets) = s.networks.as_ref() {
            let mut rx = 0u64;
            let mut tx = 0u64;
            for n in nets.values() {
                rx = rx.saturating_add(n.rx_bytes);
                tx = tx.saturating_add(n.tx_bytes);
            }
            sample.net_rx_bps = rx;
            sample.net_tx_bps = tx;
        }
    }
}
