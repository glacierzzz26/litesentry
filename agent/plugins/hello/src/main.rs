//! hello 插件（S1 端到端验证 + S4 定时任务 run 参考实现）。
//!
//! 协议（见 agent/src/plugin.rs / task.rs）：
//! - `{"cmd":"start","args":{...},"interval":N}`（长驻采集）：stdout 每行 `{"ts":...,"series":[...]}`，
//!   按 interval 周期产出 `hello.tick`（fields.count 递增；args 可带 `{"interval":N}` 覆盖节奏）
//! - `{"cmd":"run","args":{...}}`（一次性，S4 定时任务）：做一份工作 → stdout 输出 → 按结果退出。
//!   测试钩子：args `{"msg":...}` 定制输出；`{"sleep":N}` 阻塞 N 秒（配合任务 timeout 验证超时）；
//!   `{"fail":true}` 退出码 1（验证 failed）。一次性插件读到命令后即应退出
//!   （agent 会收 stdout/stderr 尾部 + 退出码 + 耗时，不要求输出 series）。

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
    let cmd: serde_json::Value = match serde_json::from_str(&line) {
        Ok(v) => v,
        Err(_) => return, // 无法解析 → 直接退出（由 agent 侧记 failed）
    };

    match cmd["cmd"].as_str() {
        Some("run") => run(&cmd["args"]),
        // 兼容旧版（无 cmd 字段，只带 interval）与长驻模式
        _ => start(&cmd),
    }
}

/// 一次性 run：sleep 钩子 → 输出结果行 → 按 fail 钩子决定退出码。
fn run(args: &serde_json::Value) {
    // 测试钩子：sleep N 秒模拟阻塞（配合任务 timeout_s 验证超时 → agent 侧 kill）
    if let Some(n) = args["sleep"].as_u64() {
        std::thread::sleep(Duration::from_secs(n));
    }
    let msg = args["msg"].as_str().unwrap_or("hello run");
    let out = serde_json::json!({ "ok": true, "msg": msg, "ts": now_ts() });
    if writeln!(std::io::stdout().lock(), "{out}").is_err() {
        return; // stdout 关闭（agent 已退出/超时被杀）→ 结束
    }
    if args["fail"].as_bool().unwrap_or(false) {
        std::process::exit(1); // 测试钩子：模拟执行失败
    }
}

/// 长驻 start：按 interval 周期产出 hello.tick series。
fn start(cmd: &serde_json::Value) {
    let interval: u64 = cmd["interval"].as_u64().unwrap_or(60).max(1);

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
