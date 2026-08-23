// JWT 认证辅助：HS256 签发/解析、bcrypt 哈希/校验、登录失败计数锁。
// Web 面板登录后拿 JWT 访问 /api/*；gRPC Agent 仍用静态 token（本包不涉及）。

package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"litesentry/server/internal/store"
)

const jwtIssuer = "litesentry"

// ---- bcrypt ----

// hashPassword bcrypt 哈希（DefaultCost=10，生产常规成本）。
func hashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// checkPassword 校验密码（bcrypt 内部恒定时间比对）。
func checkPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// dummyHash 预置一份无主哈希：用户不存在时也做一次真实比对，
// 避免通过响应时差枚举用户名。
var dummyHash = func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("litesentry-dummy-timing"), bcrypt.DefaultCost)
	return h
}()

// ---- JWT ----

// jwtClaims JWT 载荷：sub=用户 id，name=用户名。
type jwtClaims struct {
	Name string `json:"name"`
	jwt.RegisteredClaims
}

// signJWT 签发 HS256 JWT，返回 token 与过期时间。
func signJWT(secret []byte, ttl time.Duration, u *store.User) (token string, expiresAt time.Time, err error) {
	now := time.Now().UTC()
	exp := now.Add(ttl)
	claims := jwtClaims{
		Name: u.Username,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    jwtIssuer,
			Subject:   u.ID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}
	t, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	return t, exp, err
}

// parseJWT 解析并校验签名/过期/签发者。
func parseJWT(secret []byte, raw string) (*jwtClaims, error) {
	claims := &jwtClaims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return secret, nil
	}, jwt.WithIssuer(jwtIssuer), jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// auth JWT 鉴权中间件：解析 Authorization: Bearer <jwt>。
// 校验通过后把用户 id / 用户名写入 gin context，供 handler 使用。
func (s *Server) auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
			if claims, err := parseJWT(s.jwtSecret, h[7:]); err == nil {
				c.Set("userID", claims.Subject)
				c.Set("username", claims.Name)
				c.Next()
				return
			}
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "未登录或登录已过期"})
	}
}

// SeedAdmin 用户表为空时创建首个管理员（bcrypt 落库，强制首登改密）；已有用户则原样返回，幂等。
// 供 main 启动时调用。
func SeedAdmin(ctx context.Context, st store.Store, username, password string) error {
	if username == "" || password == "" {
		return nil // 无种子参数：由 main 决定是否拒绝启动
	}
	n, err := st.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	u := &store.User{
		ID:            store.NewID(),
		Username:      strings.TrimSpace(username),
		Role:          "admin",
		PasswordHash:  hash,
		MustChangePwd: true,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	return st.CreateUser(ctx, u)
}

// currentUserID 返回当前登录用户 id（auth 中间件已校验）。
func currentUserID(c *gin.Context) string {
	id, _ := c.Get("userID")
	s, _ := id.(string)
	return s
}

// ---- 登录失败计数锁 ----

// loginFail 一次失败窗口记录。
type loginFail struct {
	WindowStart time.Time
	Count       int
}

// failLimiter 内存级登录防爆破：同一 key（IP+用户名）在窗口内连续失败达上限即锁定。
type failLimiter struct {
	mu    sync.Mutex
	limit int
	win   time.Duration
	items map[string]*loginFail
}

func newFailLimiter(limit int, win time.Duration) *failLimiter {
	return &failLimiter{limit: limit, win: win, items: make(map[string]*loginFail)}
}

// locked 返回该 key 是否仍处于锁定。
func (f *failLimiter) locked(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[key]
	if !ok {
		return false
	}
	if time.Since(it.WindowStart) > f.win {
		delete(f.items, key)
		return false
	}
	return it.Count >= f.limit
}

// fail 记录一次失败；返回是否刚达到锁定阈值。
func (f *failLimiter) fail(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	it, ok := f.items[key]
	if !ok || time.Since(it.WindowStart) > f.win {
		it = &loginFail{WindowStart: time.Now()}
		f.items[key] = it
	}
	it.Count++
	return it.Count >= f.limit
}

// reset 登录成功时清除计数。
func (f *failLimiter) reset(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items, key)
}
