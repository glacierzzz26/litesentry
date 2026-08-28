//! docker 内置插件：容器指标采集（bollard / Docker Engine API），阶段二 S2 由 Agent 内迁出。
//!
//! 协议（见 agent/src/plugin.rs）：
//! - stdin 收一行启动命令 `{"cmd":"start","args":{...},"interval":N}`
//! - stdout 每行输出 `{"ts":...,"series":[...]}`，按 interval 周期产出
//!
//! 产出（与 Server translate.go 的契约，改动需同步两端）：
//! - `container.info`（每容器 1 条）：tags {container_id,name,image,state}，fields 全量容器指标
//!
//! 连接失败（无 docker / 无权限）时静默（不输出 series），与阶段一 Agent 行为一致；
//! 只读 Docker socket，不做任何管理操作。

use std::io::BufRead;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use bollard::container::{ListContainersOptions, Stats, StatsOptions};
use bollard::Docker;
use futures_util::StreamExt;
use tokio::io::AsyncWriteExt;

fn now_ts() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

/// 单容器指标快照（与 Server 翻译字段一一对应）。
#[derive(Default)]
struct ContainerSample {
    id: String,
    name: String,
    image: String,
    state: String,
    restarts: u32,
    uptime_s: u64,
    cpu_pct: f64,
    mem_usage: u64,
    mem_limit: u64,
    net_rx_bps: u64,
    net_tx_bps: u64,
}

/// 采集所有容器当前指标（list + 逐个 stats，非流式）。
async fn collect_containers(docker: &Docker) -> Vec<ContainerSample> {
    let Ok(containers) = docker
        .list_containers(Some(ListContainersOptions::<String> {
            all: true,
            ..Default::default()
        }))
        .await
    else {
        return Vec::new();
    };

    let mut out = Vec::with_capacity(containers.len());
    let now_secs = now_ts();
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
        if let Ok(stats) = fetch_stats(docker, &id).await {
            apply_stats(&mut sample, stats);
        }
        out.push(sample);
    }
    out
}

/// 取一次容器实时 stats（one_shot）。
async fn fetch_stats(docker: &Docker, id: &str) -> Result<Stats, Box<dyn std::error::Error + Send + Sync>> {
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
        None => Err("empty stats stream".into()),
    }
}

fn apply_stats(sample: &mut ContainerSample, s: Stats) {
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

#[tokio::main]
async fn main() {
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

    // 建立本地 docker 连接（best-effort；失败静默）。
    let docker = Docker::connect_with_local_defaults().ok();
    let mut stdout = tokio::io::stdout();

    loop {
        let mut series: Vec<serde_json::Value> = Vec::new();
        if let Some(d) = &docker {
            for c in collect_containers(d).await {
                series.push(serde_json::json!({
                    "name": "container.info",
                    "tags": {
                        "container_id": c.id,
                        "name": c.name,
                        "image": c.image,
                        "state": c.state,
                    },
                    "fields": {
                        "restarts": c.restarts,
                        "uptime_s": c.uptime_s,
                        "cpu_pct": c.cpu_pct,
                        "mem_usage": c.mem_usage,
                        "mem_limit": c.mem_limit,
                        "net_rx_bps": c.net_rx_bps,
                        "net_tx_bps": c.net_tx_bps,
                    }
                }));
            }
        }

        let out = serde_json::json!({ "ts": now_ts(), "series": series });
        if stdout.write_all(format!("{out}\n").as_bytes()).await.is_err() {
            return; // stdout 关闭（agent 已退出）→ 结束
        }
        let _ = stdout.flush().await;
        tokio::time::sleep(Duration::from_secs(interval)).await;
    }
}
