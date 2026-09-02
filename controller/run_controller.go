package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/example/flowgo/logger"
	"github.com/example/flowgo/service"

	"go.uber.org/zap"
)

// RunController 运行记录相关的 HTTP 请求处理器。
type RunController struct {
	service service.RunService
}

// NewRunController 创建运行记录控制器实例。
func NewRunController(svc service.RunService) *RunController {
	return &RunController{service: svc}
}

// List 处理 GET /api/runs，支持按 workflow_id 过滤与 limit 限制。
func (c *RunController) List(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	var workflowID uint
	if raw := query.Get("workflow_id"); raw != "" {
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			logger.Warn("查询运行列表：workflow_id 参数非法",
				zap.String("raw", raw))
			writeBadRequest(w, errors.New("invalid workflow_id: "+raw))
			return
		}
		workflowID = uint(v)
	}

	limit := 50
	if raw := query.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			logger.Warn("查询运行列表：limit 参数非法",
				zap.String("raw", raw))
			writeBadRequest(w, errors.New("invalid limit: "+raw))
			return
		}
		limit = v
	}
	logger.Debug("查询运行列表",
		zap.Uint("过滤工作流ID", workflowID),
		zap.Int("上限", limit),
	)

	list, err := c.service.List(workflowID, limit)
	if err != nil {
		writeInternal(w, err)
		return
	}
	logger.Debug("运行列表查询完成",
		zap.Uint("过滤工作流ID", workflowID),
		zap.Int("返回条数", len(list)),
	)
	writeOK(w, list)
}

// Get 处理 GET /api/runs/{id}，返回运行详情及节点日志。
func (c *RunController) Get(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeBadRequest(w, err)
		return
	}
	logger.Debug("查询运行详情", zap.Uint("运行ID", id))
	detail, err := c.service.GetDetail(id)
	if err != nil {
		writeStatusErr(w, err)
		return
	}
	writeOK(w, detail)
}

// Delete 处理 DELETE /api/runs/{id}，删除运行记录及其日志。
func (c *RunController) Delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r)
	if err != nil {
		writeBadRequest(w, err)
		return
	}
	logger.Info("请求删除运行记录", zap.Uint("运行ID", id))
	if err := c.service.Delete(id); err != nil {
		writeStatusErr(w, err)
		return
	}
	logger.Info("运行记录已删除", zap.Uint("运行ID", id))
	writeJSON(w, http.StatusNoContent, nil)
}
