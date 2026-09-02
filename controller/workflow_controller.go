package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/example/flowgo/model"
	"github.com/example/flowgo/scheduler"
	"github.com/example/flowgo/service"
)

// WorkflowController 工作流相关的 HTTP 请求处理器。
type WorkflowController struct {
	service   service.WorkflowService
	scheduler *scheduler.Scheduler
}

// NewWorkflowController 创建工作流控制器实例。
func NewWorkflowController(svc service.WorkflowService, sched *scheduler.Scheduler) *WorkflowController {
	return &WorkflowController{service: svc, scheduler: sched}
}

// List 处理 GET /api/workflows，返回全部工作流。
func (c *WorkflowController) List(w http.ResponseWriter, r *http.Request) {
	list, err := c.service.List()
	if err != nil {
		writeInternal(w, err)
		return
	}
	writeOK(w, list)
}

// Get 处理 GET /api/workflows/{id}，返回单个工作流详情。
func (c *WorkflowController) Get(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeBadRequest(w, err)
		return
	}
	wf, err := c.service.Get(id)
	if err != nil {
		writeNotFoundErr(w, err)
		return
	}
	writeOK(w, wf)
}

// Create 处理 POST /api/workflows，新建工作流。
func (c *WorkflowController) Create(w http.ResponseWriter, r *http.Request) {
	var wf model.Workflow
	if err := decodeJSON(r, &wf); err != nil {
		writeBadRequest(w, err)
		return
	}
	if err := c.service.Create(&wf); err != nil {
		writeBadRequest(w, err)
		return
	}
	c.reloadScheduler()
	writeCreated(w, wf)
}

// Update 处理 PUT /api/workflows/{id}，更新工作流。
func (c *WorkflowController) Update(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeBadRequest(w, err)
		return
	}
	var wf model.Workflow
	if err := decodeJSON(r, &wf); err != nil {
		writeBadRequest(w, err)
		return
	}
	wf.ID = id
	if err := c.service.Update(&wf); err != nil {
		writeStatusErr(w, err)
		return
	}
	c.reloadScheduler()
	writeOK(w, wf)
}

// Delete 处理 DELETE /api/workflows/{id}，删除工作流。
func (c *WorkflowController) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeBadRequest(w, err)
		return
	}
	if err := c.service.Delete(id); err != nil {
		writeStatusErr(w, err)
		return
	}
	c.reloadScheduler()
	writeJSON(w, http.StatusNoContent, nil)
}

// Run 处理 POST /api/workflows/{id}/run，手动触发一次执行。
// 立即返回 pending 状态的运行记录，执行进度由前端轮询 /api/runs/{id} 获取。
func (c *WorkflowController) Run(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		logger.Warn("手动触发运行：路径参数 id 非法", zap.String("raw", r.PathValue("id")))
		writeBadRequest(w, err)
		return
	}

	payload := readRunPayload(r)
	run, err := c.service.TriggerAsync(id, model.TriggerManual, payload)
	if err != nil {
		logger.Warn("手动触发运行失败",
			zap.Uint("工作流ID", id),
			zap.Error(err),
		)
		writeStatusErr(w, err)
		return
	}
	logger.Info("手动触发运行已受理，已返回待执行记录",
		zap.Uint("运行ID", run.ID),
		zap.Uint("工作流ID", id),
		zap.Int("触发负载长度", len(payload)),
	)
	writeCreated(w, run)
}

// NodeTypes 处理 GET /api/node-types，返回内置节点类型。
func (c *WorkflowController) NodeTypes(w http.ResponseWriter, r *http.Request) {
	writeOK(w, c.service.NodeTypes())
}

// AgentTools 处理 GET /api/agent-tools，返回 ai_agent 节点内部可调用的工具名称。
func (c *WorkflowController) AgentTools(w http.ResponseWriter, r *http.Request) {
	writeOK(w, c.service.AgentTools())
}

// reloadScheduler 工作流变更后刷新定时任务，失败仅记录日志不影响主流程。
func (c *WorkflowController) reloadScheduler() {
	if c.scheduler == nil {
		logger.Debug("未配置定时调度器，跳过重新加载")
		return
	}
	logger.Debug("工作流发生变更，准备重新加载定时任务")
	if err := c.scheduler.Reload(); err != nil {
		writeLogError(err)
	}
}

// parseID 解析路径参数 id。
func parseID(r *http.Request) (uint, error) {
	raw := r.PathValue("id")
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errors.New("invalid id: " + raw)
	}
	logger.Debug("解析请求路径 id 成功", zap.Uint("id", uint(v)))
	return uint(v), nil
}

// writeStatusErr 按错误类型选择合适的 HTTP 状态码。
func writeStatusErr(w http.ResponseWriter, err error) {
	if errors.Is(err, service.ErrWorkflowNotFound) || errors.Is(err, service.ErrRunNotFound) {
		writeNotFound(w, err)
		return
	}
	writeBadRequest(w, err)
}

// writeNotFoundErr 处理查询类接口的错误响应。
func writeNotFoundErr(w http.ResponseWriter, err error) {
	writeStatusErr(w, err)
}
