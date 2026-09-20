package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"regexp"
	"sort"

	"github.com/gin-gonic/gin"

	"litesentry/server/internal/store"
)

// ---- 插件仓库（阶段二）----

var (
	pluginIDRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	pluginVersionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// maxPluginSize 插件二进制上限（64MB，个人工具绰绰有余）。
const maxPluginSize = 64 << 20

// BuiltinGroupView 内置插件视图：按 plugin_id 聚合各节点上报的版本（只读展示）。
// 内置插件随 agent 二进制发布，不在 plugins 表中，故单独聚合上报清单呈现。
type BuiltinGroupView struct {
	PluginID  string            `json:"plugin_id"`
	Versions  []string          `json:"versions"`   // 去重后续（各节点可能版本不一）
	NodeCount int               `json:"node_count"` // 上报该插件的节点数
	Nodes     []BuiltinNodeView `json:"nodes"`
}

// BuiltinNodeView 单节点内置插件条目（节点一律以主机名呈现，agent_id 不回传）。
type BuiltinNodeView struct {
	Hostname string `json:"hostname"`
	Version  string `json:"version"`
	SHA256   string `json:"sha256"`
}

// listBuiltins 内置插件清单（各节点上报聚合）。
// @Summary     内置插件清单
// @Description 聚合各节点注册上报的内置插件（host/docker/disk），按 id 分组；只读
// @Tags        插件
// @Produce     json
// @Success     200 {array} BuiltinGroupView
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /builtins [get]
func (s *Server) listBuiltins(c *gin.Context) {
	ctx := c.Request.Context()
	agents, err := s.st.Agents(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	groups := map[string]*BuiltinGroupView{}
	seenVer := map[string]map[string]bool{}
	for _, a := range agents {
		if a.BuiltinJSON == "" {
			continue
		}
		for _, b := range store.DecodeBuiltinManifest(a.BuiltinJSON) {
			g := groups[b.PluginID]
			if g == nil {
				g = &BuiltinGroupView{PluginID: b.PluginID}
				groups[b.PluginID] = g
				seenVer[b.PluginID] = map[string]bool{}
			}
			if !seenVer[b.PluginID][b.Version] {
				seenVer[b.PluginID][b.Version] = true
				g.Versions = append(g.Versions, b.Version)
			}
			hostname := a.Hostname
			if hostname == "" {
				hostname = "未知节点"
			}
			g.Nodes = append(g.Nodes, BuiltinNodeView{Hostname: hostname, Version: b.Version, SHA256: b.SHA256})
			g.NodeCount++
		}
	}
	out := make([]*BuiltinGroupView, 0, len(groups))
	for _, g := range groups {
		sort.Strings(g.Versions)
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PluginID < out[j].PluginID })
	c.JSON(http.StatusOK, out)
}

// listPlugins 插件仓库列表（全部版本）。
// @Summary     插件仓库
// @Description 全部插件（含各版本元信息，不含二进制内容）
// @Tags        插件
// @Produce     json
// @Success     200 {array} store.Plugin
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /plugins [get]
func (s *Server) listPlugins(c *gin.Context) {
	rows, err := s.st.ListPlugins(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rows)
}

// uploadPlugin 上传插件：multipart（file 二进制 + id/name/version/args_schema 表单字段）。
// @Summary     上传插件
// @Description 上传不可变插件版本（multipart：file + id/name/version）；服务端计算 SHA-256
// @Tags        插件
// @Accept      multipart/form-data
// @Produce     json
// @Param       file formData file true "插件二进制"
// @Param       id formData string true "插件标识（稳定跨版本）"
// @Param       name formData string true "展示名"
// @Param       version formData string true "版本号（同 id 不可重复）"
// @Param       args_schema formData string false "参数说明（前端表单用，JSON）"
// @Success     200 {object} store.Plugin
// @Failure     400 {object} map[string]string
// @Failure     409 {object} map[string]string "版本已存在"
// @Security    BearerAuth
// @Router      /plugins [post]
func (s *Server) uploadPlugin(c *gin.Context) {
	fh, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "缺少 file 字段"})
		return
	}
	if fh.Size > maxPluginSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "插件超过大小上限（64MB）"})
		return
	}
	f, err := fh.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer f.Close()

	id := c.PostForm("id")
	name := c.PostForm("name")
	version := c.PostForm("version")
	if id == "" || name == "" || version == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id/name/version 不能为空"})
		return
	}
	if !pluginIDRe.MatchString(id) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id 仅允许小写字母/数字/._- 且不以符号开头"})
		return
	}
	if !pluginVersionRe.MatchString(version) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "version 仅允许字母/数字/._- 且不以符号开头"})
		return
	}

	data, err := io.ReadAll(f)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	sum := sha256.Sum256(data)
	p := &store.Plugin{
		ID:         id,
		Name:       name,
		Kind:       "binary",
		Version:    version,
		SHA256:     hex.EncodeToString(sum[:]),
		Size:       int64(len(data)),
		ArgsSchema: c.PostForm("args_schema"),
		Data:       data,
	}
	if err := s.st.SavePlugin(c.Request.Context(), p); err != nil {
		// 唯一错误来源是 UNIQUE(id, version) 冲突 → 409
		c.JSON(http.StatusConflict, gin.H{"error": "版本已存在: " + err.Error()})
		return
	}
	p.Data = nil // 不回传二进制内容
	c.JSON(http.StatusOK, p)
}

// pluginDetail 某插件 id 的全部版本。
// @Summary     插件详情
// @Description 某插件 id 的全部版本元信息
// @Tags        插件
// @Produce     json
// @Param       id path string true "插件标识"
// @Success     200 {array} store.Plugin
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /plugins/{id} [get]
func (s *Server) pluginDetail(c *gin.Context) {
	id := c.Param("id")
	rows, err := s.st.ListPlugins(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]*store.Plugin, 0, 4)
	for _, p := range rows {
		if p.ID == id {
			out = append(out, p)
		}
	}
	c.JSON(http.StatusOK, out)
}

type assignPluginBody struct {
	AgentID  string `json:"agent_id"`
	Version  string `json:"version"`
	ArgsJSON string `json:"args_json"`
}

// assignPlugin 指派插件到某 agent（DesiredState manifest 来源）。
// agent_id 为空串或哨兵 server → 均解析公网机（settings.server_agent_id），与 tasks.go/frp.go 同约定。
// 校验：非内置插件版本必须已上传（防 manifest 指向不存在的发布物）；内置插件版本随
// agent 发布、不在 plugins 表，故改以该节点上报的内置清单为准（支持「取消指派后再恢复」）。
// @Summary     指派插件
// @Description 将某版本插件指派到指定 agent（覆盖旧指派）；agent 下次心跳拉取并执行。
// @Description agent_id 可传 "server" 或空串表示公网机；内置插件（host/docker/disk）版本须与节点上报清单一致
// @Tags        插件
// @Accept      json
// @Produce     json
// @Param       id path string true "插件标识"
// @Param       body body api.assignPluginBody true "agent_id / version / args_json"
// @Success     200 {object} map[string]string
// @Failure     400 {object} map[string]string "内置插件版本与节点上报不符"
// @Failure     404 {object} map[string]string "插件版本不存在 / 未设置公网机 Agent"
// @Security    BearerAuth
// @Router      /plugins/{id}/assign [post]
func (s *Server) assignPlugin(c *gin.Context) {
	var body assignPluginBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	id := c.Param("id")
	if body.Version == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "version 不能为空"})
		return
	}
	// agent_id：空串与哨兵 server 同义，均指「公网机（Server 同机 agent）」——
	// 与 tasks.go resolveTargetAgent（'' = 公网机）及 FRP 页哨兵约定一致，两种写法都收。
	ctx := c.Request.Context()
	agentID := body.AgentID
	if agentID == "" || agentID == serverPathID {
		agentID, _ = s.st.GetSetting(ctx, "server_agent_id")
		if agentID == "" {
			c.JSON(http.StatusNotFound, gin.H{"error": "未设置公网机 Agent（settings.server_agent_id）"})
			return
		}
	}
	// 版本校验：内置插件版本以节点上报清单为准（不在 plugins 表）；外置插件须已上传。
	manifest, mErr := s.st.BuiltinManifest(ctx, agentID)
	if mErr != nil {
		// 读清单失败不阻断：退化为「只查插件仓库」（旧 agent / 存储异常时行为与改动前一致）
		manifest = nil
	}
	builtinVer := ""
	for _, b := range manifest {
		if b.PluginID == id && b.Version == body.Version {
			builtinVer = b.Version
			break
		}
	}
	if builtinVer == "" {
		if _, err := s.st.GetPlugin(ctx, id, body.Version); err != nil {
			// 内置 id 但版本不符 → 明确指出（避免用户以为内置插件版本可选任意值）
			for _, b := range manifest {
				if b.PluginID == id {
					c.JSON(http.StatusBadRequest, gin.H{
						"error": "内置插件版本须与节点上报一致：本节点为 " + b.Version,
					})
					return
				}
			}
			c.JSON(http.StatusNotFound, gin.H{"error": "插件版本不存在"})
			return
		}
	}
	if err := s.st.AssignPlugin(ctx, &store.AgentPlugin{
		AgentID:  agentID,
		PluginID: id,
		Version:  body.Version,
		ArgsJSON: body.ArgsJSON,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// deletePluginVersion 删除某插件版本。仍被节点指派时拒绝（须先取消指派），避免节点指向不存在的发布物。
// @Summary     删除插件版本
// @Description 删除不可变插件版本；若仍被任一节点指派则返回 409，须先取消指派
// @Tags        插件
// @Produce     json
// @Param       id path string true "插件标识"
// @Param       version path string true "版本号"
// @Success     200 {object} map[string]string
// @Failure     409 {object} map[string]string "仍被节点指派"
// @Security    BearerAuth
// @Router      /plugins/{id}/versions/{version} [delete]
func (s *Server) deletePluginVersion(c *gin.Context) {
	id := c.Param("id")
	version := c.Param("version")
	ctx := c.Request.Context()
	if _, err := s.st.GetPlugin(ctx, id, version); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "插件版本不存在"})
		return
	}
	if err := s.st.DeletePluginVersion(ctx, id, version); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// unassignPlugin 取消某节点上某插件的指派（从 manifest 移除，节点下个心跳停止该插件）。
// @Summary     取消插件指派
// @Description 从指定 agent 的 manifest 移除某插件指派
// @Tags        插件
// @Produce     json
// @Param       id path string true "agent_id"
// @Param       pluginId path string true "插件标识"
// @Success     200 {object} map[string]string
// @Security    BearerAuth
// @Router      /agents/{id}/plugins/{pluginId} [delete]
func (s *Server) unassignPlugin(c *gin.Context) {
	agentID := c.Param("id")
	pluginID := c.Param("pluginId")
	if err := s.st.UnassignPlugin(c.Request.Context(), agentID, pluginID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
