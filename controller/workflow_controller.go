package controller

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/example/flowgo/dsl"
	"github.com/example/flowgo/logger"
	"github.com/example/flowgo/model"
	"github.com/example/flowgo/scheduler"
	"github.com/example/flowgo/service"

	"go.uber.org/zap"
)

// maxDocBytes 文档导入请求体的大小上限，防止超大文档打爆内存。
const maxDocBytes = 1 << 20

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
	logger.Debug("查询工作流列表")
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
	logger.Debug("查询工作流详情", zap.Uint("工作流ID", id))
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
	logger.Debug("创建工作流", zap.String("名称", wf.Name))
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
	logger.Debug("更新工作流", zap.Uint("工作流ID", id))
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
	logger.Debug("删除工作流", zap.Uint("工作流ID", id))
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
	logger.Debug("手动触发工作流运行", zap.Uint("工作流ID", id))

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

// Import 处理 POST /api/workflows/import，请求体为 YAML 文档（Flow Spec）。
//
// 查询参数 dry_run=1 时只做解析与校验，返回解析结果但不落库，
// 供前端「校验」「应用到表单」使用；否则解析后直接创建工作流。
func (c *WorkflowController) Import(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxDocBytes))
	if err != nil {
		logger.Warn("导入文档：读取请求体失败", zap.Error(err))
		writeBadRequest(w, errors.New("读取文档内容失败"))
		return
	}
	if len(body) == 0 {
		writeBadRequest(w, errors.New("文档内容为空"))
		return
	}

	wf, err := dsl.Parse(body)
	if err != nil {
		logger.Warn("导入文档：解析失败", zap.Error(err))
		writeBadRequest(w, err)
		return
	}

	dryRun := r.URL.Query().Get("dry_run") == "1"
	logger.Debug("导入文档",
		zap.String("名称", wf.Name),
		zap.Bool("仅校验", dryRun),
		zap.Int("文档字节数", len(body)),
	)

	// 复用与表单保存完全一致的校验规则，保证两种编写方式产出同样的结果。
	if err := c.service.Validate(wf); err != nil {
		logger.Warn("导入文档：校验未通过",
			zap.String("名称", wf.Name),
			zap.Error(err),
		)
		writeBadRequest(w, err)
		return
	}

	if dryRun {
		writeOK(w, wf)
		return
	}

	if err := c.service.Create(wf); err != nil {
		logger.Warn("导入文档：创建工作流失败",
			zap.String("名称", wf.Name),
			zap.Error(err),
		)
		writeBadRequest(w, err)
		return
	}
	c.reloadScheduler()
	logger.Info("导入文档并创建工作流成功",
		zap.Uint("工作流ID", wf.ID),
		zap.String("名称", wf.Name),
	)
	writeCreated(w, wf)
}

// RenderDoc 处理 POST /api/workflows/export，把请求体中的工作流渲染为 YAML 文档。
// 与 Export 的区别是不要求工作流已落库，供前端为「尚未保存的编辑内容」实时生成文档。
func (c *WorkflowController) RenderDoc(w http.ResponseWriter, r *http.Request) {
	var wf model.Workflow
	if err := decodeJSON(r, &wf); err != nil {
		writeBadRequest(w, err)
		return
	}
	// 未保存的工作流没有 ID，跳过存在性校验，直接按文档规则渲染。
	if wf.Name == "" && wf.Graph == "" {
		writeBadRequest(w, errors.New("工作流内容为空，无法生成文档"))
		return
	}

	data, err := dsl.Export(&wf)
	if err != nil {
		logger.Warn("渲染文档失败", zap.Error(err))
		writeBadRequest(w, err)
		return
	}
	logger.Debug("渲染工作流文档成功", zap.Int("文档字节数", len(data)))
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// Export 处理 GET /api/workflows/{id}/export，把工作流导出为 YAML 文档。
// 导出结果与 Import 互为逆操作，可直接再次导入。
func (c *WorkflowController) Export(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeBadRequest(w, err)
		return
	}
	wf, err := c.service.Get(id)
	if err != nil {
		logger.Warn("导出文档：查询工作流失败",
			zap.Uint("工作流ID", id),
			zap.Error(err),
		)
		writeNotFoundErr(w, err)
		return
	}

	data, err := dsl.Export(wf)
	if err != nil {
		logger.Error("导出文档：序列化失败",
			zap.Uint("工作流ID", id),
			zap.Error(err),
		)
		writeInternal(w, err)
		return
	}

	logger.Debug("导出工作流文档成功",
		zap.Uint("工作流ID", id),
		zap.Int("文档字节数", len(data)),
	)
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// NodeTypes 处理 GET /api/node-types，返回内置节点类型。
func (c *WorkflowController) NodeTypes(w http.ResponseWriter, r *http.Request) {
	logger.Debug("查询内置节点类型")
	writeOK(w, c.service.NodeTypes())
}

// AgentTools 处理 GET /api/agent-tools，返回 ai_agent 节点内部可调用的工具名称。
func (c *WorkflowController) AgentTools(w http.ResponseWriter, r *http.Request) {
	logger.Debug("查询 ai_agent 节点可用工具")
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
