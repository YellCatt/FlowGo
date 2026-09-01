package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/example/flowgo/service"
)

// WebhookController 外部 Webhook 触发入口的处理器。
type WebhookController struct {
	service service.WorkflowService
}

// NewWebhookController 创建 Webhook 控制器实例。
func NewWebhookController(svc service.WorkflowService) *WebhookController {
	return &WebhookController{service: svc}
}

// Trigger 处理 /hook/{key} 请求，按密钥定位工作流并执行。
// 请求体、表单与查询参数会作为 trigger 变量注入节点模板。
func (c *WebhookController) Trigger(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		writeBadRequest(w, errMissingKey)
		return
	}

	payload := readRunPayload(r)
	run, err := c.service.TriggerByWebhook(r.Context(), key, payload)
	if err != nil {
		writeStatusErr(w, err)
		return
	}

	writeCreated(w, webhookResponse{
		RunID:  run.ID,
		Status: run.Status,
		Detail: "/api/runs/" + strconv.FormatUint(uint64(run.ID), 10),
	})
}

// webhookResponse Webhook 触发后的即时响应体。
type webhookResponse struct {
	RunID  uint   `json:"run_id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// errMissingKey Webhook 请求缺少密钥路径参数。
var errMissingKey = errors.New("webhook key is required")
