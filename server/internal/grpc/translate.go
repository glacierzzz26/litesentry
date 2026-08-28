// 阶段二 S2：内置插件 Series → 现有表翻译。
//
// 采集已迁入随 agent 旁路发布的内置插件（host/docker/disk，always-on），其产出走
// `MetricsBatch.series` 字段（proto 契约冻结，不新增字段）。本文件把这些 series 按
// 内置契约翻译回 `host_metrics`/`container_metrics`/`disk_metrics` + agents 快照，
// 使前端面板与告警引擎（只读这三张表 + agents 表）零改动，面板数据与迁移前一致。
//
// 契约（改动需与 agent/plugins/{host,docker,disk} 同步）：
//
//	host.info      tags{hostname,os,arch,kernel} fields{uptime_s,load_1m,load_5m,cpu_pct,
//	               mem_total,mem_used,swap_total,swap_used,net_rx_bps,net_tx_bps,
//	               agent_cpu_pct,agent_mem_rss}
//	host.ip        tags{addr,family,iface,scope}（每地址一条，无 fields）
//	disk.usage     tags{mount,fs} fields{total,used}
//	container.info tags{container_id,name,image,state} fields{restarts,uptime_s,cpu_pct,
//	               mem_usage,mem_limit,net_rx_bps,net_tx_bps}
//
// 其余 series（外部插件如 hello.tick）保持通用，仅落 series 表。
package grpc

import (
	"time"

	litesentrypb "litesentry/server/gen"
	"litesentry/server/internal/store"
)

// translateSeries 将一批插件 series 翻译回实体集合。纯函数，便于单测。
// 返回：
//   - hs：host.info 快照（每批最多 1 条，后到覆盖）
//   - disks/conts：disk.usage / container.info 逐条
//   - agent：agents 快照（host.info 提供 hostname/os/arch/kernel，host.ip 提供地址），
//     无任何 host.* series 时为 nil
func translateSeries(agentID string, ts time.Time, series []*litesentrypb.Series) (hs *store.HostSample, disks []*store.DiskSample, conts []*store.ContainerSample, agent *store.Agent) {
	for _, sr := range series {
		switch sr.GetName() {
		case "host.info":
			tags := sr.GetTags()
			f := sr.GetFields()
			hs = &store.HostSample{
				AgentID:     agentID,
				Ts:          ts,
				Hostname:    tags["hostname"],
				OS:          tags["os"],
				Arch:        tags["arch"],
				Kernel:      tags["kernel"],
				UptimeS:     f64u64(f["uptime_s"]),
				Load1:       f["load_1m"],
				Load5:       f["load_5m"],
				CPUPct:      f["cpu_pct"],
				AgentCPUPct: f["agent_cpu_pct"],
				AgentMemRSS: f64u64(f["agent_mem_rss"]),
				MemTotal:    f64u64(f["mem_total"]),
				MemUsed:     f64u64(f["mem_used"]),
				SwapTotal:   f64u64(f["swap_total"]),
				SwapUsed:    f64u64(f["swap_used"]),
				NetRXBps:    f64u64(f["net_rx_bps"]),
				NetTXBps:    f64u64(f["net_tx_bps"]),
			}
			if agent == nil {
				agent = newAgentSnapshot(agentID, ts)
			}
			agent.Hostname = hs.Hostname
			agent.OS = hs.OS
			agent.Arch = hs.Arch
			agent.Kernel = hs.Kernel

		case "host.ip":
			tags := sr.GetTags()
			if agent == nil {
				agent = newAgentSnapshot(agentID, ts)
			}
			a := store.IPAddr{
				Family: tags["family"],
				Addr:   tags["addr"],
				Iface:  tags["iface"],
				Scope:  tags["scope"],
			}
			if a.Family == "ipv6" {
				agent.IPv6 = append(agent.IPv6, a)
			} else {
				agent.IPv4 = append(agent.IPv4, a)
			}

		case "disk.usage":
			tags := sr.GetTags()
			f := sr.GetFields()
			disks = append(disks, &store.DiskSample{
				AgentID: agentID,
				Ts:      ts,
				Mount:   tags["mount"],
				FS:      tags["fs"],
				Total:   f64u64(f["total"]),
				Used:    f64u64(f["used"]),
			})

		case "container.info":
			tags := sr.GetTags()
			f := sr.GetFields()
			conts = append(conts, &store.ContainerSample{
				AgentID:     agentID,
				Ts:          ts,
				ContainerID: tags["container_id"],
				Name:        tags["name"],
				Image:       tags["image"],
				State:       tags["state"],
				Restarts:    f64u32(f["restarts"]),
				UptimeS:     f64u64(f["uptime_s"]),
				CPUPct:      f["cpu_pct"],
				MemUsage:    f64u64(f["mem_usage"]),
				MemLimit:    f64u64(f["mem_limit"]),
				NetRXBps:    f64u64(f["net_rx_bps"]),
				NetTXBps:    f64u64(f["net_tx_bps"]),
			})
		}
	}
	return hs, disks, conts, agent
}

// newAgentSnapshot 构造 agents 快照骨架（地址/主机字段由各 series 填充）。
// CreatedAt 由各 store 实现保留原值（UpsertAgent 不覆盖已存在节点的 created_at）。
func newAgentSnapshot(agentID string, ts time.Time) *store.Agent {
	return &store.Agent{
		AgentID:   agentID,
		LastSeen:  ts,
		CreatedAt: ts,
	}
}

// f64u64 float64 → uint64（负值/异常补零，防恶意或边界数据）。
func f64u64(v float64) uint64 {
	if v <= 0 {
		return 0
	}
	return uint64(v)
}

// f64u32 float64 → uint32（负值补零、溢出封顶）。
func f64u32(v float64) uint32 {
	if v <= 0 {
		return 0
	}
	if v > float64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(v)
}
