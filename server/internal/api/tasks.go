package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"litesentry/server/internal/store"
)

// ---- 定时任务（阶段二 S4）----
//
// 任务 = cron + 目标 agent + 插件引用 + args：定义与下发在 grpc 包 DesiredState（已实现），
// 执行全在目标 agent 侧（本地 cron 触发 → 插件一次性 run）。本层只做：CRUD、运行历史查询、
// 目标节点显示名解析、以及「插件必须已指派到目标节点」的校验（S1 信任边界：只执行白名单内插件）。
//
// 安全：任务引用插件即执行能力 → 仅登录用户可写（auth 中间件）；agent_id 绝不回前端，一律主机名。

// serverPathID 是「公网机（server 同机 agent）」在 REST 路径中的 agent_id 段哨兵。
// DB 内公网机任务 target_agent_id=''，但路径段必须非空才能被 gin 路由匹配 → 与 frp.go 的
// frpsPathID 同一约定（同值 "server"），前端拿到它回填请求体时再归一化为 ''。
const serverPathID = "server"

// taskView 前端展示的任务视图（target_agent_id 供编辑表单回填，前端不展示）。
type taskView struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Description     string     `json:"description"`
	TargetAgentID   string     `json:"target_agent_id"` // 空 = 公网机（server 同机 agent）
	TargetAgentName string     `json:"target_agent_name"`
	Cron            string     `json:"cron"`
	PluginID        string     `json:"plugin_id"`
	ArgsJSON        string     `json:"args_json"`
	TimeoutS        uint32     `json:"timeout_s"`
	Enabled         bool       `json:"enabled"`
	LastRunAt       *time.Time `json:"last_run_at,omitempty"`
	LastStatus      string     `json:"last_status"` // ok | failed | timeout | skipped
	LastOutputTail  string     `json:"last_output_tail"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type saveTaskBody struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	TargetAgentID string `json:"target_agent_id"` // 空 = 公网机
	Cron          string `json:"cron"`
	PluginID      string `json:"plugin_id"`
	ArgsJSON      string `json:"args_json"`
	TimeoutS      uint32 `json:"timeout_s"`
	Enabled       bool   `json:"enabled"`
}

// resolveTargetAgent 解析任务实际下发的目标 agent id（'' = 公网机 → settings.server_agent_id）。
func (s *Server) resolveTargetAgent(ctx context.Context, targetAgentID string) (string, error) {
	if targetAgentID == "" {
		id, _ := s.st.GetSetting(ctx, "server_agent_id")
		if id == "" {
			return "", errors.New("未设置公网机 Agent（settings.server_agent_id），任务无处下发")
		}
		return id, nil
	}
	return targetAgentID, nil
}

// pluginAssigned 目标 agent 的指派清单（AgentPlugins）是否含该插件。
func (s *Server) pluginAssigned(ctx context.Context, agentID, pluginID string) bool {
	rows, err := s.st.AgentPlugins(ctx, agentID)
	if err != nil {
		return false
	}
	for _, ap := range rows {
		if ap.PluginID == pluginID {
			return true
		}
	}
	return false
}

// validateTask 校验任务写入体：名称 / cron / 超时 / args / 目标节点存在 / 插件已指派（S1 信任边界）。
func (s *Server) validateTask(ctx context.Context, body *saveTaskBody) error {
	if strings.TrimSpace(body.Name) == "" {
		return errors.New("任务名称不能为空")
	}
	if len(body.Name) > 64 {
		return errors.New("任务名称最长 64 字符")
	}
	if !validCron(body.Cron) {
		return errors.New("cron 无效（标准 5 段：分 时 日 月 周）")
	}
	if body.TimeoutS < 1 || body.TimeoutS > 86400 {
		return errors.New("timeout_s 需在 1-86400 秒之间")
	}
	if body.ArgsJSON != "" && !json.Valid([]byte(body.ArgsJSON)) {
		return errors.New("args_json 不是合法 JSON")
	}
	target, err := s.resolveTargetAgent(ctx, body.TargetAgentID)
	if err != nil {
		return err
	}
	known := false
	if agents, err := s.st.Agents(ctx); err == nil {
		for _, a := range agents {
			if a.AgentID == target {
				known = true
				break
			}
		}
	}
	if !known {
		return errors.New("目标节点不存在")
	}
	if !s.pluginAssigned(ctx, target, body.PluginID) {
		return errors.New("插件 " + body.PluginID + " 未指派到目标节点")
	}
	return nil
}

// agentHosts 全量 agent_id → hostname 映射（视图 join 用）。
func (s *Server) agentHosts(ctx context.Context) map[string]string {
	hosts := map[string]string{}
	if agents, err := s.st.Agents(ctx); err == nil {
		for _, a := range agents {
			hosts[a.AgentID] = a.Hostname
		}
	}
	return hosts
}

// taskViewOf 构造任务视图：目标节点显示名解析（'' → server_agent_id → 主机名；未知 → 未知节点）。
func (s *Server) taskViewOf(ctx context.Context, t *store.Task, hosts map[string]string) taskView {
	v := taskView{
		ID:              t.ID,
		Name:            t.Name,
		Description:     t.Description,
		TargetAgentID:   t.TargetAgentID,
		Cron:            t.Cron,
		PluginID:        t.PluginID,
		ArgsJSON:        t.ArgsJSON,
		TimeoutS:        t.TimeoutS,
		Enabled:         t.Enabled,
		LastRunAt:       t.LastRunAt,
		LastStatus:      t.LastStatus,
		LastOutputTail:  t.LastOutputTail,
		CreatedAt:       t.CreatedAt,
		UpdatedAt:       t.UpdatedAt,
	}
	if t.TargetAgentID == "" {
		v.TargetAgentName = "公网机（Server 同机）"
		if serverID, _ := s.st.GetSetting(ctx, "server_agent_id"); serverID != "" {
			if h, ok := hosts[serverID]; ok {
				v.TargetAgentName = h
			}
		}
	} else if h, ok := hosts[t.TargetAgentID]; ok {
		v.TargetAgentName = h
	} else {
		v.TargetAgentName = "未知节点"
	}
	return v
}

// listTasks 任务列表（含最近运行摘要）。
// @Summary     定时任务列表
// @Description 全部定时任务，含目标节点主机名与最近运行摘要（agent_id 不回传）
// @Tags        定时任务
// @Produce     json
// @Success     200 {array} api.taskView
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /tasks [get]
func (s *Server) listTasks(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := s.st.ListTasks(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	hosts := s.agentHosts(ctx)
	out := make([]taskView, 0, len(rows))
	for _, t := range rows {
		out = append(out, s.taskViewOf(ctx, t, hosts))
	}
	c.JSON(http.StatusOK, out)
}

// createTask 新建定时任务。
// @Summary     新建定时任务
// @Description 新建定时任务：cron 到期后目标 agent 本地触发插件一次性 run（插件必须已指派）
// @Tags        定时任务
// @Accept      json
// @Produce     json
// @Param       body body api.saveTaskBody true "任务定义"
// @Success     200 {object} api.taskView
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /tasks [post]
func (s *Server) createTask(c *gin.Context) {
	ctx := c.Request.Context()
	var body saveTaskBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if err := s.validateTask(ctx, &body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	t := &store.Task{
		Name:          body.Name,
		Description:   body.Description,
		TargetAgentID: body.TargetAgentID,
		Cron:          body.Cron,
		PluginID:      body.PluginID,
		ArgsJSON:      body.ArgsJSON,
		TimeoutS:      body.TimeoutS,
		Enabled:       body.Enabled,
	}
	if err := s.st.SaveTask(ctx, t); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// 回读落库值（created_at/updated_at 由 store 写入真实时间）
	saved, err := s.st.GetTask(ctx, t.ID)
	if err != nil {
		saved = t
	}
	c.JSON(http.StatusOK, s.taskViewOf(ctx, saved, s.agentHosts(ctx)))
}

// updateTask 更新定时任务（目标/插件/args 变更 → agent 下次心跳对齐）。
// @Summary     更新定时任务
// @Description 更新定时任务定义（含目标节点/插件/args 变更）
// @Tags        定时任务
// @Accept      json
// @Produce     json
// @Param       id   path string true "任务 ID"
// @Param       body body api.saveTaskBody true "任务定义"
// @Success     200 {object} api.taskView
// @Failure     400 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Failure     404 {object} map[string]string
// @Security    BearerAuth
// @Router      /tasks/{id} [put]
func (s *Server) updateTask(c *gin.Context) {
	ctx := c.Request.Context()
	id := c.Param("id")
	var body saveTaskBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if err := s.validateTask(ctx, &body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	cur, err := s.st.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	cur.Name, cur.Description = body.Name, body.Description
	cur.TargetAgentID, cur.Cron = body.TargetAgentID, body.Cron
	cur.PluginID, cur.ArgsJSON = body.PluginID, body.ArgsJSON
	cur.TimeoutS, cur.Enabled = body.TimeoutS, body.Enabled
	if err := s.st.SaveTask(ctx, cur); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, s.taskViewOf(ctx, cur, s.agentHosts(ctx)))
}

// deleteTask 删除定时任务（agent 下次心跳停止调度）。
// @Summary     删除定时任务
// @Description 删除定时任务定义；历史运行记录保留（append-only 审计）
// @Tags        定时任务
// @Success     200 {object} map[string]string
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /tasks/{id} [delete]
func (s *Server) deleteTask(c *gin.Context) {
	if err := s.st.DeleteTask(c.Request.Context(), c.Param("id")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// taskRuns 某任务的运行历史（审计，append-only）。
// @Summary     任务运行历史
// @Description 某定时任务的执行历史（agent 上报，最近 limit 条，默认 50）
// @Tags        定时任务
// @Produce     json
// @Param       id    path string true "任务 ID"
// @Param       limit query int    false "条数上限（默认 50，最大 200）"
// @Success     200 {array} store.TaskRun
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /tasks/{id}/runs [get]
func (s *Server) taskRuns(c *gin.Context) {
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	rows, err := s.st.QueryTaskRuns(c.Request.Context(), c.Param("id"), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rows)
}

// agentPlugins 目标 agent 已指派的插件清单（任务表单插件下拉数据源）。
// agent_id 段支持哨兵 server → 解析公网机（settings.server_agent_id）。
// @Summary     Agent 已指派插件
// @Description 某 agent（或公网机 server）已指派的插件清单，供定时任务表单选择
// @Tags        定时任务
// @Produce     json
// @Param       id path string true "agent_id，或 server（公网机）"
// @Success     200 {array} store.AgentPlugin
// @Failure     401 {object} map[string]string
// @Security    BearerAuth
// @Router      /agents/{id}/plugins [get]
func (s *Server) agentPlugins(c *gin.Context) {
	ctx := c.Request.Context()
	agentID := c.Param("id")
	if agentID == serverPathID {
		agentID, _ = s.st.GetSetting(ctx, "server_agent_id")
		if agentID == "" {
			c.JSON(http.StatusNotFound, gin.H{"error": "未设置公网机 Agent（settings.server_agent_id）"})
			return
		}
	}
	rows, err := s.st.AgentPlugins(ctx, agentID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rows)
}

// validCron 轻量校验标准 5 段 cron（分 时 日 月 周）。
// 每段允许 `*`、数字、`,`、`-`、`*/N`；数字带范围校验（分 0-59、时 0-23、日 1-31、月 1-12、周 0-7）。
// 严格解析交给 agent 端 cron crate，这里只拦明显垃圾（不引重量级依赖）。
func validCron(expr string) bool {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return false
	}
	ranges := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for i, f := range fields {
		if f == "*" {
			continue
		}
		for _, part := range strings.Split(f, ",") {
			if part == "" {
				return false
			}
			if strings.HasPrefix(part, "*/") {
				// `*/N`：N 必须是纯数字且 ≥1
				rest := part[2:]
				if rest == "" {
					return false
				}
				step, err := strconv.Atoi(rest)
				if err != nil || step < 1 {
					return false
				}
				continue
			}
			lo, hi := 0, 0
			if idx := strings.IndexByte(part, '-'); idx >= 0 {
				a, err1 := strconv.Atoi(part[:idx])
				b, err2 := strconv.Atoi(part[idx+1:])
				if err1 != nil || err2 != nil {
					return false
				}
				lo, hi = a, b
			} else {
				n, err := strconv.Atoi(part)
				if err != nil {
					return false
				}
				lo, hi = n, n
			}
			r := ranges[i]
			if lo < r[0] || hi > r[1] || lo > hi {
				return false
			}
		}
	}
	return true
}
