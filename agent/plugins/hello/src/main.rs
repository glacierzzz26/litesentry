//! hello 插件（S1 端到端验证）。
//!
//! 协议（见 agent/src/plugin.rs）：
//! - stdin 收一行启动命令 `{"cmd":"start","args":{...},"interval":N}`
//! - stdout 每行输出 `{"ts":...,"series":[{name,tags,fields}]}`，按 interval 周期产出
//!
//! 产出 `hello.tick` series，fields.count 递增；args 可带 `{"interval":N}` 覆盖节奏。

use std::io::{BufRead, Write};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

fn now_ts() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

fn main() {
    let stdin = std::io::stdin();
    let mut line = String::new();
    // 启动命令：读一行；stdin 关闭（EOF）则直接退出
    if stdin.lock().read_line(&mut line).is_err() || line.trim().is_empty() {
        return;
    }
    let interval: u64 = serde_json::from_str::<serde_json::Value>(&line)
        .ok()
        .and_then(|v| v["interval"].as_u64())
        .unwrap_or(60)
        .max(1);

    let stdout = std::io::stdout();
    let mut n: u64 = 0;
    loop {
        n += 1;
        let out = serde_json::json!({
            "ts": now_ts(),
            "series": [{
                "name": "hello.tick",
                "tags": { "source": "hello-plugin" },
                "fields": { "count": n }
            }]
        });
        let mut lock = stdout.lock();
        if writeln!(lock, "{out}").is_err() || lock.flush().is_err() {
            return; // stdout 关闭（agent 已退出）→ 结束
        }
        std::thread::sleep(Duration::from_secs(interval));
    }
}
