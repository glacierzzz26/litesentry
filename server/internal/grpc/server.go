// Package grpc 接收 Agent 上报（gRPC）。
//
// 阶段一：明文 + token（证书 TLS/mTLS 预留）。鉴权链：
//
//	token 校验（metadata authorization）→ agent_id 白名单 → 时间戳防重放。
package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"sort"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	litesentrypb "litesentry/server/gen"
	"litesentry/server/internal/frp"
	"litesentry/server/internal/store"
)

// Server 实现 litesentrypb.AgentServer。
type Server struct {
	litesentrypb.UnimplementedAgentServer

	store    store.Store
	token    string
	allowIDs map[string]struct{} // agent_id 白名单；空 = 不校验
}

func New(s store.Store, token string, allowIDs []string) *Server {
	m := make(map[string]struct{}, len(allowIDs))
	for _, id := range allowIDs {
		m[id] = struct{}{}
	}
	return &Server{store: s, token: token, allowIDs: m}
}

// Register 将 Agent 服务注册到 gRPC Server。
func Register(gs *grpc.Server, srv *Server) {
	litesentrypb.RegisterAgentServer(gs, srv)
}

// maxSkew 时间戳最大偏差（防重放）。
const maxSkew = 5 * time.Minute

func (s *Server) auth(ctx context.Context) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing metadata")
	}
	vals := md.Get("authorization")
	if len(vals) == 0 || vals[0] != "Bearer "+s.token {
		return status.Error(codes.Unauthenticated, "invalid token")
	}
	return nil
}

func (s *Server) validate(batch *litesentrypb.MetricsBatch) error {
	if batch.GetAgentId() == "" {
		return status.Error(codes.InvalidArgument, "empty agent_id")
	}
	if len(s.allowIDs) > 0 {
		if _, ok := s.allowIDs[batch.GetAgentId()]; !ok {
			return status.Error(codes.PermissionDenied, "unknown agent_id")
		}
	}
	ts := time.Unix(int64(batch.GetTs()), 0)
	if d := time.Since(ts); d < -maxSkew || d > maxSkew {
		return status.Error(codes.InvalidArgument, "timestamp out of range")
	}
	return nil
}

// Push 单批上报（默认 10s/批）。
func (s *Server) Push(ctx context.Context, batch *litesentrypb.MetricsBatch) (*litesentrypb.PushAck, error) {
	if err := s.auth(ctx); err != nil {
		return nil, err
	}
	if err := s.validate(batch); err != nil {
		return nil, err
	}
	if err := s.persist(ctx, batch); err != nil {
		return nil, status.Error(codes.Internal, "persist failed: "+err.Error())
	}
	ds, err := s.desiredState(ctx, batch.GetAgentId())
	if err != nil {
		return nil, status.Error(codes.Internal, "desired state failed: "+err.Error())
	}
	return &litesentrypb.PushAck{
		ServerTime:   time.Now().UTC().Format(time.RFC3339),
		Message:      "ok",
		DesiredState: ds,
	}, nil
}

// Register 节点注册：按机器指纹复用或分发 agent_id。
// 同一台机器重新上线（含重装 / 丢失本地缓存）也能复用历史 id。
func (s *Server) Register(ctx context.Context, req *litesentrypb.RegisterRequest) (*litesentrypb.RegisterReply, error) {
	if err := s.auth(ctx); err != nil {
		return nil, err
	}
	if req.GetMachineId() == "" {
		return nil, status.Error(codes.InvalidArgument, "empty machine_id")
	}
	now := time.Now().UTC()
	a := &store.Agent{
		Hostname:  req.GetHostname(),
		OS:        req.GetOs(),
		Arch:      req.GetArch(),
		Kernel:    req.GetKernel(),
		Version:   req.GetVersion(),
		MachineID: req.GetMachineId(),
		LastSeen:  now,
		CreatedAt: now,
	}
	id, _, err := s.store.RegisterAgent(ctx, req.GetMachineId(), a)
	if err != nil {
		return nil, status.Error(codes.Internal, "register failed: "+err.Error())
	}
	return &litesentrypb.RegisterReply{
		AgentId:    id,
		ServerTime: now.Format(time.RFC3339),
	}, nil
}

// Stream 长连接流式上报（预留，阶段二插件分发复用此通道）。
func (s *Server) Stream(stream litesentrypb.Agent_StreamServer) error {
	for {
		batch, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.auth(stream.Context()); err != nil {
			return err
		}
		if err := s.validate(batch); err != nil {
			return err
		}
		if err := s.persist(stream.Context(), batch); err != nil {
			return status.Error(codes.Internal, "persist failed: "+err.Error())
		}
		ds, err := s.desiredState(stream.Context(), batch.GetAgentId())
		if err != nil {
			return status.Error(codes.Internal, "desired state failed: "+err.Error())
		}
		if err := stream.Send(&litesentrypb.PushAck{
			ServerTime:   time.Now().UTC().Format(time.RFC3339),
			Message:      "ok",
			DesiredState: ds,
		}); err != nil {
			return err
		}
	}
}

// FetchPlugin 流式下发插件二进制（分块 + 末尾分块携带完整 SHA-256）。
// Agent 端按分块拼装，校验和一致才缓存执行；不匹配拒绝运行。
func (s *Server) FetchPlugin(req *litesentrypb.PluginRequest, stream litesentrypb.Agent_FetchPluginServer) error {
	if err := s.auth(stream.Context()); err != nil {
		return err
	}
	if req.GetPluginId() == "" || req.GetVersion() == "" {
		return status.Error(codes.InvalidArgument, "plugin_id/version required")
	}
	p, err := s.store.GetPlugin(stream.Context(), req.GetPluginId(), req.GetVersion())
	if err != nil {
		return status.Error(codes.NotFound, "plugin not found: "+err.Error())
	}
	if len(p.Data) == 0 {
		return status.Error(codes.Internal, "plugin has no binary content")
	}
	const chunkSize = 256 * 1024 // 256KB 分块
	for off := 0; off < len(p.Data); off += chunkSize {
		end := off + chunkSize
		if end > len(p.Data) {
			end = len(p.Data)
		}
		chunk := &litesentrypb.Chunk{
			Data: p.Data[off:end],
			Size: uint64(len(p.Data)),
		}
		if end == len(p.Data) {
			chunk.Sha256 = p.SHA256 // 末尾分块携带完整校验和
		}
		if err := stream.Send(chunk); err != nil {
			return err
		}
	}
	return nil
}

// ServerTLS 构建 gRPC 服务端 mTLS 配置：双向 TLS，Agent 必须持有本 CA 签发的客户端证书。
// 三个文件（cert/key/ca）必须齐全，否则返回错误由调用方决定是否回落明文。
func ServerTLS(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load server cert: %w", err)
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read ca: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("invalid ca pem: %s", caFile)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// persist 将 MetricsBatch 落库（agent 快照 + 主机/磁盘/容器时序，单事务）。
func (s *Server) persist(ctx context.Context, batch *litesentrypb.MetricsBatch) error {
	ts := time.Unix(int64(batch.GetTs()), 0).UTC()
	host := batch.GetHost()

	var hs *store.HostSample
	var disks []*store.DiskSample
	var containers []*store.ContainerSample
	var agent *store.Agent

	if host != nil {
		hs = &store.HostSample{
			AgentID:     batch.GetAgentId(),
			Ts:          ts,
			Hostname:    host.GetHostname(),
			OS:          host.GetOs(),
			Arch:        host.GetArch(),
			Kernel:      host.GetKernel(),
			UptimeS:     host.GetUptimeS(),
			Load1:       float64(host.GetLoad_1M()),
			Load5:       float64(host.GetLoad_5M()),
			CPUPct:      float64(host.GetCpuPct()),
			AgentCPUPct: float64(host.GetAgentCpuPct()),
			AgentMemRSS: host.GetAgentMemRss(),
			MemTotal:    host.GetMem().GetTotal(),
			MemUsed:     host.GetMem().GetUsed(),
			SwapTotal:   host.GetSwap().GetTotal(),
			SwapUsed:    host.GetSwap().GetUsed(),
			NetRXBps:    host.GetNet().GetRxBps(),
			NetTXBps:    host.GetNet().GetTxBps(),
		}

		// make 而非 nil：无地址时 JSON 也要输出 []，前端无需判空
		ipv4 := make([]store.IPAddr, 0)
		ipv6 := make([]store.IPAddr, 0)
		for _, ip := range host.GetIps() {
			a := store.IPAddr{
				Family: ip.GetFamily(),
				Addr:   ip.GetAddr(),
				Iface:  ip.GetIface(),
				Scope:  ip.GetScope(),
			}
			if a.Family == "ipv6" {
				ipv6 = append(ipv6, a)
			} else {
				ipv4 = append(ipv4, a)
			}
		}
		agent = &store.Agent{
			AgentID:   batch.GetAgentId(),
			Hostname:  host.GetHostname(),
			OS:        host.GetOs(),
			Arch:      host.GetArch(),
			Kernel:    host.GetKernel(),
			IPv4:      ipv4,
			IPv6:      ipv6,
			LastSeen:  ts,
			CreatedAt: ts, // 已存在时各实现保留原 created_at
		}

		for _, d := range host.GetDisks() {
			disks = append(disks, &store.DiskSample{
				AgentID: batch.GetAgentId(),
				Ts:      ts,
				Mount:   d.GetMount(),
				FS:      d.GetFs(),
				Total:   d.GetTotal(),
				Used:    d.GetUsed(),
			})
		}
	}

	for _, c := range batch.GetContainers() {
		containers = append(containers, &store.ContainerSample{
			AgentID:     batch.GetAgentId(),
			Ts:          ts,
			ContainerID: c.GetId(),
			Name:        c.GetName(),
			Image:       c.GetImage(),
			State:       c.GetState(),
			Restarts:    c.GetRestarts(),
			UptimeS:     c.GetUptimeS(),
			CPUPct:      float64(c.GetCpuPct()),
			MemUsage:    c.GetMem().GetUsage(),
			MemLimit:    c.GetMem().GetLimit(),
			NetRXBps:    c.GetNet().GetRxBps(),
			NetTXBps:    c.GetNet().GetTxBps(),
		})
	}

	// S2：内置插件 Series → 翻译回现有表（host.info / host.ip / disk.usage / container.info）。
	// 新 agent 只带 series（host 留空）、旧 agent 只带 proto 字段 —— 二者天然互斥；
	// 翻译结果并入上面的实体集合，UpsertAgent/AppendBatch 保持单一路径。
	if n := len(batch.GetSeries()); n > 0 {
		ths, tdisks, tconts, tagent := translateSeries(batch.GetAgentId(), ts, batch.GetSeries())
		if hs == nil {
			hs = ths
		}
		disks = append(disks, tdisks...)
		containers = append(containers, tconts...)
		if agent == nil {
			agent = tagent
		}
	}

	if agent != nil {
		if err := s.store.UpsertAgent(ctx, agent); err != nil {
			return err
		}
	} else {
		// 心跳与采集解耦：无 series 的空心跳（采集间隔 > 心跳间隔时出现）也须刷新
		// last_seen，否则节点会在两次采集之间被误判离线。
		if err := s.store.TouchAgentLastSeen(ctx, batch.GetAgentId(), ts); err != nil {
			return err
		}
	}
	if err := s.store.AppendBatch(ctx, hs, disks, containers); err != nil {
		return err
	}

	// 插件 Series（阶段二）：统一落 series 表，S2 再按 name 前缀翻译回现有表。
	if len(batch.GetSeries()) > 0 {
		items := make([]*store.Series, 0, len(batch.GetSeries()))
		for _, sr := range batch.GetSeries() {
			st := ts
			if sr.GetTs() > 0 {
				st = time.Unix(int64(sr.GetTs()), 0).UTC()
			}
			items = append(items, &store.Series{
				Name:   sr.GetName(),
				Tags:   sr.GetTags(),
				Fields: sr.GetFields(),
				TS:     st,
			})
		}
		if err := s.store.AppendSeries(ctx, batch.GetAgentId(), items); err != nil {
			return err
		}
	}

	// frp 状态（阶段二）：agent 上报进程 + 隧道状态，覆盖写最近一次。
	if fs := batch.GetFrp(); fs != nil {
		sts := &store.FrpStatus{
			Running:    fs.GetRunning(),
			FrpVersion: fs.GetFrpVersion(),
			Error:      fs.GetError(),
			TS:         ts,
		}
		for _, t := range fs.GetTunnels() {
			sts.Tunnels = append(sts.Tunnels, store.FrpTunnelStatus{
				Name:    t.GetName(),
				Type:    t.GetType(),
				Status:  t.GetStatus(),
				Err:     t.GetErr(),
				RXBytes: t.GetRxBytes(),
				TXBytes: t.GetTxBytes(),
			})
		}
		if err := s.store.UpsertFrpStatus(ctx, batch.GetAgentId(), sts); err != nil {
			return err
		}
	}

	// 定时任务执行结果（阶段二）：落审计 + 回写任务最近一次摘要。
	for _, tr := range batch.GetTaskRuns() {
		started := time.Unix(int64(tr.GetStartedAt()), 0).UTC()
		if tr.GetStartedAt() == 0 {
			started = ts
		}
		var finished *time.Time
		if tr.GetFinishedAt() > 0 {
			f := time.Unix(int64(tr.GetFinishedAt()), 0).UTC()
			finished = &f
		}
		code := tr.GetExitCode()
		if err := s.store.AppendTaskRun(ctx, &store.TaskRun{
			TaskID:     tr.GetTaskId(),
			AgentID:    batch.GetAgentId(),
			Status:     tr.GetStatus(),
			ExitCode:   &code,
			Output:     tr.GetOutput(),
			StartedAt:  started,
			FinishedAt: finished,
		}); err != nil {
			return err
		}
		if err := s.store.UpdateTaskLastRun(ctx, tr.GetTaskId(), tr.GetStatus(), tr.GetOutput(), started); err != nil {
			return err
		}
		// 立即运行触发已消费：无论结果如何，清除 run_now 标记（让 DesiredState 哈希回落，agent 恢复纯 cron 调度）。
		if err := s.store.SetTaskRunNow(ctx, tr.GetTaskId(), false); err != nil {
			return err
		}
	}
	return nil
}

// desiredState 构造某 agent 的下发期望状态：插件 manifest + frp 配置 + 定时任务。
// state_version = 内容哈希（FNV-1a 64bit）：内容不变则版本不变（agent 跳过重应用），任一变更则变化。
func (s *Server) desiredState(ctx context.Context, agentID string) (*litesentrypb.DesiredState, error) {
	ds := &litesentrypb.DesiredState{}

	// frp 配置（S3）：节点自身 frpc 优先；公网机（server 同机 agent）兜底 frps。
	// 渲染的 toml 经 state_version 内容哈希联动 —— 配置变更自动触发 agent 重应用（含停进程）。
	if cfg, err := s.store.GetFrpConfig(ctx, "frpc", agentID); err == nil && cfg.Enabled {
		_, toml := frp.Render(cfg)
		ds.FrpEnabled = true
		ds.FrpToml = toml
	} else if cfg, err := s.store.GetFrpConfig(ctx, "frps", ""); err == nil && cfg.Enabled {
		serverID, _ := s.store.GetSetting(ctx, "server_agent_id")
		if serverID == agentID {
			_, toml := frp.Render(cfg)
			ds.FrpEnabled = true
			ds.FrpToml = toml
		}
	}

	aps, err := s.store.AgentPlugins(ctx, agentID)
	if err != nil {
		return nil, err
	}
	for _, ap := range aps {
		ds.Plugins = append(ds.Plugins, &litesentrypb.PluginSpec{
			PluginId: ap.PluginID,
			Version:  ap.Version,
			ArgsJson: ap.ArgsJSON,
		})
	}

	tasks, err := s.store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	serverID := ""
	for _, t := range tasks {
		if !t.Enabled {
			continue
		}
		switch t.TargetAgentID {
		case "":
			// 公网机（server 同机 agent）：由设置项 server_agent_id 声明，未声明则无下发对象
			if serverID == "" {
				serverID, _ = s.store.GetSetting(ctx, "server_agent_id")
			}
			if serverID != agentID {
				continue
			}
		case agentID:
		default:
			continue
		}
		ds.Tasks = append(ds.Tasks, &litesentrypb.TaskSpec{
			TaskId:   t.ID,
			Cron:     t.Cron,
			PluginId: t.PluginID,
			ArgsJson: t.ArgsJSON,
			TimeoutS: t.TimeoutS,
			RunNow:   t.RunNow,
		})
	}

	ds.StateVersion = desiredStateVersion(ds)
	return ds, nil
}

// desiredStateVersion 计算 DesiredState 的内容哈希（FNV-1a 64bit）。
// 插件 / 任务先按 id 排序，保证顺序无关；任何字段变更都会改变版本号。
func desiredStateVersion(ds *litesentrypb.DesiredState) uint64 {
	h := fnv.New64a()
	sep := func(s string) {
		_, _ = io.WriteString(h, s)
		_, _ = h.Write([]byte{0})
	}
	if ds.GetFrpEnabled() {
		sep("frp1")
	} else {
		sep("frp0")
	}
	sep(ds.GetFrpToml())

	sort.Slice(ds.Plugins, func(i, j int) bool { return ds.Plugins[i].GetPluginId() < ds.Plugins[j].GetPluginId() })
	for _, p := range ds.Plugins {
		sep(p.GetPluginId())
		sep(p.GetVersion())
		sep(p.GetArgsJson())
	}
	sort.Slice(ds.Tasks, func(i, j int) bool { return ds.Tasks[i].GetTaskId() < ds.Tasks[j].GetTaskId() })
	for _, t := range ds.Tasks {
		sep(t.GetTaskId())
		sep(t.GetCron())
		sep(t.GetPluginId())
		sep(t.GetArgsJson())
		sep(strconv.FormatUint(uint64(t.GetTimeoutS()), 10))
		sep(strconv.FormatBool(t.GetRunNow())) // run_now 翻转 → 哈希变 → agent 重应用（触发/回落）
	}
	return h.Sum64()
}
