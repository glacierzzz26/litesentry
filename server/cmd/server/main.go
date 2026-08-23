// litesentry-server：Go + Gin 中心端。
//
// 监听两个端口：
//
//	-listen  gRPC 接收 Agent 上报（默认 :9000，明文 + token；配齐证书后启用 mTLS）
//	-web    前端 REST/SSE（默认 :8080）
//
// 鉴权：
//
//	gRPC Agent 走静态 token（-token / LITESENTRY_TOKEN）；Web 面板走 JWT 用户表
//	（-bootstrap-user/-bootstrap-pass 或 LITESENTRY_BOOTSTRAP_* 种子首个管理员，密码 bcrypt 落库）。
//
// 密钥优先级：命令行参数 > 环境变量。生产请用 -prod 强制安全基线。
//
// 存储：-db  sqlite:///path（默认）或 postgres://...
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"litesentry/server/internal/alert"
	"litesentry/server/internal/api"
	grpcsvc "litesentry/server/internal/grpc"
	"litesentry/server/internal/store"
	"litesentry/server/internal/webui"

	_ "litesentry/server/docs" // swag init 生成的 OpenAPI 文档（make docs）
)

//	@title          litesentry API
//	@version        0.1.0
//	@description    轻量主机 + 容器监控 Server REST 接口（前端面板取数）。Agent↔Server 走 gRPC，契约见 docs/proto.html。
//	@termsOfService http://localhost:8080/

//	@contact.name   litesentry
//	@contact.url    https://github.com/

//	@host           localhost:8080
//	@BasePath       /api

//	@securityDefinitions.apikey BearerAuth
//	@in                         header
//	@name                       Authorization
//	@description                Web 面板 JWT（POST /api/auth/login 获取），格式：Bearer <jwt>

func main() {
	var (
		listen    = flag.String("listen", ":9000", "gRPC 监听地址（Agent 上报）")
		web       = flag.String("web", ":8080", "Web 监听地址（前端 REST/SSE）")
		db        = flag.String("db", "sqlite:///var/lib/litesentry/litesentry.db", "存储 DSN")
		tokenFlag = flag.String("token", "", "Agent 共享 token（仅 gRPC Agent 上报鉴权；缺省读 LITESENTRY_TOKEN）")
		allow     = flag.String("allow-agents", "", "agent_id 白名单（逗号分隔，空=不校验）")
		retention = flag.Duration("retention", 30*24*time.Hour, "时序数据保留期")

		bootstrapUserFlag = flag.String("bootstrap-user", "", "首个管理员用户名（用户表为空时创建；缺省读 LITESENTRY_BOOTSTRAP_USER）")
		bootstrapPassFlag = flag.String("bootstrap-pass", "", "首个管理员密码（bcrypt 落库，首登强制改密；缺省读 LITESENTRY_BOOTSTRAP_PASS）")
		jwtSecretFlag     = flag.String("jwt-secret", "", "JWT 签名密钥（缺省读 LITESENTRY_JWT_SECRET；均未指定时开发态从 bootstrap-pass 确定性派生）")
		jwtTTL            = flag.Duration("jwt-ttl", 24*time.Hour, "JWT 有效期")

		grpcTLSCert = flag.String("grpc-tls-cert", "", "gRPC 服务端证书 PEM（与 key/ca 三者齐全时启用双向 TLS）")
		grpcTLSKey  = flag.String("grpc-tls-key", "", "gRPC 服务端私钥 PEM")
		grpcTLSCA   = flag.String("grpc-tls-ca", "", "Agent 证书签发 CA（双向 TLS 用其校验客户端证书）")

		prod = flag.Bool("prod", false, "生产模式：强制安全基线（显式 JWT 密钥 + gRPC mTLS + 非空 token + 禁止默认种子密码）")
	)
	flag.Parse()

	// ---- 密钥 env 回落：命令行显式 > 环境变量 ----
	token := firstNonEmpty(*tokenFlag, os.Getenv("LITESENTRY_TOKEN"))
	bootstrapUser := firstNonEmpty(*bootstrapUserFlag, os.Getenv("LITESENTRY_BOOTSTRAP_USER"))
	bootstrapPass := firstNonEmpty(*bootstrapPassFlag, os.Getenv("LITESENTRY_BOOTSTRAP_PASS"))
	jwtSecret := firstNonEmpty(*jwtSecretFlag, os.Getenv("LITESENTRY_JWT_SECRET"))

	st, err := store.Open(*db)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// ---- Web 账号种子（用户表为空时创建首个管理员）----
	seedCtx := context.Background()
	n, err := st.CountUsers(seedCtx)
	if err != nil {
		log.Fatalf("count users: %v", err)
	}
	if n == 0 && (bootstrapUser == "" || bootstrapPass == "") {
		log.Fatalf("用户表为空且未提供 -bootstrap-user/-bootstrap-pass（或对应环境变量），无法创建首个管理员账号")
	}

	// ---- -prod 安全基线校验（任何一项不满足即拒绝启动）----
	if *prod {
		var missing []string
		if jwtSecret == "" {
			missing = append(missing, "JWT 密钥未显式指定（需 -jwt-secret 或 LITESENTRY_JWT_SECRET）")
		}
		if *grpcTLSCert == "" || *grpcTLSKey == "" || *grpcTLSCA == "" {
			missing = append(missing, "gRPC 双向 TLS 证书未配齐（需 -grpc-tls-cert/-grpc-tls-key/-grpc-tls-ca）")
		}
		if token == "" {
			missing = append(missing, "Agent token 为空（需 -token 或 LITESENTRY_TOKEN）")
		}
		if len(missing) > 0 {
			log.Fatalf("生产模式（-prod）校验失败：\n  - %s", strings.Join(missing, "\n  - "))
		}
	}
	if n == 0 && bootstrapPass == "admin" {
		log.Fatalf("首次创建管理员禁止使用默认密码 admin，请通过 -bootstrap-pass 或 LITESENTRY_BOOTSTRAP_PASS 指定强密码")
	}
	if err := api.SeedAdmin(seedCtx, st, bootstrapUser, bootstrapPass); err != nil {
		log.Fatalf("seed admin: %v", err)
	}

	// JWT 密钥：未显式指定时从 bootstrap-pass 确定性派生（重启不失效，改密码即失效）。
	// 该派生仅服务开发联调；生产（-prod）已在上方强制显式指定。
	if jwtSecret == "" {
		sum := sha256.Sum256([]byte("litesentry-jwt:" + bootstrapPass))
		jwtSecret = hex.EncodeToString(sum[:])
	}

	var allowIDs []string
	if *allow != "" {
		allowIDs = strings.Split(*allow, ",")
	}

	// ---- gRPC（Agent 上报）----
	grpcLis, err := net.Listen("tcp4", *listen)
	if err != nil {
		log.Fatalf("listen gRPC %s: %v", *listen, err)
	}

	var gsrv *grpc.Server
	if *grpcTLSCert != "" || *grpcTLSKey != "" || *grpcTLSCA != "" {
		cfg, err := grpcsvc.ServerTLS(*grpcTLSCert, *grpcTLSKey, *grpcTLSCA)
		if err != nil {
			log.Fatalf("gRPC mTLS 配置失败: %v", err)
		}
		gsrv = grpc.NewServer(grpc.Creds(credentials.NewTLS(cfg)))
		log.Println("gRPC 已启用双向 TLS（mTLS），Agent 需持有 CA 签发的客户端证书")
	} else {
		gsrv = grpc.NewServer()
		log.Println("⚠️ gRPC 明文模式（未配置证书，仅限开发联调；生产请配 -grpc-tls-* 并加 -prod）")
	}
	grpcsvc.Register(gsrv, grpcsvc.New(st, token, allowIDs))
	go func() {
		log.Printf("gRPC listening on %s (Agent 上报)", *listen)
		if err := gsrv.Serve(grpcLis); err != nil {
			log.Fatalf("gRPC serve: %v", err)
		}
	}()

	// ---- Gin（前端 + Swagger 文档）----
	httpsrv := api.New(st, []byte(jwtSecret), *jwtTTL).Routes()
	// Swagger UI 无需 token（仅开发/内网暴露；如需保护可移除下面这行，用 BasicAuth 包一层）
	httpsrv.GET("/docs/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	if fsys, err := webui.FS(); err == nil {
		api.MountStatic(httpsrv, fsys)
		log.Printf("Web UI: 内嵌前端已挂载")
	} else {
		log.Printf("Web UI: 未嵌入前端（%v），仅提供 API 与 /docs", err)
	}
	go func() {
		log.Printf("Web listening on %s (前端)", *web)
		if err := httpsrv.Run(*web); err != nil {
			log.Fatalf("web serve: %v", err)
		}
	}()

	// ---- 告警评估引擎（60s 周期，规则/设置改动实时生效）----
	alertCtx, alertCancel := context.WithCancel(context.Background())
	go alert.New(st).Run(alertCtx, 60*time.Second)
	log.Println("告警引擎已启动（周期 60s）")

	// ---- 保留期清理 ----
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	go func() {
		for {
			select {
			case <-cleanupCtx.Done():
				return
			case <-time.After(time.Hour):
				n, err := st.Cleanup(cleanupCtx, *retention)
				if err != nil {
					log.Printf("cleanup: %v", err)
				} else if n > 0 {
					log.Printf("cleanup: 清理 %d 行", n)
				}
			}
		}
	}()

	// ---- 优雅退出 ----
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("shutting down...")
	alertCancel()
	cleanupCancel()
	gsrv.GracefulStop()
}

// firstNonEmpty 返回第一个非空值（参数显式优先于环境变量）。
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
