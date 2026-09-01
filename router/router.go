// Package router 定义 HTTP 路由注册逻辑，将 URL 路径映射到对应的 Controller 方法。
package router

import (
	"net/http"

	"github.com/example/flowgo/controller"
	"github.com/example/flowgo/scheduler"
	"github.com/example/flowgo/web"
)

// NewRouter 创建并配置 HTTP 请求路由器，注册健康检查、API、Webhook 与 Web 控制台路由。
func NewRouter(
	workflowController *controller.WorkflowController,
	runController *controller.RunController,
	webhookController *controller.WebhookController,
	statusController *controller.StatusController,
	sched *scheduler.Scheduler,
) *http.ServeMux {
	mux := http.NewServeMux()

	// 健康检查，附加调度任务数量便于观测。
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		jobs := 0
		if sched != nil {
			jobs = sched.Entries()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","message":"Service is running","data":{"scheduler_jobs":` +
			itoa(jobs) + `}}`))
	})

	// 系统状态监控。
	mux.HandleFunc("GET /status", statusController.GetStatus)

	// 工作流 CRUD 与手动触发。
	mux.HandleFunc("GET /api/workflows", workflowController.List)
	mux.HandleFunc("POST /api/workflows", workflowController.Create)
	mux.HandleFunc("GET /api/workflows/{id}", workflowController.Get)
	mux.HandleFunc("PUT /api/workflows/{id}", workflowController.Update)
	mux.HandleFunc("DELETE /api/workflows/{id}", workflowController.Delete)
	mux.HandleFunc("POST /api/workflows/{id}/run", workflowController.Run)

	// 内置节点类型。
	mux.HandleFunc("GET /api/node-types", workflowController.NodeTypes)

	// 运行记录与执行日志。
	mux.HandleFunc("GET /api/runs", runController.List)
	mux.HandleFunc("GET /api/runs/{id}", runController.Get)
	mux.HandleFunc("DELETE /api/runs/{id}", runController.Delete)

	// Webhook 外部触发入口，同时支持 POST（推荐）与 GET（便于浏览器与第三方回调）。
	mux.HandleFunc("POST /hook/{key}", webhookController.Trigger)
	mux.HandleFunc("GET /hook/{key}", webhookController.Trigger)

	// Web 控制台：单页应用与静态资源。
	index := web.Index()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write(index)
	})
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", web.AssetsHandler()))

	return mux
}

// itoa 将整数转为十进制字符串，避免在响应拼接处引入额外依赖。
func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	buf := [20]byte{}
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
