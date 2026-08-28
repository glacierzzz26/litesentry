//! disk 内置插件：磁盘占用采集（sysinfo Disks），阶段二 S2 由 Agent 内迁出。
//!
//! 协议（见 agent/src/plugin.rs）：
//! - stdin 收一行启动命令 `{"cmd":"start","args":{...},"interval":N}`
//! - stdout 每行输出 `{"ts":...,"series":[...]}`，按 interval 周期产出
//!
//! 产出（与 Server translate.go 的契约，改动需同步两端）：
//! - `disk.usage`（每挂载点 1 条）：tags {mount,fs}，fields {total,used}

use std::io::{BufRead, Write};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use sysinfo::Disks;

fn now_ts() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
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

    let mut disks = Disks::new_with_refreshed_list();
    let stdout = std::io::stdout();

    loop {
        disks.refresh(false);
        let mut series: Vec<serde_json::Value> = Vec::with_capacity(disks.len());
        for d in disks.iter() {
            series.push(serde_json::json!({
                "name": "disk.usage",
                "tags": {
                    "mount": d.mount_point().to_string_lossy().to_string(),
                    "fs": d.file_system().to_string_lossy().to_string(),
                },
                "fields": {
                    "total": d.total_space(),
                    "used": d.total_space().saturating_sub(d.available_space()),
                }
            }));
        }

        let out = serde_json::json!({ "ts": now_ts(), "series": series });
        let mut lock = stdout.lock();
        if writeln!(lock, "{out}").is_err() || lock.flush().is_err() {
            return; // stdout 关闭（agent 已退出）→ 结束
        }
        std::thread::sleep(Duration::from_secs(interval));
    }
}
