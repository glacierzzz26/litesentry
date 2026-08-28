//! 插件宿主：DesiredState 应用 + FetchPlugin 下载/校验/缓存 + 子进程生命周期。
//!
//! 协议：
//! - Agent → 插件（stdin 一行）：`{"cmd":"start","args":{...},"interval":N}`（长驻采集）
//! - 插件 → Agent（stdout 逐行）：`{"ts":...,"series":[{name,tags,fields}]}` → 映射 proto Series
//!
//! 白名单模型：只运行 Server 指派清单（DesiredState.plugins）内的 plugin_id:version；
//! 二进制缓存按 plugin_id/version 存放，下载时对末尾分块 SHA-256 校验，缓存复用再次校验。

use std::collections::{HashMap, HashSet};
use std::path::PathBuf;
use std::process::Stdio;
use std::time::Duration;

use anyhow::{anyhow, Context, Result};
use serde::Deserialize;
use sha2::Digest;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::process::Command;
use tokio::sync::mpsc;
use tracing::{debug, info, warn};

use crate::client::hex;
use crate::pb::{PluginSpec, Series};

/// 一个运行中的插件子进程（start 长驻模式）。HashMap key = plugin_id。
struct Proc {
    version: String,
    child: tokio::process::Child,
    stdin: tokio::process::ChildStdin,
}

/// 插件宿主：管理本机运行中的插件进程，收集其产出的 Series。
pub struct PluginHost {
    cache_dir: PathBuf,
    procs: HashMap<String, Proc>, // plugin_id → 进程
    builtin_ids: HashSet<String>, // 内置插件（always-on）：apply() 不启停
    tx: mpsc::Sender<Series>,
    rx: mpsc::Receiver<Series>,
    interval: u64, // 上报间隔（秒），作为插件 interval 缺省
}

impl PluginHost {
    pub fn new(cache_dir: &str, interval: u64) -> Self {
        let (tx, rx) = mpsc::channel(512);
        Self {
            cache_dir: PathBuf::from(cache_dir),
            procs: HashMap::new(),
            builtin_ids: crate::builtin::BUILTIN_PLUGINS.iter().map(|e| e.id.to_string()).collect(),
            tx,
            rx,
            interval,
        }
    }

    /// 取走当前缓冲区内的全部 series（每次 push 前调用）。
    pub fn drain_series(&mut self) -> Vec<Series> {
        let mut out = Vec::new();
        while let Ok(s) = self.rx.try_recv() {
            out.push(s);
        }
        out
    }

    /// 应用 DesiredState 的插件清单（幂等）：停止已移除/变更版本，启动缺失。
    /// 单个插件失败不影响其余（失败隔离，插件问题不拖垮心跳）。
    pub async fn apply(&mut self, client: &mut crate::client::Client, token: &str, desired: &[PluginSpec]) -> Result<()> {
        // 1. 停止：不再指派，或版本变更（内置插件 always-on，跳过）
        let current: Vec<String> = self.procs.keys().cloned().collect();
        for id in current {
            if self.builtin_ids.contains(&id) {
                continue;
            }
            let spec = desired.iter().find(|p| p.plugin_id == id);
            match spec {
                None => self.stop(&id).await,
                Some(s) if s.version != self.procs[&id].version => {
                    info!("插件 {id} 版本 {} → {}", self.procs[&id].version, s.version);
                    self.stop(&id).await
                }
                _ => {}
            }
        }
        // 2. 启动：缺失
        for spec in desired {
            if !self.procs.contains_key(&spec.plugin_id) {
                match self.start(client, token, spec).await {
                    Ok(()) => info!("插件 {}@{} 已启动", spec.plugin_id, spec.version),
                    Err(e) => warn!("插件 {}@{} 启动失败: {e}", spec.plugin_id, spec.version),
                }
            }
        }
        Ok(())
    }

    /// 启动全部内置插件（阶段二 D2：always-on，不随 DesiredState 启停）。
    /// 单插件失败（清单缺失 / SHA-256 不匹配 / spawn 失败）仅告警，不影响其余。
    pub async fn start_builtins(&mut self, client: &mut crate::client::Client, token: &str) {
        for entry in crate::builtin::BUILTIN_PLUGINS {
            if self.procs.contains_key(entry.id) {
                continue;
            }
            let spec = PluginSpec {
                plugin_id: entry.id.to_string(),
                version: entry.version.to_string(),
                args_json: String::new(),
            };
            match self.start(client, token, &spec).await {
                Ok(()) => info!("内置插件 {}@{} 已启动", spec.plugin_id, spec.version),
                Err(e) => warn!("内置插件 {}@{} 启动失败: {e}", spec.plugin_id, spec.version),
            }
        }
    }

    /// 启动一个长驻插件：确保二进制 → spawn → 写 start 命令 → 后台读 stdout/stderr。
    async fn start(&mut self, client: &mut crate::client::Client, token: &str, spec: &PluginSpec) -> Result<()> {
        let bin = self.ensure_binary(client, token, spec).await?;
        let mut child = Command::new(&bin)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .with_context(|| format!("spawn plugin {}", spec.plugin_id))?;
        let mut stdin = child.stdin.take().ok_or_else(|| anyhow!("no stdin"))?;
        let stdout = child.stdout.take().ok_or_else(|| anyhow!("no stdout"))?;
        let stderr = child.stderr.take().ok_or_else(|| anyhow!("no stderr"))?;

        // 写启动命令（含 interval）
        let cmd = build_start_cmd(&spec.args_json, self.interval);
        stdin.write_all(cmd.as_bytes()).await?;
        stdin.write_all(b"\n").await?;
        stdin.flush().await?;

        // stdout 读取任务：解析 JSON-line → series
        let tx = self.tx.clone();
        let id = spec.plugin_id.clone();
        let version = spec.version.clone();
        tokio::spawn(async move {
            let mut reader = BufReader::new(stdout);
            let mut line = String::new();
            loop {
                line.clear();
                match reader.read_line(&mut line).await {
                    Ok(0) | Err(_) => break, // EOF/错误 → 进程退出或被强杀
                    Ok(_) => {
                        let l = line.trim();
                        if l.is_empty() {
                            continue;
                        }
                        match parse_output(l) {
                            Some(series) => {
                                for s in series {
                                    if tx.try_send(s).is_err() {
                                        // 缓冲满：丢弃，不阻塞读取（防止插件写阻塞）
                                        debug!("插件 {id} series 缓冲满，丢弃一条");
                                        break;
                                    }
                                }
                            }
                            None => warn!("插件 {id} 输出无法解析: {l}"),
                        }
                    }
                }
            }
            info!("插件 {id}@{version} 进程退出（stdout EOF）");
        });

        // stderr 读取任务：仅打点，不处理
        let id2 = spec.plugin_id.clone();
        tokio::spawn(async move {
            let mut reader = BufReader::new(stderr);
            let mut line = String::new();
            loop {
                line.clear();
                match reader.read_line(&mut line).await {
                    Ok(0) | Err(_) => break,
                    Ok(_) => {
                        let l = line.trim();
                        if !l.is_empty() {
                            warn!("插件 {id2} stderr: {l}");
                        }
                    }
                }
            }
        });

        self.procs.insert(
            spec.plugin_id.clone(),
            Proc { version: spec.version.clone(), child, stdin },
        );
        Ok(())
    }

    /// 停止插件：stdin EOF 触发正常退出，2s 超时再强杀。
    async fn stop(&mut self, id: &str) {
        if let Some(mut p) = self.procs.remove(id) {
            let _ = p.stdin.shutdown().await;
            if tokio::time::timeout(Duration::from_secs(2), p.child.wait()).await.is_err() {
                let _ = p.child.kill().await;
                let _ = p.child.wait().await;
            }
            info!("插件 {id} 已停止");
        }
    }

    /// 确保插件二进制可用。内置插件（清单命中且版本一致）→ 本地 SHA-256 复核，不走网络；
    /// 外部插件 → 缓存目录（按 plugin_id/version）校验，缺/旧则 FetchPlugin 下载。
    async fn ensure_binary(&self, client: &mut crate::client::Client, token: &str, spec: &PluginSpec) -> Result<PathBuf> {
        if let Some(entry) = crate::builtin::find(&spec.plugin_id) {
            if entry.version == spec.version {
                return crate::builtin::verify(entry).map_err(|e| anyhow!(e));
            }
            warn!(
                "内置插件 {} 清单版本 {} ≠ 期望 {} —— 走 FetchPlugin 下载",
                spec.plugin_id, entry.version, spec.version
            );
        }
        let dir = self.cache_dir.join(&spec.plugin_id).join(&spec.version);
        let bin = dir.join("plugin");
        let sidecar = dir.join("plugin.sha256");

        if bin.exists() && sidecar.exists() {
            let want = std::fs::read_to_string(&sidecar)?.trim().to_string();
            match file_sha256(&bin) {
                Ok(got) if got == want => return Ok(bin),
                _ => {
                    warn!("插件 {}@{} 缓存校验失败，重新下载", spec.plugin_id, spec.version);
                    let _ = std::fs::remove_file(&bin);
                    let _ = std::fs::remove_file(&sidecar);
                }
            }
        }

        let data = client.fetch_plugin(token, &spec.plugin_id, &spec.version).await?;
        std::fs::create_dir_all(&dir)?;
        set_private(&dir)?;
        std::fs::write(&bin, &data)?;
        set_exec(&bin)?;
        std::fs::write(&sidecar, hex(&sha2::Sha256::digest(&data)))?;
        Ok(bin)
    }
}

/// 构造插件 start 命令（args_json 是 JSON 字符串，解析成 JSON 值嵌入命令行）。
fn build_start_cmd(args_json: &str, interval: u64) -> String {
    let args: serde_json::Value = if args_json.trim().is_empty() {
        serde_json::Value::Null
    } else {
        serde_json::from_str(args_json).unwrap_or(serde_json::Value::Null)
    };
    serde_json::json!({ "cmd": "start", "args": args, "interval": interval }).to_string()
}

/// 解析插件输出行 → 若干 Series（行级 ts 缺省用当前时间）。
fn parse_output(line: &str) -> Option<Vec<Series>> {
    let out: PluginLine = serde_json::from_str(line).ok()?;
    let ts = out.ts.unwrap_or_else(|| {
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_secs() as u64)
            .unwrap_or(0)
    });
    Some(
        out.series
            .into_iter()
            .map(|s| Series { name: s.name, tags: s.tags, fields: s.fields, ts })
            .collect(),
    )
}

#[derive(Deserialize)]
struct PluginLine {
    ts: Option<u64>,
    #[serde(default)]
    series: Vec<PluginSeries>,
}

#[derive(Deserialize)]
struct PluginSeries {
    name: String,
    #[serde(default)]
    tags: HashMap<String, String>,
    #[serde(default)]
    fields: HashMap<String, f64>,
}

fn file_sha256(path: &std::path::Path) -> Result<String> {
    let data = std::fs::read(path)?;
    Ok(hex(&sha2::Sha256::digest(&data)))
}

#[cfg(unix)]
fn set_private(dir: &std::path::Path) -> Result<()> {
    use std::os::unix::fs::PermissionsExt;
    std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o700))?;
    Ok(())
}

#[cfg(unix)]
fn set_exec(bin: &std::path::Path) -> Result<()> {
    use std::os::unix::fs::PermissionsExt;
    let mut perm = std::fs::metadata(bin)?.permissions();
    perm.set_mode(0o700); // owner rwx（含 x，需可执行）
    std::fs::set_permissions(bin, perm)?;
    Ok(())
}

#[cfg(not(unix))]
fn set_private(_dir: &std::path::Path) -> Result<()> {
    Ok(())
}

#[cfg(not(unix))]
fn set_exec(_bin: &std::path::Path) -> Result<()> {
    Ok(())
}
