//! 从 proto 生成 tonic/prost 代码（仅生成 client，Server 端在 Go）
//!
//! proto 是唯一数据契约：agent（Rust）与 server（Go）共用 proto/litesentry.proto。

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let root = std::env::var("CARGO_MANIFEST_DIR")?;
    tonic_build::configure()
        .build_server(false)
        .build_client(true)
        .compile_protos(
            &[format!("{root}/../proto/litesentry.proto")],
            &[format!("{root}/../proto")],
        )?;
    Ok(())
}
