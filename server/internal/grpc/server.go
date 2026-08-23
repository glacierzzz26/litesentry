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
	"io"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	litesentrypb "litesentry/server/gen"
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
	return &litesentrypb.PushAck{
		ServerTime: time.Now().UTC().Format(time.RFC3339),
		Message:    "ok",
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
		if err := stream.Send(&litesentrypb.PushAck{
			ServerTime: time.Now().UTC().Format(time.RFC3339),
			Message:    "ok",
		}); err != nil {
			return err
		}
	}
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

	if agent != nil {
		if err := s.store.UpsertAgent(ctx, agent); err != nil {
			return err
		}
	}
	return s.store.AppendBatch(ctx, hs, disks, containers)
}
