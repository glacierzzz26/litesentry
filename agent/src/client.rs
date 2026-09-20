//! tonic gRPC 客户端：向 Server 推送 MetricsBatch。
//!
//! 配齐 LS_TLS_CA/LS_TLS_CERT/LS_TLS_KEY 时走 https + 双向 TLS（客户端证书认证），
//! 否则明文 http 回退（开发联调）。token 走 metadata "authorization: Bearer <token>"。

use std::str::FromStr;
use std::time::Duration;

use anyhow::Result;
use tonic::metadata::MetadataValue;
use tonic::transport::{Channel, Certificate as TonicCertificate, ClientTlsConfig, Identity};
use tonic::Request;

use crate::config::TlsConfig;
use crate::pb::agent_client::AgentClient;
use crate::pb::{MetricsBatch, PluginRequest, PushAck, RegisterRequest};

/// 注册用的节点自身信息。
#[derive(Debug, Clone)]
pub struct RegisterInfo {
    pub hostname: String,
    pub machine_id: String,
    pub os: String,
    pub arch: String,
    pub kernel: String,
    pub version: String,
    /// 本机内置插件清单 (id, version, sha256)：随 agent 发布，由 build.rs 构建期生成。
    /// 上报后 Server 可据此做默认兜底指派并在插件页展示。
    pub builtins: Vec<(String, String, String)>,
}

pub struct Client {
    inner: AgentClient<Channel>,
}

impl Client {
    /// 建立连接。addr 形如 "192.168.1.10:9000"（IPv4）。
    /// tls 配了任意路径即启用 https + 双向 TLS；否则明文 http。
    pub async fn connect(addr: &str, tls: &TlsConfig) -> Result<Self> {
        let scheme = if tls.enabled() { "https" } else { "http" };
        let mut endpoint = Channel::from_shared(format!("{scheme}://{addr}"))?
            .timeout(Duration::from_secs(30))
            .connect_timeout(Duration::from_secs(10));
        if tls.enabled() {
            let mut tls_cfg = ClientTlsConfig::new();
            if !tls.ca_path.is_empty() {
                let ca = std::fs::read(&tls.ca_path)?;
                tls_cfg = tls_cfg.ca_certificate(TonicCertificate::from_pem(ca));
            }
            if !tls.cert_path.is_empty() && !tls.key_path.is_empty() {
                let cert = std::fs::read(&tls.cert_path)?;
                let key = std::fs::read(&tls.key_path)?;
                tls_cfg = tls_cfg.identity(Identity::from_pem(cert, key));
            }
            endpoint = endpoint.tls_config(tls_cfg)?;
        }
        let ch = endpoint.connect().await?;
        Ok(Self { inner: AgentClient::new(ch) })
    }

    /// 注册：用自身机器信息换取（或复用）Server 分发的 agent_id。
    pub async fn register(&mut self, token: &str, info: &RegisterInfo) -> Result<String> {
        let builtins = info
            .builtins
            .iter()
            .map(|(id, version, sha256)| crate::pb::BuiltinPlugin {
                plugin_id: id.clone(),
                version: version.clone(),
                sha256: sha256.clone(),
            })
            .collect();
        let mut req = Request::new(RegisterRequest {
            hostname: info.hostname.clone(),
            machine_id: info.machine_id.clone(),
            os: info.os.clone(),
            arch: info.arch.clone(),
            kernel: info.kernel.clone(),
            version: info.version.clone(),
            builtins,
        });
        req.metadata_mut()
            .insert("authorization", MetadataValue::from_str(&format!("Bearer {token}"))?);
        let reply = self.inner.register(req).await?.into_inner();
        Ok(reply.agent_id)
    }

    /// 推送一批指标。
    pub async fn push(&mut self, token: &str, batch: MetricsBatch) -> Result<PushAck> {
        let mut req = Request::new(batch);
        req.metadata_mut()
            .insert("authorization", MetadataValue::from_str(&format!("Bearer {token}"))?);
        Ok(self.inner.push(req).await?.into_inner())
    }

    /// 拉取插件二进制（流式分块），末尾分块携带完整 SHA-256，拼装后校验。
    pub async fn fetch_plugin(&mut self, token: &str, plugin_id: &str, version: &str) -> Result<Vec<u8>> {
        let mut req = Request::new(PluginRequest {
            plugin_id: plugin_id.to_string(),
            version: version.to_string(),
        });
        req.metadata_mut()
            .insert("authorization", MetadataValue::from_str(&format!("Bearer {token}"))?);
        let mut stream = self.inner.fetch_plugin(req).await?.into_inner();

        let mut data = Vec::new();
        let mut sha = String::new();
        let mut size: u64 = 0;
        while let Some(chunk) = stream.message().await? {
            if !chunk.error.is_empty() {
                anyhow::bail!("fetch plugin error: {}", chunk.error);
            }
            data.extend_from_slice(&chunk.data);
            if !chunk.sha256.is_empty() {
                sha = chunk.sha256;
            }
            if chunk.size > 0 {
                size = chunk.size;
            }
        }
        if data.is_empty() {
            anyhow::bail!("empty plugin binary");
        }
        if size > 0 && data.len() as u64 != size {
            anyhow::bail!("plugin size mismatch: got {}, want {}", data.len(), size);
        }
        if !sha.is_empty() {
            use sha2::{Digest, Sha256};
            let got = hex(&Sha256::digest(&data));
            if got != sha {
                anyhow::bail!("plugin sha256 mismatch: got {}, want {}", got, sha);
            }
        }
        Ok(data)
    }
}

/// bytes → 小写十六进制（sha2 摘要转 hex）。
pub(crate) fn hex(data: &[u8]) -> String {
    let mut s = String::with_capacity(data.len() * 2);
    for b in data {
        s.push_str(&format!("{b:02x}"));
    }
    s
}
