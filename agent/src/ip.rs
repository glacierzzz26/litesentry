//! IPv4 / IPv6 地址探测（硬需求，先落地）。
//!
//! 遍历本机所有网卡，上报全部 IPv4 地址与**全局 IPv6 地址**：
//! - IPv4：全部（含私网），回环标 `loopback`
//! - IPv6：排除回环 `::1` 与链路本地 `fe80::/10`，仅保留全局地址（可直连/ssh）
//! - 本机无全局 IPv6 时返回空列表（面板显示 N/A）
//!
//! 通信走 IPv4；这里收集的 IPv6 地址只作为数据上报，供面板展示/直连。

use std::net::IpAddr;

/// 单个地址条目（与 proto `IPAddr` 对应；纯逻辑结构，便于单测）
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AddrInfo {
    pub family: String, // "ipv4" | "ipv6"
    pub addr: String,
    pub iface: String,
    pub scope: String, // "global" | "link" | "loopback"
}

/// 采集本机全部 IPv4 + 全局 IPv6 地址。
pub fn collect_addrs() -> Vec<AddrInfo> {
    let Ok(interfaces) = get_if_addrs::get_if_addrs() else {
        return Vec::new();
    };
    let mut out = Vec::with_capacity(interfaces.len());
    for iface in interfaces {
        let ip = iface.addr.ip();
        let scope = classify_scope(ip);
        match ip {
            IpAddr::V4(_) => out.push(AddrInfo {
                family: "ipv4".into(),
                addr: ip.to_string(),
                iface: iface.name.clone(),
                scope,
            }),
            IpAddr::V6(_) if scope == "global" => out.push(AddrInfo {
                family: "ipv6".into(),
                addr: ip.to_string(),
                iface: iface.name.clone(),
                scope,
            }),
            _ => {} // IPv6 链路本地 / 回环不参与直连，跳过
        }
    }
    out
}

fn classify_scope(ip: IpAddr) -> String {
    if ip.is_loopback() {
        return "loopback".into();
    }
    match ip {
        IpAddr::V6(v6) => {
            // 链路本地 fe80::/10
            if v6.segments()[0] & 0xffc0 == 0xfe80 {
                "link".into()
            } else {
                "global".into()
            }
        }
        IpAddr::V4(_) => "global".into(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::net::{Ipv4Addr, Ipv6Addr};

    #[test]
    fn scope_classification() {
        let fe80 = IpAddr::V6(Ipv6Addr::new(0xfe80, 0, 0, 0, 0, 0, 0, 1));
        assert_eq!(classify_scope(fe80), "link");

        let loopback6 = IpAddr::V6(Ipv6Addr::LOCALHOST);
        assert_eq!(classify_scope(loopback6), "loopback");

        // 全局 IPv6（公网段）
        let global6 = IpAddr::V6(Ipv6Addr::new(0x2408, 0x8207, 0x1, 0, 0, 0, 0, 0x1));
        assert_eq!(classify_scope(global6), "global");

        let loopback4 = IpAddr::V4(Ipv4Addr::new(127, 0, 0, 1));
        assert_eq!(classify_scope(loopback4), "loopback");

        let private4 = IpAddr::V4(Ipv4Addr::new(192, 168, 1, 10));
        assert_eq!(classify_scope(private4), "global");
    }

    #[test]
    fn collect_runs_and_contains_loopback() {
        let addrs = collect_addrs();
        assert!(addrs.iter().any(|a| a.scope == "loopback"), "应至少包含回环地址");
        // 不应包含 IPv6 链路本地
        assert!(!addrs.iter().any(|a| a.family == "ipv6" && a.scope == "link"));
    }
}
