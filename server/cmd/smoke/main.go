// cmd/smoke：端到端冒烟客户端。
// 模拟 Agent 行为：向 Server 推送一批主机 + 容器指标，验证
// gRPC 接收 → token 鉴权 → SQLite 落库 全链路。
//
// 明文：go run ./cmd/smoke -addr 127.0.0.1:9000 -token dev -id agent-smoke
// mTLS：go run ./cmd/smoke -addr <host>:9000 -token <token> -tls-ca certs/ca.crt \
//       -tls-cert certs/agent.crt -tls-key certs/agent.key
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"log"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"litesentry/server/gen"
)

func main() {
	var (
		addr  = flag.String("addr", "127.0.0.1:9000", "Server gRPC 地址")
		token = flag.String("token", "dev", "Bearer token")
		id    = flag.String("id", "agent-smoke", "agent_id")
		times = flag.Int("n", 3, "推送次数（每次间隔 1s，模拟心跳）")

		tlsCA   = flag.String("tls-ca", "", "CA 证书 PEM（指定即启用 TLS；配 cert/key 则为双向 mTLS）")
		tlsCert = flag.String("tls-cert", "", "客户端证书 PEM（mTLS）")
		tlsKey  = flag.String("tls-key", "", "客户端私钥 PEM（mTLS）")
	)
	flag.Parse()

	creds := insecure.NewCredentials()
	if *tlsCA != "" {
		pem, err := os.ReadFile(*tlsCA)
		if err != nil {
			log.Fatalf("read ca: %v", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			log.Fatalf("invalid ca pem: %s", *tlsCA)
		}
		cfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		if *tlsCert != "" && *tlsKey != "" {
			cert, err := tls.LoadX509KeyPair(*tlsCert, *tlsKey)
			if err != nil {
				log.Fatalf("load client cert: %v", err)
			}
			cfg.Certificates = []tls.Certificate{cert}
		}
		creds = credentials.NewTLS(cfg)
		log.Println("已启用 TLS（" + tlsMode(*tlsCert, *tlsKey) + "）")
	}

	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		log.Fatalf("dial %s: %v", *addr, err)
	}
	defer conn.Close()
	client := litesentrypb.NewAgentClient(conn)

	for i := 0; i < *times; i++ {
		batch := buildBatch(*id)
		ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+*token)
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ack, err := client.Push(ctx, batch)
		cancel()
		if err != nil {
			log.Fatalf("push #%d: %v", i+1, err)
		}
		log.Printf("push #%d OK: server_time=%s msg=%q", i+1, ack.ServerTime, ack.Message)
		time.Sleep(time.Second)
	}
	log.Println("smoke done")
}

func tlsMode(cert, key string) string {
	if cert != "" && key != "" {
		return "mTLS"
	}
	return "单向 TLS"
}

func buildBatch(id string) *litesentrypb.MetricsBatch {
	ts := uint64(time.Now().Unix())
	return &litesentrypb.MetricsBatch{
		AgentId: id,
		Ts:      ts,
		Host: &litesentrypb.HostMetrics{
			Hostname: "smoke-node",
			Os:       "linux",
			Arch:     "x86_64",
			Kernel:   "6.8.0",
			UptimeS:  3600,
			Load_1M:  0.42,
			Load_5M:  0.35,
			CpuPct:   12.5,
			Mem:      &litesentrypb.Mem{Total: 16 << 30, Used: 6 << 30},
			Swap:     &litesentrypb.Mem{Total: 2 << 30, Used: 1 << 30},
			Net:      &litesentrypb.Net{RxBps: 1_234_567, TxBps: 456_789},
			Disks: []*litesentrypb.Disk{
				{Mount: "/", Fs: "ext4", Total: 500 << 30, Used: 200 << 30},
				{Mount: "/data", Fs: "xfs", Total: 2 << 40, Used: 500 << 30},
			},
			Ips: []*litesentrypb.IPAddr{
				{Family: "ipv4", Addr: "192.168.1.10", Iface: "eth0", Scope: "global"},
				{Family: "ipv6", Addr: "2408:8207:xxxx::1", Iface: "eth0", Scope: "global"},
				{Family: "ipv6", Addr: "fe80::1", Iface: "eth0", Scope: "link"},
			},
		},
		Containers: []*litesentrypb.ContainerMetrics{
			{
				Id:       "abc123",
				Name:     "quant-engine",
				Image:    "nginx:alpine",
				State:    "running",
				Restarts: 2,
				UptimeS:  1800,
				CpuPct:   8.3,
				Mem:      &litesentrypb.ContainerMem{Usage: 256 << 20, Limit: 1 << 30},
				Net:      &litesentrypb.Net{RxBps: 88_888, TxBps: 12_345},
			},
			{
				Id:      "def456",
				Name:    "backtest",
				Image:   "redis:7",
				State:   "running",
				UptimeS: 900,
				CpuPct:  2.1,
				Mem:     &litesentrypb.ContainerMem{Usage: 64 << 20, Limit: 512 << 20},
				Net:     &litesentrypb.Net{RxBps: 3_333, TxBps: 7_777},
			},
		},
		Series: nil,
	}
}
