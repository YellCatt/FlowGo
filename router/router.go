// Package router 定义 HTTP 路由注册逻辑，将 URL 路径映射到对应的 Controller 方法。
package router

import (
	"net/http"
	"time"

	"github.com/example/flowgo/controller"
	"github.com/example/flowgo/logger"
	"github.com/example/flowgo/scheduler"
	"github.com/example/flowgo/web"

	"go.uber.org/zap"
)

// NewRouter 创建并配置 HTTP 请求路由器，注册健康检查、API、Webhook 与 Web 控制台路由。
func NewRouter(
	version string,
	workflowController *controller.WorkflowController,
	runController *controller.RunController,
	webhookController *controller.WebhookController,
	statusController *controller.StatusController,
	sched *scheduler.Scheduler,
) *http.ServeMux {
	mux := http.NewServeMux()

	// 健康检查，附加调度任务数量与版本号便于观测。
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		jobs := 0
		if sched != nil {
			jobs = sched.Entries()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","message":"Service is running","version":"` + version + `","data":{"scheduler_jobs":` +
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

	// 内置节点类型与 ai_agent 可调用的工具清单。
	mux.HandleFunc("GET /api/node-types", workflowController.NodeTypes)
	mux.HandleFunc("GET /api/agent-tools", workflowController.AgentTools)

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

	// 在外层挂载请求日志中间件，统一记录所有路由的访问信息。
	outer := http.NewServeMux()
	outer.Handle("/", loggingMiddleware(mux))
	return outer
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

// statusRecorder 包装 http.ResponseWriter 以记录响应状态码，便于访问日志输出。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader 记录首个写入的状态码。
func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Write 在隐式返回 200 时补全状态码记录。
func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// loggingMiddleware 记录每个 HTTP 请求的方法、路径、来源、状态码与耗时。
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 0}
		next.ServeHTTP(rec, r)
		elapsed := time.Since(start)
		logger.Debug("HTTP 请求处理完成",
			zap.String("方法", r.Method),
			zap.String("路径", r.URL.Path),
			zap.String("来源", r.RemoteAddr),
			zap.Int("状态码", rec.status),
			zap.Duration("耗时", elapsed),
		)
	})
}