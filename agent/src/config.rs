//! 配置加载：命令行参数 + 环境变量，token 等敏感项只从 env / 0600 配置读。
//!
//! 优先级：命令行参数 > 环境变量 > 默认值。

use std::path::Path;

/// Agent 配置。
#[derive(Debug, Clone)]
pub struct Config {
    /// Server gRPC 地址（IPv4，通信走 IPv4）
    pub server: String,
    /// 共享 token（metadata authorization: "Bearer <token>"）
    pub token: String,
    /// 固定 agent_id（默认空 = 启动时向 Server 注册获取/复用）
    pub agent_id: String,
    /// agent_id 本地缓存路径（仅记录 Server 下发的 id，非权威）
    pub id_file: String,
    /// 上报间隔（秒），即心跳周期
    pub interval_secs: u64,
    /// 双向 TLS：CA 证书 PEM 路径（Agent 用它校验 Server 身份）
    pub tls_ca: String,
    /// 双向 TLS：Agent 客户端证书 PEM 路径
    pub tls_cert: String,
    /// 双向 TLS：Agent 客户端私钥 PEM 路径
    pub tls_key: String,
}

/// 可选的 mTLS 配置（三个文件配齐即启用双向 TLS，否则明文回退）。
#[derive(Debug, Clone, Default)]
pub struct TlsConfig {
    pub ca_path: String,
    pub cert_path: String,
    pub key_path: String,
}

impl TlsConfig {
    /// 任一 TLS 路径配置了就按启用处理（Server 侧同样要求三件套齐全）。
    pub fn enabled(&self) -> bool {
        !self.ca_path.is_empty() || !self.cert_path.is_empty() || !self.key_path.is_empty()
    }
}

impl Config {
    pub fn from_env() -> Self {
        let interval_secs: u64 = std::env::var("LS_INTERVAL")
            .ok()
            .and_then(|v| v.parse().ok())
            .unwrap_or(60);

        Config {
            server: std::env::var("LS_SERVER").unwrap_or_else(|_| "127.0.0.1:9000".into()),
            token: std::env::var("LS_TOKEN").unwrap_or_default(),
            agent_id: std::env::var("LS_AGENT_ID").unwrap_or_default(),
            id_file: std::env::var("LS_ID_FILE")
                .unwrap_or_else(|_| "/var/lib/litesentry/agent_id".into()),
            interval_secs,
            tls_ca: std::env::var("LS_TLS_CA").unwrap_or_default(),
            tls_cert: std::env::var("LS_TLS_CERT").unwrap_or_default(),
            tls_key: std::env::var("LS_TLS_KEY").unwrap_or_default(),
        }
    }

    /// 组装 mTLS 配置（供 Client::connect 使用）。
    pub fn tls(&self) -> TlsConfig {
        TlsConfig {
            ca_path: self.tls_ca.clone(),
            cert_path: self.tls_cert.clone(),
            key_path: self.tls_key.clone(),
        }
    }
}

/// 机器指纹：优先 /etc/machine-id（Linux 稳定不变），容器内缺省回退 hostname。
pub fn machine_id() -> String {
    if let Ok(s) = std::fs::read_to_string("/etc/machine-id") {
        let s = s.trim().to_string();
        if !s.is_empty() {
            return s;
        }
    }
    std::env::var("HOSTNAME").unwrap_or_else(|_| "unknown".into())
}

/// 缓存 Server 下发的 agent_id（0600），仅作本地记录/排障，不参与分配逻辑。
pub fn cache_agent_id(path: &str, id: &str) {
    if let Some(parent) = Path::new(path).parent() {
        let _ = std::fs::create_dir_all(parent);
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(parent, std::fs::Permissions::from_mode(0o755));
        }
    }
    if std::fs::write(path, id).is_ok() {
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600));
        }
    }
}
