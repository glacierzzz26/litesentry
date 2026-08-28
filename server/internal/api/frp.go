package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"litesentry/server/internal/store"
)

// ---- FRP 隧道管理（阶段二 S3）----
//
// 配置渲染 + 下发在 grpc 包的 DesiredState（server/internal/frp/render.go），本层只做：
// 配置 CRUD、join 最近上报状态、目标节点显示名解析。
//
// 安全：token 绝不过 REST 回传（token_set 布尔占位，编辑时留空 = 保留原值）；
// frp 配置 ≈ 远程执行能力，auth 中间件保证仅登录用户可写。

// frpsPathID 是 frps 配置在 REST 路径中的 agent_id 段。DB 内 frps 行 agent_id=''（S1 契约），
// 但路径段必须非空才能被 gin 路由匹配 → API 层统一用固定哨兵，前端拿到它直接回填 PUT/DELETE。
const frpsPathID = "server"

// normFrpAgentID 将 REST 路径的 agent_id 段归一化为 DB 存储值：
// frps 恒为 ''；frpc 原样。
func normFrpAgentID(kind, pathID string) string {
	if kind == "frps" {
		return ""
	}
	return pathID
}

// frpView 前端展示的 frp 配置视图（不含 token 明文）。
type frpView struct {
	Kind       string            `json:"kind"`
	AgentID    string            `json:"agent_id"` // frps 行恒为 "server"（哨兵）；前端不展示
	AgentName  string            `json:"agent_name"`
	ServerAddr string            `json:"server_addr"`
	ServerPort int               `json:"server_port"`
	Proxies    []store.FrpTunnel `json:"proxies"`
	Enabled    bool              `json:"enabled"`
	TokenSet   bool              `json:"token_set"`
	UpdatedAt  time.Time         `json:"updated_at"`
	Status     *store.FrpStatus  `json:"status,omitempty"`
}

// saveFrpBody PUT 写入体。token 为空 = 保留原值（编辑不强制回填）。
type saveFrpBody struct {
	ServerAddr string            `json:"server_addr"`
	ServerPort int               `json:"server_port"`
	Token      string            `json:"token"`
	Proxies    []store.FrpTunnel `json:"proxies"`
	Enabled    bool              `json:"enabled"`
}

// listFrp 配置列表：逐条 join 最近上报状态 + 目标节点显示名。
// @Summary     FRP 配置列表
// @Description 全部 frps/frpc 配置，join 最近上报状态与目标节点主机名（token 不回传）
// @Tags        FRP
// @Produce     json
// @Success     200 {array} api.frpView
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /frp [get]
func (s *Server) listFrp(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := s.st.ListFrpConfigs(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]frpView, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.frpView(ctx, r))
	}
	c.JSON(http.StatusOK, out)
}

// frpView 构造单条配置视图：解析状态归属 agent 与目标节点显示名。
func (s *Server) frpView(ctx context.Context, r *store.FrpConfig) frpView {
	v := frpView{
		Kind:       r.Kind,
		ServerAddr: r.ServerAddr,
		ServerPort: r.ServerPort,
		Proxies:    r.Proxies,
		Enabled:    r.Enabled,
		TokenSet:   r.Token != "",
		UpdatedAt:  r.UpdatedAt,
	}
	hosts := map[string]string{}
	if agents, err := s.st.Agents(ctx); err == nil {
		for _, a := range agents {
			hosts[a.AgentID] = a.Hostname
		}
	}

	if r.Kind == "frps" {
		// frps 状态归属 = server 同机 agent（由设置项 server_agent_id 声明）
		v.AgentID = frpsPathID
		v.AgentName = "公网机（Server 同机）"
		serverID, _ := s.st.GetSetting(ctx, "server_agent_id")
		if serverID != "" {
			if h, ok := hosts[serverID]; ok {
				v.AgentName = h
			}
			if st, err := s.st.QueryFrpStatus(ctx, serverID); err == nil {
				v.Status = st
			}
		}
	} else {
		v.AgentID = r.AgentID
		if h, ok := hosts[r.AgentID]; ok {
			v.AgentName = h
		} else {
			v.AgentName = "未知节点"
		}
		if st, err := s.st.QueryFrpStatus(ctx, r.AgentID); err == nil {
			v.Status = st
		}
	}
	return v
}

// saveFrp 新建或更新 frp 配置（幂等 upsert）。
// @Summary     保存 FRP 配置
// @Description 新建（agent_id 不存在时）或更新 frps/frpc 配置；token 留空 = 保留原值
// @Tags        FRP
// @Accept      json
// @Produce     json
// @Param       kind     path string true "frps | frpc"
// @Param       agent_id path string true "目标 agent（frps 恒为 server）"
// @Param       body     body api.saveFrpBody true "配置体"
// @Success     200 {object} api.frpView
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /frp/{kind}/{agent_id} [put]
func (s *Server) saveFrp(c *gin.Context) {
	kind := c.Param("kind")
	if kind != "frps" && kind != "frpc" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind 仅支持 frps / frpc"})
		return
	}
	var body saveFrpBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if body.ServerPort < 1 || body.ServerPort > 65535 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "server_port 需在 1-65535"})
		return
	}
	agentID := normFrpAgentID(kind, c.Param("agent_id"))
	if kind == "frpc" && agentID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "frpc 必须指定目标节点"})
		return
	}
	for _, p := range body.Proxies {
		if p.Name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "proxy name 不能为空"})
			return
		}
		if p.LocalPort != 0 && (p.LocalPort < 1 || p.LocalPort > 65535) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "local_port 需在 1-65535: " + p.Name})
			return
		}
		if p.RemotePort != 0 && (p.RemotePort < 1 || p.RemotePort > 65535) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "remote_port 需在 1-65535: " + p.Name})
			return
		}
	}

	ctx := c.Request.Context()
	cur, gerr := s.st.GetFrpConfig(ctx, kind, agentID)
	token := body.Token
	switch {
	case gerr == nil:
		// 编辑：token 留空保留原值
		if token == "" {
			token = cur.Token
		}
	case errors.Is(gerr, sql.ErrNoRows):
		// 新建：必须提供 token（frp 认证令牌 + admin 密码）
		if token == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "新建 frp 配置必须提供 token"})
			return
		}
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": gerr.Error()})
		return
	}

	cfg := &store.FrpConfig{
		Kind:       kind,
		AgentID:    agentID,
		ServerAddr: body.ServerAddr,
		ServerPort: body.ServerPort,
		Token:      token,
		Proxies:    body.Proxies,
		Enabled:    body.Enabled,
	}
	if err := s.st.SaveFrpConfig(ctx, cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 回读落库值（updated_at 由 store 写入真实时间，内存副本为零值）
	saved, err := s.st.GetFrpConfig(ctx, kind, agentID)
	if err != nil {
		saved = cfg
	}
	c.JSON(http.StatusOK, s.frpView(ctx, saved))
}

// deleteFrp 删除 frp 配置：下次 DesiredState 不带 frp → agent 停掉进程。
// @Summary     删除 FRP 配置
// @Description 删除 frps/frpc 配置并下线对应 frp 进程（agent 下次心跳生效）
// @Tags        FRP
// @Success     200 {object} map[string]string
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /frp/{kind}/{agent_id} [delete]
func (s *Server) deleteFrp(c *gin.Context) {
	kind := c.Param("kind")
	if kind != "frps" && kind != "frpc" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "kind 仅支持 frps / frpc"})
		return
	}
	agentID := normFrpAgentID(kind, c.Param("agent_id"))
	if err := s.st.DeleteFrpConfig(c.Request.Context(), kind, agentID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
