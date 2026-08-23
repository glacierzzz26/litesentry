// 认证与用户管理 handlers：登录换 JWT、改密、用户 CRUD。
// 密码一律 bcrypt 存库、绝不回传；新用户/重置密码后 must_change_password=1 强制首登改密。

package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"litesentry/server/internal/store"
)

// ---- 登录 / 改密 ----

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResp struct {
	Token             string    `json:"token"`
	ExpiresAt         time.Time `json:"expires_at"`
	MustChangePassword bool      `json:"must_change_password"`
	User              store.User `json:"user"`
}

// login 用户名密码换 JWT。
// @Summary     登录
// @Description 校验用户名密码，成功签发 JWT 并回写上次登录时间；新用户/被重置密码的用户返回 must_change_password=true
// @Tags        认证
// @Accept      json
// @Produce     json
// @Param       body body api.loginReq true "用户名密码"
// @Success     200 {object} api.loginResp
// @Failure     400 {object} map[string]string "参数缺失"
// @Failure     401 {object} map[string]string "凭据错误"
// @Failure     429 {object} map[string]string "失败次数过多已锁定"
// @Router      /auth/login [post]
func (s *Server) login(c *gin.Context) {
	var req loginReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Username) == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入用户名和密码"})
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	key := c.ClientIP() + "|" + strings.ToLower(req.Username)
	if s.loginFails.locked(key) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "登录失败次数过多，请 15 分钟后再试"})
		return
	}

	ctx := c.Request.Context()
	u, err := s.st.GetUserByUsername(ctx, req.Username)
	if err != nil || !checkPassword(u.PasswordHash, req.Password) {
		// 用户不存在也做一次真实 bcrypt 比对，消除用户名枚举的时序差异
		_ = checkPassword(string(dummyHash), req.Password)
		s.loginFails.fail(key)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}
	s.loginFails.reset(key)

	now := time.Now().UTC()
	u.LastLoginAt = &now
	if err := s.st.SetLastLogin(ctx, u.ID, now); err != nil {
		// 回写失败不阻断登录
	}
	token, exp, err := signJWT(s.jwtSecret, s.jwtTTL, u)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "签发令牌失败"})
		return
	}
	c.JSON(http.StatusOK, loginResp{
		Token:              token,
		ExpiresAt:          exp,
		MustChangePassword: u.MustChangePwd,
		User:               *u,
	})
}

type changePwdReq struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// changePassword 修改当前用户密码（首次登录强制改密时调用）。
// @Summary     修改密码
// @Description 校验旧密码，写入新密码并清除 must_change_password
// @Tags        认证
// @Accept      json
// @Produce     json
// @Param       body body api.changePwdReq true "旧/新密码"
// @Success     200 {object} map[string]string
// @Failure     400 {object} map[string]string "旧密码错误 / 新密码过短"
// @Security    BearerAuth
// @Router      /auth/change-password [post]
func (s *Server) changePassword(c *gin.Context) {
	var req changePwdReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体格式错误"})
		return
	}
	if len(req.NewPassword) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "新密码至少 8 位"})
		return
	}
	ctx := c.Request.Context()
	u, err := s.st.GetUserByID(ctx, currentUserID(c))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户不存在"})
		return
	}
	if !checkPassword(u.PasswordHash, req.OldPassword) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "旧密码错误"})
		return
	}
	hash, err := hashPassword(req.NewPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码哈希失败"})
		return
	}
	u.PasswordHash = hash
	u.MustChangePwd = false
	if err := s.st.UpdateUser(ctx, u); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// me 当前登录用户信息。
// @Summary     当前用户
// @Tags        认证
// @Produce     json
// @Success     200 {object} store.User
// @Security    BearerAuth
// @Router      /auth/me [get]
func (s *Server) me(c *gin.Context) {
	u, err := s.st.GetUserByID(c.Request.Context(), currentUserID(c))
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户不存在"})
		return
	}
	c.JSON(http.StatusOK, u)
}

// ---- 用户管理 ----

// listUsers 用户列表（JSON 不含密码哈希）。
// @Summary     用户列表
// @Tags        用户
// @Produce     json
// @Success     200 {array} store.User
// @Security    BearerAuth
// @Router      /users [get]
func (s *Server) listUsers(c *gin.Context) {
	rows, err := s.st.ListUsers(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rows)
}

type userBody struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

// createUser 新建用户（首次登录强制改密）。
// @Summary     新建用户
// @Tags        用户
// @Accept      json
// @Produce     json
// @Param       body body api.userBody true "用户名/初始密码/显示名"
// @Success     200 {object} store.User
// @Failure     400 {object} map[string]string "用户名或密码不合规"
// @Failure     409 {object} map[string]string "用户名已存在"
// @Security    BearerAuth
// @Router      /users [post]
func (s *Server) createUser(c *gin.Context) {
	var b userBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体格式错误"})
		return
	}
	username := strings.TrimSpace(b.Username)
	if username == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入用户名"})
		return
	}
	if len(b.Password) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "密码至少 8 位"})
		return
	}
	ctx := c.Request.Context()
	if _, err := s.st.GetUserByUsername(ctx, username); err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "用户名已存在"})
		return
	}
	hash, err := hashPassword(b.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码哈希失败"})
		return
	}
	now := time.Now().UTC()
	u := &store.User{
		ID:            store.NewID(),
		Username:      username,
		DisplayName:   strings.TrimSpace(b.DisplayName),
		Role:          "admin",
		PasswordHash:  hash,
		MustChangePwd: true, // 新用户首次登录强制改密
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.st.CreateUser(ctx, u); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, u)
}

type updateUserBody struct {
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

// updateUser 更新用户显示名/角色。
// @Summary     更新用户
// @Tags        用户
// @Accept      json
// @Produce     json
// @Param       id   path string true "用户 ID"
// @Param       body body api.updateUserBody true "显示名/角色"
// @Success     200 {object} store.User
// @Failure     404 {object} map[string]string
// @Security    BearerAuth
// @Router      /users/{id} [put]
func (s *Server) updateUser(c *gin.Context) {
	var b updateUserBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体格式错误"})
		return
	}
	ctx := c.Request.Context()
	u, err := s.st.GetUserByID(ctx, c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}
	u.DisplayName = strings.TrimSpace(b.DisplayName)
	if b.Role != "" {
		u.Role = b.Role
	}
	if err := s.st.UpdateUser(ctx, u); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, u)
}

type resetPwdBody struct {
	Password string `json:"password"`
}

// resetPassword 管理员重置某用户密码（重置后强制改密）。
// @Summary     重置密码
// @Tags        用户
// @Accept      json
// @Produce     json
// @Param       id   path string true "用户 ID"
// @Param       body body api.resetPwdBody true "新密码"
// @Success     200 {object} store.User
// @Failure     400 {object} map[string]string "密码过短"
// @Failure     404 {object} map[string]string
// @Security    BearerAuth
// @Router      /users/{id}/password [put]
func (s *Server) resetPassword(c *gin.Context) {
	var b resetPwdBody
	if err := c.ShouldBindJSON(&b); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体格式错误"})
		return
	}
	if len(b.Password) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "密码至少 8 位"})
		return
	}
	ctx := c.Request.Context()
	u, err := s.st.GetUserByID(ctx, c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}
	hash, err := hashPassword(b.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码哈希失败"})
		return
	}
	u.PasswordHash = hash
	u.MustChangePwd = true // 重置后强制改密
	if err := s.st.UpdateUser(ctx, u); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, u)
}

// deleteUser 删除用户。守卫：不能删自己；不能删最后一个用户（生产保留至少一个管理员）。
// @Summary     删除用户
// @Tags        用户
// @Produce     json
// @Param       id path string true "用户 ID"
// @Success     200 {object} map[string]string
// @Failure     400 {object} map[string]string "删自己 / 删最后一个用户"
// @Failure     404 {object} map[string]string
// @Security    BearerAuth
// @Router      /users/{id} [delete]
func (s *Server) deleteUser(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	if id == currentUserID(c) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能删除当前登录用户"})
		return
	}
	if _, err := s.st.GetUserByID(ctx, id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}
	n, err := s.st.CountUsers(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if n <= 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能删除最后一个用户"})
		return
	}
	if err := s.st.DeleteUser(ctx, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
