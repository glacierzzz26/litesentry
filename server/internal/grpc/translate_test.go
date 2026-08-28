package grpc

import (
	"testing"
	"time"

	litesentrypb "litesentry/server/gen"
)

// 构造一条插件 series（helper）。
func mkSeries(name string, tags map[string]string, fields map[string]float64) *litesentrypb.Series {
	return &litesentrypb.Series{Name: name, Tags: tags, Fields: fields}
}

// TestTranslateSeries 验证内置插件 series → 现有表实体翻译（阶段二 S2）。
// 喂 host.info + host.ip×2 + disk.usage×2 + container.info×2 + hello.tick：
//   - host.info → HostSample 各字段 + agent 快照 hostname/os/arch/kernel
//   - host.ip   → agent 快照 IPv4/IPv6
//   - disk.usage / container.info → 逐条 DiskSample/ContainerSample
//   - hello.tick（外部插件）→ 保持通用，不进翻译产物
func TestTranslateSeries(t *testing.T) {
	ts := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	agentID := "a-111"

	series := []*litesentrypb.Series{
		mkSeries("host.info", map[string]string{
			"hostname": "node1", "os": "Debian", "arch": "x86_64", "kernel": "6.1.0",
		}, map[string]float64{
			"uptime_s": 3600, "load_1m": 0.5, "load_5m": 0.4, "cpu_pct": 12.3,
			"mem_total": 16e9, "mem_used": 8e9, "swap_total": 2e9, "swap_used": 0,
			"net_rx_bps": 1024, "net_tx_bps": 512, "agent_cpu_pct": 0.1, "agent_mem_rss": 5_000_000,
		}),
		mkSeries("host.ip", map[string]string{"addr": "192.168.1.5", "family": "ipv4", "iface": "eth0", "scope": "global"}, nil),
		mkSeries("host.ip", map[string]string{"addr": "fe80::1", "family": "ipv6", "iface": "eth0", "scope": "link"}, nil),
		mkSeries("disk.usage", map[string]string{"mount": "/", "fs": "ext4"}, map[string]float64{"total": 1e11, "used": 5e10}),
		mkSeries("disk.usage", map[string]string{"mount": "/data", "fs": "xfs"}, map[string]float64{"total": 2e11, "used": 1e11}),
		mkSeries("container.info", map[string]string{
			"container_id": "c1", "name": "web", "image": "nginx:alpine", "state": "running",
		}, map[string]float64{
			"restarts": 2, "uptime_s": 600, "cpu_pct": 3.5,
			"mem_usage": 1e8, "mem_limit": 5e8, "net_rx_bps": 100, "net_tx_bps": 200,
		}),
		mkSeries("container.info", map[string]string{
			"container_id": "c2", "name": "db", "image": "postgres:16", "state": "exited",
		}, map[string]float64{
			"restarts": 0, "uptime_s": 0, "cpu_pct": 0,
			"mem_usage": 0, "mem_limit": 0, "net_rx_bps": 0, "net_tx_bps": 0,
		}),
		mkSeries("hello.tick", map[string]string{"seq": "7"}, map[string]float64{"v": 1}),
	}

	hs, disks, conts, agent := translateSeries(agentID, ts, series)

	// host.info → HostSample
	if hs == nil {
		t.Fatal("host.info 应产出 HostSample，实际 nil")
	}
	if hs.AgentID != agentID || hs.Hostname != "node1" || hs.OS != "Debian" || hs.Arch != "x86_64" || hs.Kernel != "6.1.0" {
		t.Errorf("host 快照不符: %+v", hs)
	}
	if hs.UptimeS != 3600 || hs.Load1 != 0.5 || hs.Load5 != 0.4 || hs.CPUPct != 12.3 {
		t.Errorf("host 数值字段不符: %+v", hs)
	}
	if hs.MemTotal != 16e9 || hs.MemUsed != 8e9 || hs.SwapTotal != 2e9 || hs.SwapUsed != 0 {
		t.Errorf("内存字段不符: %+v", hs)
	}
	if hs.NetRXBps != 1024 || hs.NetTXBps != 512 || hs.AgentCPUPct != 0.1 || hs.AgentMemRSS != 5_000_000 {
		t.Errorf("网络/Agent 自耗字段不符: %+v", hs)
	}
	if !hs.Ts.Equal(ts) {
		t.Errorf("ts 应为 batch ts %v，实得 %v", ts, hs.Ts)
	}

	// agent 快照：hostname/os/arch/kernel + IPv4/IPv6
	if agent == nil {
		t.Fatal("host.* series 应产出 agent 快照，实际 nil")
	}
	if agent.Hostname != "node1" || agent.OS != "Debian" || agent.Arch != "x86_64" || agent.Kernel != "6.1.0" {
		t.Errorf("agent 快照主机字段不符: %+v", agent)
	}
	if len(agent.IPv4) != 1 || agent.IPv4[0].Addr != "192.168.1.5" || agent.IPv4[0].Iface != "eth0" {
		t.Errorf("IPv4 快照不符: %+v", agent.IPv4)
	}
	if len(agent.IPv6) != 1 || agent.IPv6[0].Addr != "fe80::1" || agent.IPv6[0].Family != "ipv6" {
		t.Errorf("IPv6 快照不符: %+v", agent.IPv6)
	}
	if !agent.LastSeen.Equal(ts) {
		t.Errorf("LastSeen 应为 %v，实得 %v", ts, agent.LastSeen)
	}

	// disk.usage → 2 条 DiskSample
	if len(disks) != 2 {
		t.Fatalf("应翻译 2 条 DiskSample，实得 %d", len(disks))
	}
	if disks[0].Mount != "/" || disks[0].FS != "ext4" || disks[0].Total != 1e11 || disks[0].Used != 5e10 {
		t.Errorf("磁盘[0] 不符: %+v", disks[0])
	}
	if disks[1].Mount != "/data" || disks[1].FS != "xfs" {
		t.Errorf("磁盘[1] 不符: %+v", disks[1])
	}

	// container.info → 2 条 ContainerSample
	if len(conts) != 2 {
		t.Fatalf("应翻译 2 条 ContainerSample，实得 %d", len(conts))
	}
	c0 := conts[0]
	if c0.ContainerID != "c1" || c0.Name != "web" || c0.Image != "nginx:alpine" || c0.State != "running" {
		t.Errorf("容器[0] 标签不符: %+v", c0)
	}
	if c0.Restarts != 2 || c0.UptimeS != 600 || c0.CPUPct != 3.5 || c0.MemUsage != 1e8 || c0.MemLimit != 5e8 || c0.NetRXBps != 100 || c0.NetTXBps != 200 {
		t.Errorf("容器[0] 字段不符: %+v", c0)
	}
	if c1 := conts[1]; c1.ContainerID != "c2" || c1.State != "exited" || c1.Restarts != 0 {
		t.Errorf("容器[1] 不符: %+v", c1)
	}
}

// TestTranslateSeriesOnlyExternal 验证纯外部插件 series：不产出任何实体（hs/disks/conts/agent 均 nil）。
func TestTranslateSeriesOnlyExternal(t *testing.T) {
	series := []*litesentrypb.Series{
		mkSeries("hello.tick", map[string]string{"seq": "1"}, map[string]float64{"v": 1}),
	}
	hs, disks, conts, agent := translateSeries("a-1", time.Now(), series)
	if hs != nil || disks != nil || conts != nil || agent != nil {
		t.Fatalf("外部插件 series 不应被翻译: hs=%v disks=%v conts=%v agent=%v", hs, disks, conts, agent)
	}
}

// TestTranslateSeriesBoundary 边界数据（负值/缺字段）不 panic，补零而非溢出。
func TestTranslateSeriesBoundary(t *testing.T) {
	ts := time.Now()
	series := []*litesentrypb.Series{
		mkSeries("host.info", nil, map[string]float64{
			"uptime_s": -5, "mem_total": -1, "load_1m": 1e18, "cpu_pct": -0.5,
		}),
		mkSeries("disk.usage", nil, nil),
		mkSeries("container.info", map[string]string{"container_id": "x"}, map[string]float64{
			"restarts": -1, "uptime_s": 5e11, "mem_usage": -9,
		}),
		mkSeries("host.ip", map[string]string{"family": "ipv6", "addr": "::1"}, nil),
	}
	hs, disks, conts, agent := translateSeries("a-1", ts, series)
	if hs.UptimeS != 0 || hs.MemTotal != 0 {
		t.Errorf("负值应补零: %+v", hs)
	}
	if hs.Load1 == 0 {
		t.Errorf("正值 1e18 不应被截断为 0: %+v", hs)
	}
	if disks[0].Mount != "" || disks[0].Total != 0 {
		t.Errorf("缺字段 disk 应补零/空: %+v", disks[0])
	}
	if conts[0].Restarts != 0 || conts[0].UptimeS != 5e11 {
		t.Errorf("restarts 负值补零、uptime 大值保留: %+v", conts[0])
	}
	if agent == nil || len(agent.IPv6) != 1 || agent.IPv6[0].Addr != "::1" {
		t.Errorf("host.ip 仍应产出 agent 快照 IPv6: %+v", agent)
	}
}
