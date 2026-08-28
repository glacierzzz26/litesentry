// Package api 提供 Gin REST/SSE，供 React 前端取数。
// 前端不直接碰 gRPC；数据从 Store 读 JSON。
package api

import (
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"litesentry/server/internal/alert"
	"litesentry/server/internal/store"
)

type Server struct {
	st         store.Store
	jwtSecret  []byte
	jwtTTL     time.Duration
	loginFails *failLimiter
}

// New 创建 Web API Server。
// jwtSecret 为 JWT 签名密钥；jwtTTL 为 token 有效期；Web 鉴权一律走 JWT
// （gRPC Agent 鉴权用独立静态 token，见 grpc 包，与本处无关）。
func New(st store.Store, jwtSecret []byte, jwtTTL time.Duration) *Server {
	return &Server{
		st:         st,
		jwtSecret:  jwtSecret,
		jwtTTL:     jwtTTL,
		loginFails: newFailLimiter(5, 15*time.Minute),
	}
}

// Agent 状态机（服务端权威判定，字符串便于扩展）：
//   - online   最近心跳 ≤ offlineAfter
//   - offline  连续 offlineAfter 无心跳
//   - 预留 upgrading 等状态（阶段二 Agent 升级时启用）
const (
	StatusOnline  = "online"
	StatusOffline = "offline"
)

// offlineAfter 连续无心跳判定离线阈值（Agent 每 60s 心跳 → 5 分钟 = 连续 5 次缺失）。
const offlineAfter = 5 * time.Minute

// agentStatus 按最近心跳计算节点状态。
func agentStatus(lastSeen time.Time) string {
	if time.Since(lastSeen) <= offlineAfter {
		return StatusOnline
	}
	return StatusOffline
}

// Routes 注册全部路由。除 POST /api/auth/login 外，其余 /api/* 均需 JWT（s.auth() 中间件）。
func (s *Server) Routes() *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	// 免鉴权：登录换取 JWT
	r.POST("/api/auth/login", s.login)

	api := r.Group("/api", s.auth())
	{
		api.GET("/health", s.health)
		api.GET("/overview", s.overview)
		api.GET("/agents", s.agents)
		api.GET("/agents/:id/host", s.hostSeries)
		api.GET("/agents/:id/containers", s.containerSeries)
		api.GET("/agents/:id/disks", s.diskSeries)

		api.GET("/settings", s.getSettings)
		api.PUT("/settings", s.putSettings)
		api.POST("/settings/feishu-test", s.feishuTest)

		api.GET("/plugins", s.listPlugins)
		api.POST("/plugins", s.uploadPlugin)
		api.GET("/plugins/:id", s.pluginDetail)
		api.POST("/plugins/:id/assign", s.assignPlugin)

		api.GET("/frp", s.listFrp)
		api.PUT("/frp/:kind/:agent_id", s.saveFrp)
		api.DELETE("/frp/:kind/:agent_id", s.deleteFrp)

		api.GET("/tasks", s.listTasks)
		api.POST("/tasks", s.createTask)
		api.PUT("/tasks/:id", s.updateTask)
		api.DELETE("/tasks/:id", s.deleteTask)
		api.GET("/tasks/:id/runs", s.taskRuns)
		api.GET("/agents/:id/plugins", s.agentPlugins)

		api.GET("/alerts/rules", s.listRules)
		api.POST("/alerts/rules", s.createRule)
		api.PUT("/alerts/rules/:id", s.updateRule)
		api.DELETE("/alerts/rules/:id", s.deleteRule)
		api.GET("/alerts/events", s.listEvents)

		api.POST("/auth/change-password", s.changePassword)
		api.GET("/auth/me", s.me)
		api.GET("/users", s.listUsers)
		api.POST("/users", s.createUser)
		api.PUT("/users/:id", s.updateUser)
		api.PUT("/users/:id/password", s.resetPassword)
		api.DELETE("/users/:id", s.deleteUser)
	}
	return r
}

// health 健康检查。
// @Summary     健康检查
// @Tags        系统
// @Produce     json
// @Success     200 {object} map[string]string "status/time"
// @Router      /health [get]
func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "time": time.Now().UTC().Format(time.RFC3339)})
}

// agents 节点列表（含 IPv4/IPv6 地址快照）。
// @Summary     节点列表
// @Description 全部 Agent 最新状态，含 IPv4/IPv6 地址快照
// @Tags        节点
// @Produce     json
// @Success     200 {array} store.Agent
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /agents [get]
func (s *Server) agents(c *gin.Context) {
	list, err := s.st.Agents(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for _, a := range list {
		a.Status = agentStatus(a.LastSeen)
	}
	c.JSON(http.StatusOK, list)
}

// MountStatic 挂载内嵌前端（hash 路由 SPA）。
// /api、/docs 交给既有路由；/assets/* 走静态文件；其余回退 index.html。
func MountStatic(r *gin.Engine, fsys fs.FS) {
	fileServer := http.FileServer(http.FS(fsys))
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api") || strings.HasPrefix(p, "/docs") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		if strings.HasPrefix(p, "/assets/") {
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}
		index, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "index.html missing in embedded frontend"})
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	})
}

// parseWindow 解析 ?from=&to= unix 秒；缺省返回最近 def 窗口。
func parseWindow(c *gin.Context, def time.Duration) (time.Time, time.Time) {
	now := time.Now().UTC()
	from := now.Add(-def)
	to := now
	if v := c.Query("from"); v != "" {
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
			from = time.Unix(sec, 0).UTC()
		}
	}
	if v := c.Query("to"); v != "" {
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
			to = time.Unix(sec, 0).UTC()
		}
	}
	return from, to
}

// hostSeries 主机时序样本。
// @Summary     主机指标时序
// @Description 某节点在窗口内的主机样本（CPU/内存/负载/网络/系统信息），按时间升序
// @Tags        时序
// @Produce     json
// @Param       id   path     string true "Agent ID"
// @Param       from query    int    false "起始 unix 秒（默认 1 小时前）"
// @Param       to   query    int    false "结束 unix 秒（默认当前）"
// @Success     200 {array} store.HostSample
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /agents/{id}/host [get]
func (s *Server) hostSeries(c *gin.Context) {
	from, to := parseWindow(c, time.Hour)
	rows, err := s.st.QueryHost(c.Request.Context(), c.Param("id"), from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rows)
}

// overview 总览聚合：一次返回前端总览页所需全量数据（每节点最新主机样本 + 每节点最高磁盘占用 + firing 事件数），
// 消除按节点逐个扇出请求（N+1）。
// @Summary     总览聚合
// @Description 一次返回窗口内每节点最新主机样本、每节点最高磁盘占用、当前 firing 事件数
// @Tags        时序
// @Produce     json
// @Param       from query    int    false "起始 unix 秒（默认 1 小时前）"
// @Param       to   query    int    false "结束 unix 秒（默认当前）"
// @Success     200 {object} store.Overview
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /overview [get]
func (s *Server) overview(c *gin.Context) {
	from, to := parseWindow(c, time.Hour)
	ov, err := s.st.Overview(c.Request.Context(), from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, ov)
}

// diskSeries 磁盘时序样本。
// @Summary     磁盘指标时序
// @Description 某节点在窗口内各挂载点的磁盘占用样本，按时间升序
// @Tags        时序
// @Produce     json
// @Param       id   path     string true "Agent ID"
// @Param       from query    int    false "起始 unix 秒（默认 1 小时前）"
// @Param       to   query    int    false "结束 unix 秒（默认当前）"
// @Success     200 {array} store.DiskSample
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /agents/{id}/disks [get]
func (s *Server) diskSeries(c *gin.Context) {
	from, to := parseWindow(c, time.Hour)
	rows, err := s.st.QueryDisks(c.Request.Context(), c.Param("id"), from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rows)
}

// containerSeries 容器时序样本。
// @Summary     容器指标时序
// @Description 某节点在窗口内各容器的状态与资源样本，按时间升序
// @Tags        时序
// @Produce     json
// @Param       id   path     string true "Agent ID"
// @Param       from query    int    false "起始 unix 秒（默认 1 小时前）"
// @Param       to   query    int    false "结束 unix 秒（默认当前）"
// @Success     200 {array} store.ContainerSample
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /agents/{id}/containers [get]
func (s *Server) containerSeries(c *gin.Context) {
	from, to := parseWindow(c, time.Hour)
	rows, err := s.st.QueryContainers(c.Request.Context(), c.Param("id"), from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rows)
}

// ---- 设置 ----

// settingsView 设置视图：secret 只回传是否已设置，不泄漏原文。
type settingsView struct {
	FeishuWebhook   string `json:"feishu_webhook"`
	FeishuSecretSet bool   `json:"feishu_secret_set"`
}

// settingsBody 设置写入体（空 secret = 保留原值；feishu_secret_clear = 显式清除）。
type settingsBody struct {
	FeishuWebhook     string `json:"feishu_webhook"`
	FeishuSecret      string `json:"feishu_secret"`
	FeishuSecretClear bool   `json:"feishu_secret_clear"`
}

// getSettings 读取设置。
// @Summary     读取设置
// @Description 飞书 webhook 与 secret 是否存在（secret 不回显明文）
// @Tags        设置
// @Produce     json
// @Success     200 {object} api.settingsView
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /settings [get]
func (s *Server) getSettings(c *gin.Context) {
	ctx := c.Request.Context()
	webhook, err := s.st.GetSetting(ctx, "feishu_webhook")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	secret, _ := s.st.GetSetting(ctx, "feishu_secret")
	c.JSON(http.StatusOK, settingsView{FeishuWebhook: webhook, FeishuSecretSet: secret != ""})
}

// putSettings 保存设置。
// @Summary     保存设置
// @Description 更新飞书 webhook；secret 为空表示保留原值
// @Tags        设置
// @Accept      json
// @Produce     json
// @Success     200 {object} api.settingsView
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /settings [put]
func (s *Server) putSettings(c *gin.Context) {
	var body settingsBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	ctx := c.Request.Context()
	if err := s.st.SetSetting(ctx, "feishu_webhook", body.FeishuWebhook); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	secretSet := false
	switch {
	case body.FeishuSecretClear:
		if err := s.st.SetSetting(ctx, "feishu_secret", ""); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	case body.FeishuSecret != "":
		if err := s.st.SetSetting(ctx, "feishu_secret", body.FeishuSecret); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		secretSet = true
	default:
		if cur, _ := s.st.GetSetting(ctx, "feishu_secret"); cur != "" {
			secretSet = true
		}
	}
	c.JSON(http.StatusOK, settingsView{FeishuWebhook: body.FeishuWebhook, FeishuSecretSet: secretSet})
}

// feishuTest 发送测试消息。
// @Summary     测试飞书机器人
// @Description 按当前配置发送一条测试消息
// @Tags        设置
// @Success     200 {object} map[string]string
// @Failure     400 {object} map[string]string "未配置或发送失败"
// @Security    BearerAuth
// @Router      /settings/feishu-test [post]
func (s *Server) feishuTest(c *gin.Context) {
	if err := alert.SendTest(c.Request.Context(), s.st); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "message": "测试消息已发送"})
}

// ---- 告警规则 ----

// allowedRuleMetrics 与 alert 包指标集保持一致。
var allowedRuleMetrics = map[string]bool{
	alert.MetricCPU: true, alert.MetricMem: true, alert.MetricLoad1: true,
	alert.MetricDiskPct: true, alert.MetricContCPU: true, alert.MetricContMem: true,
	alert.MetricContDown: true, alert.MetricOffline: true,
}

// validateRule 校验规则字段，返回可写的规范化副本。
func validateRule(r *store.AlertRule) (*store.AlertRule, error) {
	if r.Name == "" {
		return nil, errors.New("规则名称不能为空")
	}
	if !allowedRuleMetrics[r.Metric] {
		return nil, errors.New("不支持的指标: " + r.Metric)
	}
	if r.Op != ">" && r.Op != "<" {
		r.Op = ">"
	}
	if r.Severity != "critical" {
		r.Severity = "warning"
	}
	if r.DurationS < 0 {
		r.DurationS = 0
	}
	return r, nil
}

// listRules 规则列表。
// @Summary     告警规则列表
// @Tags        告警
// @Produce     json
// @Success     200 {array} store.AlertRule
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /alerts/rules [get]
func (s *Server) listRules(c *gin.Context) {
	rows, err := s.st.ListRules(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rows)
}

// createRule 新建规则。
// @Summary     新建告警规则
// @Tags        告警
// @Accept      json
// @Produce     json
// @Param       body body store.AlertRule true "规则（id 忽略）"
// @Success     200 {object} store.AlertRule
// @Failure     400 {object} map[string]string
// @Security    BearerAuth
// @Router      /alerts/rules [post]
func (s *Server) createRule(c *gin.Context) {
	var r store.AlertRule
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	r.ID = ""
	if _, err := validateRule(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.st.SaveRule(c.Request.Context(), &r); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, r)
}

// updateRule 更新规则。
// @Summary     更新告警规则
// @Tags        告警
// @Accept      json
// @Produce     json
// @Param       id   path string true "规则 ID"
// @Param       body body store.AlertRule true "规则（id 取自路径）"
// @Success     200 {object} store.AlertRule
// @Failure     400 {object} map[string]string
// @Security    BearerAuth
// @Router      /alerts/rules/{id} [put]
func (s *Server) updateRule(c *gin.Context) {
	var r store.AlertRule
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	r.ID = c.Param("id")
	if _, err := validateRule(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.st.SaveRule(c.Request.Context(), &r); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, r)
}

// deleteRule 删除规则。
// @Summary     删除告警规则
// @Tags        告警
// @Success     200 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /alerts/rules/{id} [delete]
func (s *Server) deleteRule(c *gin.Context) {
	if err := s.st.DeleteRule(c.Request.Context(), c.Param("id")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// listEvents 告警事件列表。
// @Summary     告警事件列表
// @Description 按时间窗口查询，可按节点 / 状态过滤；默认最近 24h
// @Tags        告警
// @Produce     json
// @Param       from query int false "起始 unix 秒"
// @Param       to   query int false "结束 unix 秒"
// @Param       agent_id query string false "节点 ID"
// @Param       state query string false "firing | resolved"
// @Param       limit query int false "条数（默认 100，最大 500）"
// @Success     200 {array} store.AlertEvent
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /alerts/events [get]
func (s *Server) listEvents(c *gin.Context) {
	from, to := parseWindow(c, 24*time.Hour)
	limit := 100
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
			if limit > 500 {
				limit = 500
			}
		}
	}
	rows, err := s.st.QueryEvents(c.Request.Context(), from, to, c.Query("agent_id"), c.Query("state"), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rows)
}
