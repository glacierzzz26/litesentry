package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"regexp"

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
// @Summary     指派插件
// @Description 将某版本插件指派到指定 agent（覆盖旧指派）；agent 下次心跳拉取并执行
// @Tags        插件
// @Accept      json
// @Produce     json
// @Param       id path string true "插件标识"
// @Param       body body api.assignPluginBody true "agent_id / version / args_json"
// @Success     200 {object} map[string]string
// @Failure     404 {object} map[string]string "插件版本不存在"
// @Security    BearerAuth
// @Router      /plugins/{id}/assign [post]
func (s *Server) assignPlugin(c *gin.Context) {
	var body assignPluginBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	id := c.Param("id")
	if body.AgentID == "" || body.Version == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "agent_id/version 不能为空"})
		return
	}
	// 校验插件版本存在（防 manifest 白名单指向不存在的发布物）
	if _, err := s.st.GetPlugin(c.Request.Context(), id, body.Version); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "插件版本不存在"})
		return
	}
	if err := s.st.AssignPlugin(c.Request.Context(), &store.AgentPlugin{
		AgentID:  body.AgentID,
		PluginID: id,
		Version:  body.Version,
		ArgsJSON: body.ArgsJSON,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
