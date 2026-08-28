//! 内置插件清单：随 agent 发布的采集插件（host / docker / disk）。
//!
//! 清单由 build.rs 在构建期生成（读插件二进制算 SHA-256 + 插件 Cargo.toml 版本）。
//! 运行期从 `<agent_exe>/plugins/<bin_name>`（或 `LS_BUILTIN_DIR` 覆盖）解析，
//! 并对文件做 SHA-256 复核 —— 防止旁路发布时插件文件被替换 / 与 agent 版本错配。
//!
//! 内置插件 **always-on**（阶段二 D2）：不随 DesiredState 指派，Agent 启动即拉起；
//! 信任继承自 agent 本体（随本体发布），运行时校验是防替换防线。
//! 外部插件（hello / 未来自定义）仍走 DesiredState 白名单 + FetchPlugin 校验。

include!(concat!(env!("OUT_DIR"), "/builtin.rs"));

use sha2::Digest;
use std::path::PathBuf;

/// 内置插件查找目录：`LS_BUILTIN_DIR` 覆盖；默认 `<agent_exe>/plugins`。
pub fn builtin_dir() -> PathBuf {
    if let Ok(d) = std::env::var("LS_BUILTIN_DIR") {
        if !d.is_empty() {
            return PathBuf::from(d);
        }
    }
    std::env::current_exe()
        .ok()
        .and_then(|e| e.parent().map(|p| p.join("plugins")))
        .unwrap_or_else(|| PathBuf::from("plugins"))
}

/// 按 plugin_id 查内置清单条目（构建期未收录/缺失时返回 None）。
pub fn find(id: &str) -> Option<&'static BuiltinEntry> {
    BUILTIN_PLUGINS.iter().find(|e| e.id == id)
}

/// 校验内置插件二进制存在、SHA-256 与构建期清单一致，并确保可执行；返回路径。
#[cfg(unix)]
pub fn verify(entry: &BuiltinEntry) -> Result<PathBuf, String> {
    use std::os::unix::fs::PermissionsExt;
    let path = builtin_dir().join(entry.bin_name);
    let data = std::fs::read(&path)
        .map_err(|e| format!("内置插件 {} 读取失败（{}）: {}", entry.id, path.display(), e))?;
    let got = crate::client::hex(&sha2::Sha256::digest(&data));
    if got != entry.sha256 {
        return Err(format!(
            "内置插件 {} SHA-256 不匹配（期望 {}，实得 {}）——插件文件可能被替换或版本错配",
            entry.id, entry.sha256, got
        ));
    }
    let _ = std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o700));
    Ok(path)
}

#[cfg(not(unix))]
pub fn verify(entry: &BuiltinEntry) -> Result<PathBuf, String> {
    let path = builtin_dir().join(entry.bin_name);
    std::fs::read(&path)
        .map(|_| path)
        .map_err(|e| format!("内置插件 {} 读取失败（{}）: {}", entry.id, path.display(), e))
}
