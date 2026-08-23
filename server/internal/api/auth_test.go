// 认证单元测试：bcrypt / JWT / failLimiter 纯函数层 + login / changePassword HTTP 层
// （store 用 sqlite://:memory:，modernc 驱动无 cgo）。
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"litesentry/server/internal/store"
)

// ---- HTTP 层 ----

func newTestServer(t *testing.T) (*Server, *gin.Engine, store.Store) {
	t.Helper()
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(st, []byte("test-secret"), time.Hour)
	return s, s.Routes(), st
}

func postJSON(t *testing.T, r *gin.Engine, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestLoginSuccessAndWrongPassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, r, st := newTestServer(t)
	ctx := context.Background()
	if err := SeedAdmin(ctx, st, "admin", "strong-pass-123"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := postJSON(t, r, "/api/auth/login", "", map[string]string{"username": "admin", "password": "strong-pass-123"})
	if w.Code != http.StatusOK {
		t.Fatalf("正确密码登录 code=%d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp loginResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Token == "" {
		t.Fatal("登录成功但无 token")
	}
	if resp.User.Username != "admin" {
		t.Errorf("user = %q, want admin", resp.User.Username)
	}
	if !resp.MustChangePassword {
		t.Errorf("种子用户首登应强制改密")
	}

	w = postJSON(t, r, "/api/auth/login", "", map[string]string{"username": "admin", "password": "wrong"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("错误密码 code=%d, want 401", w.Code)
	}
}

func TestLoginLockout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, r, st := newTestServer(t)
	ctx := context.Background()
	if err := SeedAdmin(ctx, st, "admin", "strong-pass-123"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	body := map[string]string{"username": "admin", "password": "wrong"}

	for i := 0; i < 5; i++ { // 连续 5 次失败
		if w := postJSON(t, r, "/api/auth/login", "", body); w.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败 code=%d, want 401", i+1, w.Code)
		}
	}
	// 第 6 次：锁定（即使密码正确也 429）
	if w := postJSON(t, r, "/api/auth/login", "", map[string]string{"username": "admin", "password": "strong-pass-123"}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("锁定后正确密码 code=%d, want 429", w.Code)
	}
}

func TestChangePassword(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, r, st := newTestServer(t)
	ctx := context.Background()
	if err := SeedAdmin(ctx, st, "admin", "strong-pass-123"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	login := postJSON(t, r, "/api/auth/login", "", map[string]string{"username": "admin", "password": "strong-pass-123"})
	var resp loginResp
	_ = json.Unmarshal(login.Body.Bytes(), &resp)
	token := resp.Token
	if token == "" {
		t.Fatal("登录失败，无 token")
	}

	// 新密码过短 → 400
	if w := postJSON(t, r, "/api/auth/change-password", token, map[string]string{"old_password": "strong-pass-123", "new_password": "short"}); w.Code != http.StatusBadRequest {
		t.Fatalf("短密码 code=%d, want 400", w.Code)
	}
	// 旧密码错误 → 400
	if w := postJSON(t, r, "/api/auth/change-password", token, map[string]string{"old_password": "nope", "new_password": "brand-new-pass-456"}); w.Code != http.StatusBadRequest {
		t.Fatalf("旧密码错 code=%d, want 400", w.Code)
	}
	// 成功 → 200，清除强制改密
	if w := postJSON(t, r, "/api/auth/change-password", token, map[string]string{"old_password": "strong-pass-123", "new_password": "brand-new-pass-456"}); w.Code != http.StatusOK {
		t.Fatalf("改密 code=%d, want 200", w.Code)
	}
	u, err := st.GetUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if u.MustChangePwd {
		t.Errorf("改密后仍强制改密")
	}
	// 新密码可登录
	if w := postJSON(t, r, "/api/auth/login", "", map[string]string{"username": "admin", "password": "brand-new-pass-456"}); w.Code != http.StatusOK {
		t.Fatalf("新密码登录 code=%d, want 200", w.Code)
	}
	// 未带 token 访问受保护接口 → 401
	if w := postJSON(t, r, "/api/auth/change-password", "", map[string]string{"old_password": "x", "new_password": "yyyyyyyy"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("无 token code=%d, want 401", w.Code)
	}
	// 错误 token → 401
	if w := postJSON(t, r, "/api/auth/change-password", "bad-token", map[string]string{"old_password": "x", "new_password": "yyyyyyyy"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("坏 token code=%d, want 401", w.Code)
	}
	_ = s // 保留：避免误删 Server 引用
}

// ---- 纯函数层 ----

func TestBcrypt(t *testing.T) {
	h, err := hashPassword("abc12345")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if h == "abc12345" {
		t.Error("bcrypt 不应返回明文")
	}
	if !checkPassword(h, "abc12345") {
		t.Error("正确密码应匹配")
	}
	if checkPassword(h, "abc12344") {
		t.Error("错误密码不应匹配")
	}
}

func TestJWTSignParse(t *testing.T) {
	secret := []byte("s3cret-key")
	u := &store.User{ID: "u1", Username: "alice"}
	token, exp, err := signJWT(secret, time.Hour, u)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !exp.After(time.Now()) {
		t.Error("过期时间应在未来")
	}
	claims, err := parseJWT(secret, token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.Subject != "u1" || claims.Name != "alice" {
		t.Errorf("claims = %+v", claims)
	}

	// 错误密钥 → 拒绝
	if _, err := parseJWT([]byte("wrong"), token); err == nil {
		t.Error("错误密钥应解析失败")
	}
	// 过期 token → 拒绝
	expired, _, err := signJWT(secret, -time.Hour, u)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseJWT(secret, expired); err == nil {
		t.Error("过期 token 应解析失败")
	}
	// 错误签发者 → 拒绝
	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, jwtClaims{
		Name: "alice",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "evil",
			Subject:   "u1",
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	tok, _ := forged.SignedString(secret)
	if _, err := parseJWT(secret, tok); err == nil {
		t.Error("错误签发者应解析失败")
	}
}

func TestFailLimiter(t *testing.T) {
	f := newFailLimiter(3, time.Hour)
	key := "ip|user"
	for i := 0; i < 3; i++ {
		f.fail(key)
	}
	if !f.locked(key) {
		t.Fatal("连续 3 次失败后应锁定")
	}
	f.reset(key)
	if f.locked(key) {
		t.Fatal("重置后不应锁定")
	}

	// 窗口过期自动解锁
	f2 := newFailLimiter(2, 20*time.Millisecond)
	f2.fail(key)
	f2.fail(key)
	if !f2.locked(key) {
		t.Fatal("窗口内应锁定")
	}
	time.Sleep(40 * time.Millisecond)
	if f2.locked(key) {
		t.Fatal("窗口过期应解锁")
	}
}

func TestSeedAdminIdempotent(t *testing.T) {
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	if err := SeedAdmin(ctx, st, "admin", "pw-one"); err != nil {
		t.Fatalf("seed1: %v", err)
	}
	n, _ := st.CountUsers(ctx)
	if n != 1 {
		t.Fatalf("seed 后用户数 = %d, want 1", n)
	}
	// 再次种子（不同用户名）不新增
	if err := SeedAdmin(ctx, st, "other", "pw-two"); err != nil {
		t.Fatalf("seed2: %v", err)
	}
	n, _ = st.CountUsers(ctx)
	if n != 1 {
		t.Errorf("SeedAdmin 应幂等, 用户数 = %d", n)
	}
}
