//! 构建脚本：① 从 proto 生成 tonic/prost 代码（仅生成 client，Server 端在 Go）；
//! ② 读取内置插件二进制（plugins/{host,docker,disk}）计算 SHA-256 + Cargo.toml 版本，
//!    生成 `OUT_DIR/builtin.rs` 内置清单（Agent 随本体发布并校验这些插件）。
//!
//! 插件由 `make agent-plugins` 先行构建；缺失时仅警告并跳过该条目（保证
//! `cargo check`/`cargo test` 单跑可用），运行期 Agent 会因清单缺条目而跳过对应内置插件。

use std::path::Path;

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let root = std::env::var("CARGO_MANIFEST_DIR")?;
    tonic_build::configure()
        .build_server(false)
        .build_client(true)
        .compile_protos(
            &[format!("{root}/../proto/litesentry.proto")],
            &[format!("{root}/../proto")],
        )?;

    // ---- 内置插件清单 ----
    let out_dir = std::env::var("OUT_DIR")?;
    let plugins: [(&str, &str); 3] = [("host", "host"), ("docker", "docker"), ("disk", "disk")];
    let mut entries: Vec<String> = Vec::new();
    for (id, bin) in plugins {
        let bin_path = Path::new(&root).join("plugins").join(id).join("target/release").join(bin);
        println!("cargo:rerun-if-changed={}", bin_path.display());
        let version = crate_version(&Path::new(&root).join("plugins").join(id).join("Cargo.toml"));
        match std::fs::read(&bin_path) {
            Ok(data) => {
                use sha2::Digest;
                let sum = sha2::Sha256::digest(&data);
                let hex: String = sum.iter().map(|b| format!("{b:02x}")).collect();
                entries.push(format!(
                    "BuiltinEntry {{ id: {id:?}, version: {version:?}, sha256: {hex:?}, bin_name: {bin:?} }}"
                ));
            }
            Err(e) => {
                eprintln!("warn: 内置插件 {id} 未构建（{}，{e}）——该插件将不随 agent 启用；先 `make agent-plugins`", bin_path.display());
            }
        }
    }
    let gen = format!(
        "// 由 build.rs 生成：内置插件清单（勿手改）。\npub struct BuiltinEntry {{\n  pub id: &'static str,\n  pub version: &'static str,\n  pub sha256: &'static str,\n  pub bin_name: &'static str,\n}}\n\npub const BUILTIN_PLUGINS: &[BuiltinEntry] = &[\n  {}\n];\n",
        entries.join(",\n  ")
    );
    std::fs::write(Path::new(&out_dir).join("builtin.rs"), gen)?;
    Ok(())
}

/// 读插件 crate 的 Cargo.toml 版本字段（`version = "x.y.z"`）。
fn crate_version(path: &Path) -> String {
    std::fs::read_to_string(path)
        .ok()
        .and_then(|s| {
            s.lines().find(|l| l.trim_start().starts_with("version = ")).map(|l| {
                l.split('=')
                    .nth(1)
                    .map(|v| v.trim().trim_matches('"').to_string())
                    .unwrap_or_else(|| "unknown".into())
            })
        })
        .unwrap_or_else(|| "unknown".into())
}
