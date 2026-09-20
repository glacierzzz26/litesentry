//! 阶段二 S4：定时任务调度器。
//!
//! 任务定义经 DesiredState.tasks 下发（proto 冻结，S1 预留）：
//! TaskSpec{ task_id, cron, plugin_id, args_json, timeout_s }，标准 5 段 cron。
//! Server 只存定义与下发，执行全在 agent 侧：
//! - `apply()`：state_version 门内重建任务集（cron 解析 + 计算首次触发时间）
//! - `fire_due()`：每心跳触发到期任务 → 确保插件二进制 → 后台一次性 run（不阻塞心跳）
//! - `run_one_shot()`：spawn 插件 → stdin 写 `{"cmd":"run","args":...}` → 收 stdout/stderr
//!   尾部 8KB + 退出码 + 耗时 → TaskRunReport（status: ok|failed|timeout|skipped）
//! - 结果经 mpsc 回传，`drain_reports()` 每心跳汇入 MetricsBatch.task_runs（审计）
//!
//! 安全（S1 信任边界）：只执行指派清单内插件。任务无版本字段（proto 冻结），
//! 版本从 ds.plugins（指派 manifest）+ 内置 manifest 解析；未指派/未内置 → 该次 run 报 failed。
//!
//! 触发语义：到期即触发一次，**无论成败都推进 next_fire**（失败/未指派等不每心跳重报）；
//! 上一轮仍在跑 → 报 skipped。错过触发（agent 离线 / 心跳间隔长）不补跑。
//!
//! 立即运行：Server 置位 TaskSpec.run_now → 任务下个心跳立即执行一次（不占 cron 边界、不推进 next_fire）；
//! 触发即消费（run_now_fired 持久记录，跨 apply 防「触发后 Server 清除前的不相关配置变更重 apply」导致的重复执行），
//! 结果照常回传 → Server 收到报告清除 run_now → 下次 apply 移除 fired，可再次手动触发（触发可重复）；
//! 离线重连：run_now 保持置位 → 首轮 apply 即执行（语义「尽快执行」，不重复计 cron）。

use std::collections::{HashMap, HashSet};
use std::path::Path;
use std::process::Stdio;
use std::str::FromStr;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use anyhow::Result;
use chrono::{DateTime, Local};
use cron::Schedule;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::process::Command;
use tokio::sync::mpsc;
use tracing::{info, warn};

use crate::pb::{PluginSpec, TaskRunReport, TaskSpec};

/// 输出尾部上限（stdout+stderr 合并，超限丢头；回传前截断）。
const OUTPUT_TAIL_CAP: usize = 8 * 1024;

/// 报告缓冲上限（每心跳最多回传的条数；防极端场景爆内存）。
const REPORTS_CAP: usize = 64;

/// 一个已调度任务的状态（cron 解析结果 + 下次触发时间 + 立即运行标记）。
struct TaskState {
    schedule: Schedule,
    plugin_id: String,
    args_json: String,
    timeout_s: u32,
    next_fire: DateTime<Local>,
    /// Server 置位（POST /run）→ 下个心跳立即执行一次；报告到达后 Server 清除。
    run_now: bool,
}

/// 定时任务调度器。
pub struct TaskRunner {
    /// task_id → 调度状态（DesiredState.tasks 重建）。
    tasks: HashMap<String, TaskState>,
    /// plugin_id → version（ds.plugins 指派 manifest + 内置 manifest；版本解析用）。
    plugins: HashMap<String, String>,
    /// 正在后台执行的任务（防重入：同一任务不并发两轮）。
    in_flight: HashSet<String>,
    /// 已消费的「立即运行」标记（跨 apply 持久）：run_now 置位期间只执行一次。
    /// 放 TaskRunner 而非 TaskState——触发后、Server 清除前的不相关配置变更会重 apply，
    /// TaskState 被整体重建会丢标记，这里保留才能守住「每标记最多执行一次」。
    run_now_fired: HashSet<String>,
    /// 后台 run 结果回传通道。
    rx: mpsc::Receiver<TaskRunReport>,
    tx: mpsc::Sender<TaskRunReport>,
    /// 本心跳待回传报告（fire_due 直接产生的失败/跳过 + 通道回收的 run 结果）。
    reports: Vec<TaskRunReport>,
}

impl TaskRunner {
    pub fn new() -> Self {
        let (tx, rx) = mpsc::channel(REPORTS_CAP);
        Self {
            tasks: HashMap::new(),
            plugins: HashMap::new(),
            in_flight: HashSet::new(),
            run_now_fired: HashSet::new(),
            rx,
            tx,
            reports: Vec::new(),
        }
    }

    /// 应用 DesiredState 的任务/插件清单（state_version 门内调用，幂等）。
    ///
    /// - 插件版本表 = 内置 manifest 兜底 + 指派 manifest **覆盖**：内置插件并入 Server
    ///   插件体系后，「指派版本」才是权威（升级后的 host/docker/disk 需按指派版本执行），
    ///   内置 manifest 仅在未指派时兜底（旧 Server / 空清单）。
    /// - 任务集整体重建：同内容重算 next_fire 与旧值一致（cron 确定性）
    /// - cron 解析失败的任务跳过调度（Server 侧已有轻量校验拦截明显非法）
    pub fn apply(&mut self, tasks: &[TaskSpec], plugins: &[PluginSpec]) {
        let mut plugin_versions: HashMap<String, String> = HashMap::new();
        // 1) 内置 manifest 兜底
        for b in crate::builtin::BUILTIN_PLUGINS {
            plugin_versions.insert(b.id.to_string(), b.version.to_string());
        }
        // 2) 指派 manifest 覆盖（含内置插件的升级版本）
        for p in plugins {
            plugin_versions.insert(p.plugin_id.clone(), p.version.clone());
        }
        self.plugins = plugin_versions;

        let now = Local::now();
        let mut tasks_map = HashMap::with_capacity(tasks.len());
        for t in tasks {
            match parse_schedule(&t.cron) {
                Ok(schedule) => {
                    let next_fire = schedule
                        .after(&now)
                        .next()
                        .unwrap_or_else(|| now + chrono::Duration::minutes(1));
                    tasks_map.insert(
                        t.task_id.clone(),
                        TaskState {
                            schedule,
                            plugin_id: t.plugin_id.clone(),
                            args_json: t.args_json.clone(),
                            timeout_s: t.timeout_s,
                            next_fire,
                            run_now: t.run_now,
                        },
                    );
                }
                Err(e) => warn!("任务 {} cron 无效，跳过调度: {e}", t.task_id),
            }
        }
        self.tasks = tasks_map;
        // run_now_fired 跨 apply 持久：仅保留「仍存在且 Server 未清除 run_now」的任务。
        // 任务已删除 / Server 收到报告清除标记 → 移除，允许下一次手动触发。
        self.run_now_fired.retain(|id| self.tasks.get(id).is_some_and(|st| st.run_now));
        // 移除任务后，其后台 run 若仍返回结果照常上报（append-only 审计），只清 in_flight
        self.in_flight.retain(|id| self.tasks.contains_key(id));
    }

    /// 分类并消费本轮到期任务，返回待执行列表 (task_id, 是否 run_now 触发)。
    ///
    /// - cron 到期：推进 next_fire（无论后续执行成败，不每心跳重报）
    /// - run_now 标记（Server 置位、尚未消费）：insert run_now_fired——跨 apply 持久，
    ///   触发后、Server 清除前的任何重 apply 都不重复执行；报告到达 Server 清除 run_now 后，
    ///   下一次 apply 移除 fired → 可再次手动触发
    /// - 仅 cron 到期推进 next_fire；run_now 触发不占 cron 边界
    fn consume_due(&mut self, now: DateTime<Local>) -> Vec<(String, bool)> {
        let due: Vec<(String, bool, bool)> = self
            .tasks
            .iter()
            .filter_map(|(id, st)| {
                let cron_due = now >= st.next_fire;
                let run_now_due = st.run_now && !self.run_now_fired.contains(id);
                if cron_due || run_now_due {
                    Some((id.clone(), cron_due, run_now_due))
                } else {
                    None
                }
            })
            .collect();

        let mut out = Vec::with_capacity(due.len());
        for (id, cron_due, run_now_due) in due {
            if let Some(task) = self.tasks.get_mut(&id) {
                if cron_due {
                    task.next_fire = task
                        .schedule
                        .after(&now)
                        .next()
                        .unwrap_or_else(|| now + chrono::Duration::minutes(1));
                }
            }
            if run_now_due {
                self.run_now_fired.insert(id.clone());
            }
            out.push((id, run_now_due));
        }
        out
    }

    /// 触发到期任务（每心跳 push 前调用）。
    ///
    /// 返回前先回收已完成的后台 run（清 in_flight），避免「run 刚完成但结果未入通道」
    /// 被误判为 skipped；随后逐个触发到期任务。ensure_binary 必要时下载（短暂阻塞可接受）。
    pub async fn fire_due(
        &mut self,
        host: &crate::plugin::PluginHost,
        client: &mut crate::client::Client,
        token: &str,
    ) {
        while let Ok(r) = self.rx.try_recv() {
            self.in_flight.remove(&r.task_id);
            self.push_report(r);
        }

        let now = Local::now();
        // 分类 + 消费：cron 到期推进 next_fire；run_now 触发 insert run_now_fired（跨 apply 防重复）
        let due = self.consume_due(now);

        for (id, run_now_due) in due {
            let task = match self.tasks.get_mut(&id) {
                Some(t) => t,
                None => continue,
            };
            let (plugin_id, args_json, timeout_s) =
                (task.plugin_id.clone(), task.args_json.clone(), task.timeout_s);

            if self.in_flight.contains(&id) {
                self.push_report(fail_report(&id, "skipped", 0, "上一轮仍在执行，跳过本次触发".to_string()));
                continue;
            }
            let version = match self.plugins.get(&plugin_id) {
                Some(v) => v.clone(),
                None => {
                    self.push_report(fail_report(
                        &id,
                        "failed",
                        -1,
                        format!("插件 {plugin_id} 未指派到本节点"),
                    ));
                    continue;
                }
            };
            let spec = PluginSpec {
                plugin_id: plugin_id.clone(),
                version,
                args_json: String::new(),
            };
            let bin = match host.ensure_binary(client, token, &spec).await {
                Ok(b) => b,
                Err(e) => {
                    self.push_report(fail_report(
                        &id,
                        "failed",
                        -1,
                        format!("插件 {plugin_id} 二进制不可用: {e}"),
                    ));
                    continue;
                }
            };

            self.in_flight.insert(id.clone());
            if run_now_due {
                info!("任务 {id} 触发（立即运行）：{plugin_id}@{}", spec.version);
            } else {
                info!("任务 {id} 触发：{plugin_id}@{}", spec.version);
            }
            let tx = self.tx.clone();
            let tid = id.clone();
            tokio::spawn(async move {
                let report = run_one_shot(&bin, &args_json, timeout_s, &tid).await;
                let _ = tx.send(report).await;
            });
        }
    }

    /// 取走本心跳待回传的报告（含后台 run 完成的结果）。
    pub fn drain_reports(&mut self) -> Vec<TaskRunReport> {
        while let Ok(r) = self.rx.try_recv() {
            self.in_flight.remove(&r.task_id);
            self.reports.push(r);
        }
        std::mem::take(&mut self.reports)
    }

    fn push_report(&mut self, r: TaskRunReport) {
        if self.reports.len() >= REPORTS_CAP {
            self.reports.remove(0);
        }
        self.reports.push(r);
    }
}

/// 构造未实际执行 run 的报告（spawn 前失败 / 跳过 / 版本不可用）：起止同为当前时间。
fn fail_report(task_id: &str, status: &str, exit_code: i32, output: String) -> TaskRunReport {
    let now = chrono::Utc::now().timestamp() as u64;
    TaskRunReport {
        task_id: task_id.to_string(),
        started_at: now,
        finished_at: now,
        status: status.to_string(),
        exit_code,
        output,
    }
}

/// 一次性运行插件：spawn 二进制 → stdin 写 run 命令 → 并发收 stdout/stderr 尾部 → 等退出。
///
/// 超时：`tokio::time::timeout`，到期 kill → status=timeout / exit_code=-1。
/// 退出码 0 → ok；非 0 → failed；spawn 失败 → failed。等读取任务排空管道后再取输出，
/// 保证输出完整（子进程已退出 → EOF 立即到达）。
async fn run_one_shot(bin: &Path, args_json: &str, timeout_s: u32, task_id: &str) -> TaskRunReport {
    let started = chrono::Utc::now().timestamp() as u64;
    let mut child = match Command::new(bin)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
    {
        Ok(c) => c,
        Err(e) => return fail_report(task_id, "failed", -1, format!("spawn 失败: {e}")),
    };

    if let Some(mut stdin) = child.stdin.take() {
        let cmd = build_run_cmd(args_json);
        let _ = stdin.write_all(cmd.as_bytes()).await;
        let _ = stdin.write_all(b"\n").await;
        let _ = stdin.shutdown().await; // EOF → 一次性插件读到命令即可退出
    }

    let tail: Arc<Mutex<String>> = Arc::new(Mutex::new(String::new()));
    let mut readers = Vec::new();
    if let Some(out) = child.stdout.take() {
        readers.push(drain_tail(out, tail.clone()));
    }
    if let Some(err) = child.stderr.take() {
        readers.push(drain_tail(err, tail.clone()));
    }

    let (status, exit_code) = wait_with_timeout(&mut child, timeout_s).await;
    for handle in readers {
        let _ = handle.await;
    }
    let finished = chrono::Utc::now().timestamp() as u64;
    let output = tail.lock().unwrap().clone();
    TaskRunReport {
        task_id: task_id.to_string(),
        started_at: started,
        finished_at: finished,
        status: status.to_string(),
        exit_code,
        output,
    }
}

fn run_status(st: &std::process::ExitStatus) -> &'static str {
    if st.success() {
        "ok"
    } else {
        "failed"
    }
}

/// cron 0.12 只接受 6/7 字段（秒 分 时 日 月 周 [年]），5 字段（分 时 日 月 周）
/// 为常见写法 → 前置 "0 "（秒=0，语义等价：分边界触发）。其余表达式原样传给 crate。
fn normalize_cron(expr: &str) -> String {
    let fields = expr.split_whitespace().count();
    if fields == 5 {
        format!("0 {expr}")
    } else {
        expr.to_string()
    }
}

fn parse_schedule(expr: &str) -> Result<Schedule> {
    let normalized = normalize_cron(expr);
    Schedule::from_str(&normalized).map_err(|e| anyhow::anyhow!("cron 无效: {e}"))
}

/// 等子进程退出，应用超时：到期 kill → (timeout, -1)；退出码 0 → ok；非 0 → failed。
async fn wait_with_timeout(child: &mut tokio::process::Child, timeout_s: u32) -> (&'static str, i32) {
    if timeout_s > 0 {
        match tokio::time::timeout(Duration::from_secs(timeout_s as u64), child.wait()).await {
            Ok(Ok(st)) => (run_status(&st), st.code().unwrap_or(0)),
            Ok(Err(_)) => ("failed", -1), // wait 本身失败（罕见）
            Err(_) => {
                let _ = child.kill().await;
                let _ = child.wait().await;
                ("timeout", -1)
            }
        }
    } else {
        match child.wait().await {
            Ok(st) => (run_status(&st), st.code().unwrap_or(0)),
            Err(_) => ("failed", -1),
        }
    }
}

/// 构造插件 run 命令（args_json 解析成 JSON 值嵌入；非法退回 null，与 start 命令一致）。
fn build_run_cmd(args_json: &str) -> String {
    let args: serde_json::Value = if args_json.trim().is_empty() {
        serde_json::Value::Null
    } else {
        serde_json::from_str(args_json).unwrap_or(serde_json::Value::Null)
    };
    serde_json::json!({ "cmd": "run", "args": args }).to_string()
}

/// 把一段输出追加进尾部缓冲（超限丢头）。纯函数，便于单测。
fn append_tail(buf: &mut String, chunk: &str, cap: usize) {
    if chunk.is_empty() {
        return;
    }
    buf.push_str(chunk);
    if buf.len() > cap {
        let excess = buf.len() - cap;
        buf.drain(..excess);
    }
}

/// 后台读取任务：把 reader 的字节流不断收进共享尾部缓冲（进程退出 / EOF 结束）。
fn drain_tail<R>(reader: R, tail: Arc<Mutex<String>>) -> tokio::task::JoinHandle<()>
where
    R: tokio::io::AsyncRead + Unpin + Send + 'static,
{
    tokio::spawn(async move {
        let mut reader = reader;
        let mut buf = [0u8; 4096];
        loop {
            match reader.read(&mut buf).await {
                Ok(0) | Err(_) => break,
                Ok(n) => {
                    let chunk = String::from_utf8_lossy(&buf[..n]).into_owned();
                    let mut t = tail.lock().unwrap();
                    append_tail(&mut t, &chunk, OUTPUT_TAIL_CAP);
                }
            }
        }
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn build_run_cmd_shapes() {
        // args_json 为空 → args=null；合法 JSON 原样嵌入；非法 JSON → 退回 null
        let v: serde_json::Value = serde_json::from_str(&build_run_cmd("")).unwrap();
        assert_eq!(v["cmd"], "run");
        assert_eq!(v["args"], serde_json::Value::Null);

        let v2: serde_json::Value =
            serde_json::from_str(&build_run_cmd(r#"{"msg":"ping"}"#)).unwrap();
        assert_eq!(v2["cmd"], "run");
        assert_eq!(v2["args"]["msg"], "ping");

        let v3: serde_json::Value = serde_json::from_str(&build_run_cmd("not json")).unwrap();
        assert_eq!(v3["args"], serde_json::Value::Null);
    }

    #[test]
    fn append_tail_drops_head() {
        let mut buf = String::new();
        append_tail(&mut buf, "abcdefghij", 10); // 恰好 cap
        assert_eq!(buf, "abcdefghij");
        append_tail(&mut buf, "0123456789", 10); // 超限 10 → 丢头 → 保留尾部
        assert_eq!(buf, "0123456789");
        assert_eq!(buf.len(), 10);
    }

    #[test]
    fn append_tail_partial_truncate() {
        let mut buf = String::new();
        append_tail(&mut buf, "abc", 10);
        append_tail(&mut buf, "defghijklmnop", 10); // 3+13=16，超限 6 → 丢头 6
        assert_eq!(buf, "ghijklmnop");
        append_tail(&mut buf, "", 10); // 空块 no-op
        assert_eq!(buf, "ghijklmnop");
    }

    #[test]
    fn normalize_cron_prepends_seconds() {
        // 5 字段 → 前置 "0 "（秒=0）；6/7 字段原样
        assert_eq!(normalize_cron("* * * * *"), "0 * * * * *");
        assert_eq!(normalize_cron("*/5 * * * *"), "0 */5 * * * *");
        assert_eq!(normalize_cron("0 5 * * * *"), "0 5 * * * *");
        assert_eq!(normalize_cron("30 0 5 * * * *"), "30 0 5 * * * *");
    }

    #[test]
    fn cron_next_fire_advances() {
        // 5 字段 `* * * * *` → 下一个整分钟边界，严格晚于 now 且不超 1 分钟
        let sched = parse_schedule("* * * * *").unwrap();
        let now = Local::now();
        let next = sched.after(&now).next().unwrap();
        assert!(next > now, "next_fire 必须严格晚于 now");
        assert!(next - now <= chrono::Duration::minutes(1) + chrono::Duration::seconds(2));
    }

    #[test]
    fn cron_invalid_rejected() {
        assert!(parse_schedule("not a cron").is_err());
        assert!(parse_schedule("61 * * * *").is_err(), "分超范围");
        // 6 字段（显式秒）同样支持
        assert!(parse_schedule("0 5 * * * *").is_ok());
    }

    /// run 协议对真实进程的端到端：cat 回显 stdin → ok + 输出包含命令。
    #[cfg(unix)]
    #[tokio::test]
    async fn run_one_shot_ok_cat() {
        let r = run_one_shot(Path::new("/bin/cat"), r#"{"msg":"ping"}"#, 5, "t-ok").await;
        assert_eq!(r.status, "ok");
        assert_eq!(r.exit_code, 0);
        assert!(
            r.output.contains("ping"),
            "cat 应回显 run 命令到 stdout: {:?}",
            r.output
        );
        assert!(r.finished_at >= r.started_at);
    }

    /// 退出码非 0 → failed（false 恒退出 1）。
    #[cfg(unix)]
    #[tokio::test]
    async fn run_one_shot_failed_exit_code() {
        let r = run_one_shot(Path::new("/bin/false"), "", 5, "t-fail").await;
        assert_eq!(r.status, "failed");
        assert_eq!(r.exit_code, 1);
    }

    /// 超时 → kill → status=timeout / exit_code=-1（sleep 60 被 1s 超时杀掉）。
    #[cfg(unix)]
    #[tokio::test]
    async fn wait_with_timeout_kills_slow_child() {
        let mut child = Command::new("/bin/sleep")
            .arg("60")
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .unwrap();
        let started = chrono::Utc::now().timestamp() as u64;
        let (status, code) = wait_with_timeout(&mut child, 1).await;
        assert_eq!(status, "timeout");
        assert_eq!(code, -1);
        assert!(chrono::Utc::now().timestamp() as u64 >= started);
    }

    /// 正常退出且码 0 → ok（true 恒退出 0）。
    #[cfg(unix)]
    #[tokio::test]
    async fn wait_with_timeout_ok_child() {
        let mut child = Command::new("/bin/true").spawn().unwrap();
        let (status, code) = wait_with_timeout(&mut child, 5).await;
        assert_eq!(status, "ok");
        assert_eq!(code, 0);
    }

    /// 构造测试任务 spec：plugin 用 "nope"（不在 manifest/内置 → fire_due 走未指派早退，不 spawn；
    /// 单测只验证 consume_due 的调度决策，不需要 PluginHost/Client）。
    fn spec(id: &str, cron: &str, run_now: bool) -> TaskSpec {
        TaskSpec {
            task_id: id.to_string(),
            cron: cron.to_string(),
            plugin_id: "nope".to_string(),
            args_json: String::new(),
            timeout_s: 5,
            run_now,
        }
    }

    /// 立即运行核心闭环：触发一次并消费 → 跨 apply 防重复 → Server 清除后放行 → 可再次触发。
    #[test]
    fn run_now_consumed_once_until_server_clears() {
        let mut runner = TaskRunner::new();
        // cron 明年才到期（"0 0 1 1 *"）→ 与 cron 触发可区分；run_now=true
        runner.apply(&[spec("t1", "0 0 1 1 *", true)], &[]);
        let now = Local::now();

        // 心跳 1：run_now 触发一次并消费
        let due = runner.consume_due(now);
        assert_eq!(due.len(), 1);
        assert!(due[0].1, "本轮应为 run_now 触发");
        assert!(runner.run_now_fired.contains("t1"), "触发后应记录 fired");
        assert!(
            runner.tasks.get("t1").unwrap().next_fire > now,
            "run_now 触发不应推进 cron 边界"
        );

        // 心跳 2（Server 尚未清除，期间发生不相关配置变更 → 重 apply 同 spec）：不重复触发
        runner.apply(&[spec("t1", "0 0 1 1 *", true)], &[]);
        assert!(runner.run_now_fired.contains("t1"), "run_now_fired 应跨 apply 持久");
        assert!(runner.consume_due(now).is_empty(), "已消费 → 不再触发");

        // 报告到达 → Server 清除 run_now（apply 带 run_now=false）→ fired 被清理
        runner.apply(&[spec("t1", "0 0 1 1 *", false)], &[]);
        assert!(!runner.run_now_fired.contains("t1"), "Server 清除后 fired 应移除");
        assert!(runner.consume_due(now).is_empty(), "run_now=false → 无触发");

        // 再次手动触发（run_now 回到 true）→ 又能立即执行（触发可重复）
        runner.apply(&[spec("t1", "0 0 1 1 *", true)], &[]);
        assert_eq!(runner.consume_due(now).len(), 1, "再次置位应再次触发");
        assert!(runner.run_now_fired.contains("t1"));
    }

    /// run_now 与 cron 到期同时发生：两者都消费（next_fire 推进 + fired 记录），互不干扰。
    #[test]
    fn run_now_and_cron_due_both_consume() {
        let mut runner = TaskRunner::new();
        runner.apply(&[spec("t1", "* * * * *", true)], &[]);
        // 取 apply 算出的 cron 边界作为触发时刻：cron_due=true 且 run_now 未消费 → 双触发
        let boundary = runner.tasks.get("t1").unwrap().next_fire;
        let due = runner.consume_due(boundary);
        assert_eq!(due.len(), 1);
        assert!(due[0].1, "本轮含 run_now 触发");
        assert!(
            runner.tasks.get("t1").unwrap().next_fire > boundary,
            "cron 到期应推进 next_fire"
        );
        assert!(runner.run_now_fired.contains("t1"));
        assert!(runner.consume_due(boundary).is_empty(), "双触发已全部消费");
    }

    /// 任务删除：run_now_fired 一并清理，避免泄漏阻止重建后的任务触发。
    #[test]
    fn task_deleted_prunes_run_now_fired() {
        let mut runner = TaskRunner::new();
        runner.apply(&[spec("t1", "0 0 1 1 *", true)], &[]);
        assert_eq!(runner.consume_due(Local::now()).len(), 1);
        assert!(runner.run_now_fired.contains("t1"));

        // 任务被删除（空 spec）→ fired 移除；重建同 id 新任务（run_now=true）→ 可立即触发
        runner.apply(&[], &[]);
        assert!(runner.run_now_fired.is_empty());
        runner.apply(&[spec("t1", "0 0 1 1 *", true)], &[]);
        assert_eq!(runner.consume_due(Local::now()).len(), 1, "重建后可再次触发");
    }
}
